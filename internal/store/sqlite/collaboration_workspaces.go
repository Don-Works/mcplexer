package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const workspaceShareColumns = `
	share_id, local_workspace_id, home_peer_id, owner_principal_id,
	status, access_epoch, created_at, updated_at, revoked_at`

func (d *DB) CreateWorkspaceShare(ctx context.Context, share *store.WorkspaceShare) error {
	if share == nil {
		return fmt.Errorf("workspace share is required")
	}
	var err error
	if share.LocalWorkspaceID, err = requiredLabel(share.LocalWorkspaceID, "local workspace ID"); err != nil {
		return err
	}
	if share.HomePeerID, err = requiredLabel(share.HomePeerID, "home peer ID"); err != nil {
		return err
	}
	if share.OwnerPrincipalID == "" {
		return fmt.Errorf("owner principal ID is required")
	}
	if share.ShareID == "" {
		share.ShareID = ulid.Make().String()
	}
	if share.Status == "" {
		share.Status = store.WorkspaceShareStatusActive
	}
	if share.Status != store.WorkspaceShareStatusActive {
		return fmt.Errorf("new workspace share must be active")
	}
	if share.AccessEpoch == 0 {
		share.AccessEpoch = 1
	}
	if share.AccessEpoch != 1 {
		return fmt.Errorf("new workspace share must start at access epoch 1")
	}
	now := collaborationTime(share.CreatedAt)
	share.CreatedAt, share.UpdatedAt = now, now
	return d.withTx(ctx, func(q queryable) error {
		owner, err := getPrincipalQ(ctx, q, share.OwnerPrincipalID)
		if err != nil {
			return err
		}
		if owner.Status != store.PrincipalStatusActive {
			return fmt.Errorf("workspace owner is not active: %w", store.ErrConflict)
		}
		_, err = q.ExecContext(ctx, `
			INSERT INTO p2p_workspace_shares (
				share_id, local_workspace_id, home_peer_id, owner_principal_id,
				status, access_epoch, created_at, updated_at, revoked_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			share.ShareID, share.LocalWorkspaceID, share.HomePeerID,
			share.OwnerPrincipalID, share.Status, share.AccessEpoch,
			share.CreatedAt.Unix(), share.UpdatedAt.Unix())
		if err != nil {
			return mapConstraintError(err)
		}
		_, err = q.ExecContext(ctx, `
			INSERT INTO p2p_workspace_policies (
				share_id, default_visibility, agent_visibility_ceiling,
				widening_requires_approval, egress_profile, allow_remote_evidence,
				updated_by_principal_id, created_at, updated_at
			) VALUES (?, 'private', 'private', 1, 'task-safe-v1', 0, ?, ?, ?)`,
			share.ShareID, share.OwnerPrincipalID, now.Unix(), now.Unix())
		return err
	})
}

func (d *DB) GetWorkspaceShare(ctx context.Context, shareID string) (*store.WorkspaceShare, error) {
	return getWorkspaceShareByQ(ctx, d.q, "share_id", shareID)
}

func (d *DB) GetWorkspaceShareByLocalWorkspaceID(ctx context.Context, workspaceID string) (*store.WorkspaceShare, error) {
	return getWorkspaceShareByQ(ctx, d.q, "local_workspace_id", workspaceID)
}

func getWorkspaceShareQ(ctx context.Context, q queryable, shareID string) (*store.WorkspaceShare, error) {
	return getWorkspaceShareByQ(ctx, q, "share_id", shareID)
}

func getWorkspaceShareByQ(ctx context.Context, q queryable, field, value string) (*store.WorkspaceShare, error) {
	if field != "share_id" && field != "local_workspace_id" {
		return nil, fmt.Errorf("unsupported workspace share lookup")
	}
	share, err := scanWorkspaceShare(q.QueryRowContext(ctx,
		`SELECT `+workspaceShareColumns+` FROM p2p_workspace_shares WHERE `+field+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return share, err
}

func (d *DB) ListWorkspaceShares(ctx context.Context) ([]store.WorkspaceShare, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+workspaceShareColumns+`
		FROM p2p_workspace_shares ORDER BY created_at, share_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var shares []store.WorkspaceShare
	for rows.Next() {
		share, err := scanWorkspaceShare(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, *share)
	}
	return shares, rows.Err()
}

func scanWorkspaceShare(scanner rowScanner) (*store.WorkspaceShare, error) {
	var share store.WorkspaceShare
	var createdAt, updatedAt int64
	var revokedAt sql.NullInt64
	err := scanner.Scan(&share.ShareID, &share.LocalWorkspaceID, &share.HomePeerID,
		&share.OwnerPrincipalID, &share.Status, &share.AccessEpoch,
		&createdAt, &updatedAt, &revokedAt)
	if err != nil {
		return nil, err
	}
	share.CreatedAt, share.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	share.RevokedAt = unixTimePtr(revokedAt)
	return &share, nil
}

func (d *DB) SetWorkspaceGrants(ctx context.Context, set store.WorkspaceGrantSet) (int64, []store.WorkspaceGrant, error) {
	at := collaborationTime(set.At)
	if set.ShareID == "" || set.PrincipalID == "" || set.CreatedByPrincipalID == "" {
		return 0, nil, fmt.Errorf("share, principal, and grant actor are required")
	}
	if set.ExpiresAt != nil && !collaborationTime(*set.ExpiresAt).After(at) {
		return 0, nil, fmt.Errorf("grant expiry must be in the future")
	}
	constraints, err := collaborationJSON(set.ConstraintsJSON)
	if err != nil {
		return 0, nil, err
	}
	capabilities, err := normalizedCapabilities(set.Capabilities)
	if err != nil {
		return 0, nil, err
	}
	var epoch int64
	var grants []store.WorkspaceGrant
	err = d.withTx(ctx, func(q queryable) error {
		share, err := getWorkspaceShareQ(ctx, q, set.ShareID)
		if err != nil {
			return err
		}
		if share.Status != store.WorkspaceShareStatusActive {
			return fmt.Errorf("workspace share is revoked: %w", store.ErrConflict)
		}
		for _, id := range []string{set.PrincipalID, set.CreatedByPrincipalID} {
			principal, err := getPrincipalQ(ctx, q, id)
			if err != nil {
				return err
			}
			if principal.Status != store.PrincipalStatusActive {
				return fmt.Errorf("principal %s is not active: %w", id, store.ErrConflict)
			}
		}
		current, err := listWorkspaceGrantsQ(ctx, q, set.ShareID, set.PrincipalID, false)
		if err != nil {
			return err
		}
		effective := effectiveWorkspaceGrants(current, at)
		if workspaceGrantSetEqual(effective, capabilities, constraints, set.ExpiresAt) {
			epoch, grants = share.AccessEpoch, effective
			return nil
		}
		if len(effective) == 0 && len(capabilities) == 0 {
			// Expired rows can be retired without advancing the security epoch;
			// they already authorize nothing.
			_, err := q.ExecContext(ctx, `UPDATE p2p_workspace_grants SET revoked_at = ?
				WHERE share_id = ? AND principal_id = ? AND revoked_at IS NULL`,
				at.Unix(), set.ShareID, set.PrincipalID)
			epoch = share.AccessEpoch
			return err
		}
		result, err := q.ExecContext(ctx, `
			UPDATE p2p_workspace_shares
			SET access_epoch = access_epoch + 1, updated_at = ?
			WHERE share_id = ? AND status = 'active'`, at.Unix(), set.ShareID)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(result); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE p2p_workspace_grants SET revoked_at = ?
			WHERE share_id = ? AND principal_id = ? AND revoked_at IS NULL`,
			at.Unix(), set.ShareID, set.PrincipalID); err != nil {
			return err
		}
		if err := q.QueryRowContext(ctx, `SELECT access_epoch FROM p2p_workspace_shares WHERE share_id = ?`, set.ShareID).Scan(&epoch); err != nil {
			return err
		}
		grants = make([]store.WorkspaceGrant, 0, len(capabilities))
		for _, capability := range capabilities {
			grant := store.WorkspaceGrant{
				ID: ulid.Make().String(), ShareID: set.ShareID,
				PrincipalID: set.PrincipalID, Capability: capability,
				ConstraintsJSON: constraints, CreatedByPrincipalID: set.CreatedByPrincipalID,
				GrantedEpoch: epoch, CreatedAt: at, ExpiresAt: set.ExpiresAt,
			}
			if err := insertWorkspaceGrantQ(ctx, q, &grant); err != nil {
				return err
			}
			grants = append(grants, grant)
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			ShareID: set.ShareID, Event: "workspace.grants.changed",
			ActorPrincipalID: set.CreatedByPrincipalID,
			SubjectKind:      "principal", SubjectID: set.PrincipalID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"capabilities": capabilities,
				"access_epoch": epoch,
			}),
			CreatedAt: at,
		})
	})
	return epoch, grants, err
}

func normalizedCapabilities(input []string) ([]string, error) {
	seen := make(map[string]struct{}, len(input))
	capabilities := make([]string, 0, len(input))
	for _, capability := range input {
		if !store.ValidWorkspaceCapability(capability) {
			return nil, fmt.Errorf("unknown workspace capability %q", capability)
		}
		if _, exists := seen[capability]; exists {
			return nil, fmt.Errorf("duplicate workspace capability %q", capability)
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	return capabilities, nil
}

func effectiveWorkspaceGrants(grants []store.WorkspaceGrant, at time.Time) []store.WorkspaceGrant {
	effective := make([]store.WorkspaceGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.RevokedAt == nil && (grant.ExpiresAt == nil || grant.ExpiresAt.After(at)) {
			effective = append(effective, grant)
		}
	}
	return effective
}

func workspaceGrantSetEqual(current []store.WorkspaceGrant, capabilities []string, constraints []byte, expiresAt *time.Time) bool {
	if len(current) != len(capabilities) {
		return false
	}
	byCapability := make(map[string]store.WorkspaceGrant, len(current))
	for _, grant := range current {
		byCapability[grant.Capability] = grant
	}
	for _, capability := range capabilities {
		grant, exists := byCapability[capability]
		if !exists || !bytes.Equal(grant.ConstraintsJSON, constraints) || !sameUnixTime(grant.ExpiresAt, expiresAt) {
			return false
		}
	}
	return true
}

func sameUnixTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Unix() == b.Unix()
}

func insertWorkspaceGrantQ(ctx context.Context, q queryable, grant *store.WorkspaceGrant) error {
	constraints, err := collaborationJSON(grant.ConstraintsJSON)
	if err != nil {
		return err
	}
	grant.ConstraintsJSON = constraints
	_, err = q.ExecContext(ctx, `INSERT INTO p2p_workspace_grants (
		id, share_id, principal_id, capability, constraints_json,
		created_by_principal_id, granted_epoch, created_at, expires_at, revoked_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, grant.ID, grant.ShareID,
		grant.PrincipalID, grant.Capability, string(constraints),
		grant.CreatedByPrincipalID, grant.GrantedEpoch, grant.CreatedAt.Unix(),
		nullableUnix(grant.ExpiresAt), nullableUnix(grant.RevokedAt))
	return mapConstraintError(err)
}

func (d *DB) ListWorkspaceGrants(ctx context.Context, shareID string, includeRevoked bool) ([]store.WorkspaceGrant, error) {
	return listWorkspaceGrantsQ(ctx, d.q, shareID, "", includeRevoked)
}

const workspaceGrantColumns = `
	id, share_id, principal_id, capability, constraints_json,
	created_by_principal_id, granted_epoch, created_at, expires_at, revoked_at`

func listWorkspaceGrantsQ(ctx context.Context, q queryable, shareID, principalID string, includeRevoked bool) ([]store.WorkspaceGrant, error) {
	query := `SELECT ` + workspaceGrantColumns + ` FROM p2p_workspace_grants WHERE share_id = ?`
	args := []any{shareID}
	if principalID != "" {
		query += ` AND principal_id = ?`
		args = append(args, principalID)
	}
	if !includeRevoked {
		query += ` AND revoked_at IS NULL`
	}
	query += ` ORDER BY principal_id, capability, created_at DESC`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var grants []store.WorkspaceGrant
	for rows.Next() {
		grant, err := scanWorkspaceGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, *grant)
	}
	return grants, rows.Err()
}

func scanWorkspaceGrant(scanner rowScanner) (*store.WorkspaceGrant, error) {
	var grant store.WorkspaceGrant
	var constraints string
	var createdAt int64
	var expiresAt, revokedAt sql.NullInt64
	err := scanner.Scan(&grant.ID, &grant.ShareID, &grant.PrincipalID,
		&grant.Capability, &constraints, &grant.CreatedByPrincipalID,
		&grant.GrantedEpoch, &createdAt, &expiresAt, &revokedAt)
	if err != nil {
		return nil, err
	}
	grant.CreatedAt = unixTime(createdAt)
	grant.ConstraintsJSON = []byte(constraints)
	grant.ExpiresAt, grant.RevokedAt = unixTimePtr(expiresAt), unixTimePtr(revokedAt)
	return &grant, nil
}

func (d *DB) HasWorkspaceCapability(ctx context.Context, principalID, shareID, capability string, at time.Time) (bool, error) {
	return hasWorkspaceCapabilityQ(ctx, d.q, principalID, shareID, capability, collaborationTime(at))
}

func hasWorkspaceCapabilityQ(ctx context.Context, q queryable, principalID, shareID, capability string, at time.Time) (bool, error) {
	if !store.ValidWorkspaceCapability(capability) {
		return false, fmt.Errorf("unknown workspace capability %q", capability)
	}
	var exists int
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM p2p_workspace_grants g
			JOIN p2p_workspace_shares s ON s.share_id = g.share_id
			JOIN p2p_principals p ON p.id = g.principal_id
			WHERE g.principal_id = ? AND g.share_id = ? AND g.capability = ?
			  AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at > ?)
			  AND s.status = 'active' AND p.status = 'active'
		)`, principalID, shareID, capability, collaborationTime(at).Unix()).Scan(&exists)
	return exists == 1, err
}

func (d *DB) PutWorkspacePublicationPolicy(ctx context.Context, policy *store.WorkspacePublicationPolicy) error {
	if policy == nil || policy.ShareID == "" || policy.UpdatedByPrincipalID == "" {
		return fmt.Errorf("share and policy actor are required")
	}
	if policy.DefaultVisibility != store.TaskVisibilityPrivate && policy.DefaultVisibility != store.TaskVisibilityWorkspace {
		return fmt.Errorf("default visibility must be private or workspace")
	}
	if !store.ValidTaskVisibility(policy.AgentVisibilityCeiling) {
		return fmt.Errorf("invalid agent visibility ceiling")
	}
	if visibilityRank(policy.DefaultVisibility) > visibilityRank(policy.AgentVisibilityCeiling) {
		return fmt.Errorf("default visibility exceeds agent visibility ceiling")
	}
	if policy.AllowRemoteEvidence {
		return fmt.Errorf("remote evidence is not supported by the collaboration wire")
	}
	policy.EgressProfile = strings.TrimSpace(policy.EgressProfile)
	if policy.EgressProfile == "" {
		return fmt.Errorf("egress profile is required")
	}
	now := collaborationTime(policy.UpdatedAt)
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	policy.UpdatedAt = now
	return d.withTx(ctx, func(q queryable) error {
		share, err := getWorkspaceShareQ(ctx, q, policy.ShareID)
		if err != nil {
			return err
		}
		if share.Status != store.WorkspaceShareStatusActive {
			return fmt.Errorf("workspace share is revoked: %w", store.ErrConflict)
		}
		if share.OwnerPrincipalID != policy.UpdatedByPrincipalID {
			return fmt.Errorf("only the workspace owner may update publication policy: %w", store.ErrConflict)
		}
		result, err := q.ExecContext(ctx, `
			INSERT INTO p2p_workspace_policies (
				share_id, default_visibility, agent_visibility_ceiling,
				widening_requires_approval, egress_profile, allow_remote_evidence,
				updated_by_principal_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(share_id) DO UPDATE SET
				default_visibility = excluded.default_visibility,
				agent_visibility_ceiling = excluded.agent_visibility_ceiling,
				widening_requires_approval = excluded.widening_requires_approval,
				egress_profile = excluded.egress_profile,
				allow_remote_evidence = excluded.allow_remote_evidence,
				updated_by_principal_id = excluded.updated_by_principal_id,
				updated_at = excluded.updated_at`,
			policy.ShareID, policy.DefaultVisibility, policy.AgentVisibilityCeiling,
			policy.WideningRequiresApproval, policy.EgressProfile,
			policy.AllowRemoteEvidence, policy.UpdatedByPrincipalID,
			policy.CreatedAt.Unix(), policy.UpdatedAt.Unix())
		if err != nil {
			return mapConstraintError(err)
		}
		if _, err := result.RowsAffected(); err != nil {
			return err
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			ShareID: policy.ShareID, Event: "workspace.policy.changed",
			ActorPrincipalID: policy.UpdatedByPrincipalID,
			SubjectKind:      "workspace", SubjectID: policy.ShareID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"default_visibility":         policy.DefaultVisibility,
				"agent_visibility_ceiling":   policy.AgentVisibilityCeiling,
				"widening_requires_approval": policy.WideningRequiresApproval,
				"egress_profile":             policy.EgressProfile,
				"allow_remote_evidence":      false,
			}),
			CreatedAt: now,
		})
	})
}

func visibilityRank(visibility string) int {
	switch visibility {
	case store.TaskVisibilityPrivate:
		return 0
	case store.TaskVisibilityRestricted:
		return 1
	case store.TaskVisibilityWorkspace:
		return 2
	default:
		return 99
	}
}

func (d *DB) GetWorkspacePublicationPolicy(ctx context.Context, shareID string) (*store.WorkspacePublicationPolicy, error) {
	var policy store.WorkspacePublicationPolicy
	var createdAt, updatedAt int64
	err := d.q.QueryRowContext(ctx, `
		SELECT share_id, default_visibility, agent_visibility_ceiling,
		       widening_requires_approval, egress_profile, allow_remote_evidence,
		       updated_by_principal_id, created_at, updated_at
		FROM p2p_workspace_policies WHERE share_id = ?`, shareID).Scan(
		&policy.ShareID, &policy.DefaultVisibility, &policy.AgentVisibilityCeiling,
		&policy.WideningRequiresApproval, &policy.EgressProfile,
		&policy.AllowRemoteEvidence, &policy.UpdatedByPrincipalID,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	policy.CreatedAt, policy.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	return &policy, nil
}
