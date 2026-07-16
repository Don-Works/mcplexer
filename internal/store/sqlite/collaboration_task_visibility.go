package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

func (d *DB) GetTaskAccess(ctx context.Context, taskID string) (*store.TaskAccess, error) {
	return getTaskAccessQ(ctx, d.q, taskID)
}

func getTaskAccessQ(ctx context.Context, q queryable, taskID string) (*store.TaskAccess, error) {
	var access store.TaskAccess
	var updatedAt sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT t.id, t.workspace_id, COALESCE(s.share_id, ''),
		       COALESCE(t.owner_principal_id, ''), t.visibility,
		       t.visibility_epoch,
		       COALESCE(t.visibility_updated_by_principal_id, ''),
		       t.visibility_updated_at
		FROM tasks t
		LEFT JOIN p2p_workspace_shares s
		  ON s.local_workspace_id = t.workspace_id AND s.status = 'active'
		WHERE t.id = ? AND t.deleted_at IS NULL`, taskID).Scan(
		&access.TaskID, &access.WorkspaceID, &access.ShareID,
		&access.OwnerPrincipalID, &access.Visibility, &access.VisibilityEpoch,
		&access.VisibilityUpdatedByPrincipalID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	access.VisibilityUpdatedAt = unixTimePtr(updatedAt)
	rows, err := q.QueryContext(ctx, `
		SELECT principal_id FROM task_visibility_audience
		WHERE task_id = ? AND revoked_at IS NULL ORDER BY principal_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var principalID string
		if err := rows.Scan(&principalID); err != nil {
			return nil, err
		}
		access.AudiencePrincipalIDs = append(access.AudiencePrincipalIDs, principalID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var remoteMirror int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM p2p_workspace_memberships WHERE local_workspace_id = ?
	)`, access.WorkspaceID).Scan(&remoteMirror); err != nil {
		return nil, err
	}
	access.VisibilityEditable = remoteMirror == 0 && access.ShareID != ""
	return &access, nil
}

func (d *DB) SetTaskVisibility(ctx context.Context, change store.TaskVisibilityChange) (*store.TaskAccess, error) {
	if change.TaskID == "" || change.ActorPrincipalID == "" || !store.ValidTaskVisibility(change.Visibility) {
		return nil, fmt.Errorf("task, actor, and valid visibility are required")
	}
	audience, err := normalizedPrincipalIDs(change.AudiencePrincipalIDs)
	if err != nil {
		return nil, err
	}
	if change.Visibility == store.TaskVisibilityRestricted && len(audience) == 0 {
		return nil, fmt.Errorf("restricted visibility requires an audience")
	}
	if change.Visibility != store.TaskVisibilityRestricted && len(audience) != 0 {
		return nil, fmt.Errorf("only restricted visibility accepts an audience")
	}
	at := collaborationTime(change.At)
	var result *store.TaskAccess
	err = d.withTx(ctx, func(q queryable) error {
		actor, err := getPrincipalQ(ctx, q, change.ActorPrincipalID)
		if err != nil {
			return err
		}
		if actor.Status != store.PrincipalStatusActive {
			return fmt.Errorf("visibility actor is not active: %w", store.ErrConflict)
		}
		current, err := getTaskAccessQ(ctx, q, change.TaskID)
		if err != nil {
			return err
		}
		if !current.VisibilityEditable {
			return fmt.Errorf("task visibility is controlled by the workspace home: %w", store.ErrConflict)
		}
		if current.OwnerPrincipalID == "" {
			if !actor.IsLocalOwner {
				return fmt.Errorf("only the local owner may claim an orphaned task: %w", store.ErrConflict)
			}
			current.OwnerPrincipalID = actor.ID
		}
		if change.Visibility != store.TaskVisibilityPrivate && current.ShareID == "" {
			return fmt.Errorf("task workspace is not shared: %w", store.ErrConflict)
		}
		for _, principalID := range audience {
			principal, err := getPrincipalQ(ctx, q, principalID)
			if err != nil {
				return err
			}
			if principal.Status != store.PrincipalStatusActive {
				return fmt.Errorf("audience principal is not active: %w", store.ErrConflict)
			}
			canView, err := hasWorkspaceCapabilityQ(ctx, q, principalID, current.ShareID, store.CapabilityWorkspaceView, at)
			if err != nil || !canView {
				return fmt.Errorf("audience principal cannot view workspace: %w", store.ErrConflict)
			}
			canRead, err := hasWorkspaceCapabilityQ(ctx, q, principalID, current.ShareID, store.CapabilityTasksRead, at)
			if err != nil || !canRead {
				return fmt.Errorf("audience principal cannot read tasks: %w", store.ErrConflict)
			}
		}
		if current.Visibility == change.Visibility && equalStrings(current.AudiencePrincipalIDs, audience) &&
			current.OwnerPrincipalID != "" {
			result = current
			return nil
		}
		newEpoch := current.VisibilityEpoch + 1
		update, err := q.ExecContext(ctx, `
			UPDATE tasks SET owner_principal_id = ?, visibility = ?,
				visibility_epoch = ?, visibility_updated_by_principal_id = ?,
				visibility_updated_at = ?, hlc_at = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL AND visibility_epoch = ?`,
			current.OwnerPrincipalID, change.Visibility, newEpoch,
			change.ActorPrincipalID, at.Unix(), clock.Now(), at.Unix(),
			change.TaskID, current.VisibilityEpoch)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(update); err != nil {
			return fmt.Errorf("task visibility changed concurrently: %w", store.ErrConflict)
		}
		if _, err := q.ExecContext(ctx, `UPDATE task_visibility_audience
			SET revoked_at = ? WHERE task_id = ? AND revoked_at IS NULL`, at.Unix(), change.TaskID); err != nil {
			return err
		}
		for _, principalID := range audience {
			_, err := q.ExecContext(ctx, `INSERT INTO task_visibility_audience (
				id, task_id, principal_id, added_by_principal_id,
				visibility_epoch, added_at, revoked_at
			) VALUES (?, ?, ?, ?, ?, ?, NULL)`, ulid.Make().String(), change.TaskID,
				principalID, change.ActorPrincipalID, newEpoch, at.Unix())
			if err != nil {
				return mapConstraintError(err)
			}
		}
		details, _ := json.Marshal(map[string]any{
			"from": current.Visibility, "to": change.Visibility,
			"audience_principal_ids": audience, "visibility_epoch": newEpoch,
		})
		if err := appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			ShareID: current.ShareID, Event: "task.visibility.changed",
			ActorPrincipalID: change.ActorPrincipalID,
			SubjectKind:      "task", SubjectID: change.TaskID,
			DetailsJSON: details, CreatedAt: at,
		}); err != nil {
			return err
		}
		result, err = getTaskAccessQ(ctx, q, change.TaskID)
		return err
	})
	return result, err
}

func normalizedPrincipalIDs(input []string) ([]string, error) {
	seen := make(map[string]struct{}, len(input))
	ids := make([]string, 0, len(input))
	for _, id := range input {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("audience principal ID is empty")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate audience principal %q", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (d *DB) CanPrincipalReadTask(ctx context.Context, principalID, taskID string, at time.Time) (bool, error) {
	return canPrincipalReadTaskQ(ctx, d.q, principalID, taskID, collaborationTime(at))
}

func canPrincipalReadTaskQ(ctx context.Context, q queryable, principalID, taskID string, at time.Time) (bool, error) {
	principal, err := getPrincipalQ(ctx, q, principalID)
	if err != nil {
		return false, err
	}
	if principal.Status != store.PrincipalStatusActive {
		return false, nil
	}
	access, err := getTaskAccessQ(ctx, q, taskID)
	if err != nil {
		return false, err
	}
	if principal.IsLocalOwner || (access.Visibility == store.TaskVisibilityPrivate && access.OwnerPrincipalID == principalID) {
		return true, nil
	}
	if access.Visibility == store.TaskVisibilityPrivate || access.ShareID == "" {
		return false, nil
	}
	for _, capability := range []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead} {
		allowed, err := hasWorkspaceCapabilityQ(ctx, q, principalID, access.ShareID, capability, at)
		if err != nil || !allowed {
			return false, err
		}
	}
	if access.Visibility == store.TaskVisibilityWorkspace {
		return true, nil
	}
	for _, audienceID := range access.AudiencePrincipalIDs {
		if audienceID == principalID {
			return true, nil
		}
	}
	return false, nil
}

func (d *DB) RecordTaskDisclosure(ctx context.Context, disclosure *store.TaskDisclosure) error {
	if disclosure == nil || disclosure.TaskID == "" || disclosure.RecipientPrincipalID == "" ||
		disclosure.RecipientDeviceID == "" || disclosure.RecipientPeerID == "" {
		return fmt.Errorf("task and disclosure recipient are required")
	}
	if disclosure.ProjectionBytes < 0 || strings.TrimSpace(disclosure.EgressProfile) == "" {
		return fmt.Errorf("valid projection size and egress profile are required")
	}
	if _, err := hex.DecodeString(disclosure.ProjectionSHA256); err != nil ||
		len(disclosure.ProjectionSHA256) != 64 || strings.ToLower(disclosure.ProjectionSHA256) != disclosure.ProjectionSHA256 {
		return fmt.Errorf("projection hash must be lowercase SHA-256 hex")
	}
	disclosure.DisclosedAt = collaborationTime(disclosure.DisclosedAt)
	if disclosure.ID == "" {
		disclosure.ID = ulid.Make().String()
	}
	return d.withTx(ctx, func(q queryable) error {
		allowed, err := canPrincipalReadTaskQ(ctx, q, disclosure.RecipientPrincipalID, disclosure.TaskID, disclosure.DisclosedAt)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("recipient cannot read task: %w", store.ErrConflict)
		}
		access, err := getTaskAccessQ(ctx, q, disclosure.TaskID)
		if err != nil {
			return err
		}
		var accessEpoch int64
		err = q.QueryRowContext(ctx, `SELECT access_epoch FROM p2p_workspace_shares
			WHERE share_id = ? AND status = 'active'`, access.ShareID).Scan(&accessEpoch)
		if err != nil {
			return fmt.Errorf("active workspace share: %w", err)
		}
		var deviceCount int
		err = q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM p2p_principal_devices d
			JOIN p2p_principal_keys k ON k.id = d.identity_key_id
			WHERE d.id = ? AND d.peer_id = ? AND d.principal_id = ?
			  AND d.status = 'active' AND k.status = 'active'`,
			disclosure.RecipientDeviceID, disclosure.RecipientPeerID,
			disclosure.RecipientPrincipalID).Scan(&deviceCount)
		if err != nil || deviceCount != 1 {
			return fmt.Errorf("recipient device is not active: %w", store.ErrConflict)
		}
		disclosure.ShareID = access.ShareID
		disclosure.AccessEpoch = accessEpoch
		disclosure.VisibilityEpoch = access.VisibilityEpoch
		_, err = q.ExecContext(ctx, `INSERT INTO task_disclosures (
			id, task_id, share_id, recipient_principal_id, recipient_device_id,
			recipient_peer_id, access_epoch, visibility_epoch, projection_sha256,
			projection_bytes, egress_profile, disclosed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, disclosure.ID,
			disclosure.TaskID, disclosure.ShareID, disclosure.RecipientPrincipalID,
			disclosure.RecipientDeviceID, disclosure.RecipientPeerID,
			disclosure.AccessEpoch, disclosure.VisibilityEpoch,
			disclosure.ProjectionSHA256, disclosure.ProjectionBytes,
			disclosure.EgressProfile, disclosure.DisclosedAt.Unix())
		return mapConstraintError(err)
	})
}

func (d *DB) ListTaskDisclosures(ctx context.Context, taskID string, limit int) ([]store.TaskDisclosure, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := d.q.QueryContext(ctx, `SELECT
		id, task_id, share_id, recipient_principal_id,
		COALESCE(recipient_device_id, ''), recipient_peer_id,
		access_epoch, visibility_epoch, projection_sha256, projection_bytes,
		egress_profile, disclosed_at
		FROM task_disclosures WHERE task_id = ?
		ORDER BY disclosed_at DESC, id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var disclosures []store.TaskDisclosure
	for rows.Next() {
		var disclosure store.TaskDisclosure
		var disclosedAt int64
		if err := rows.Scan(&disclosure.ID, &disclosure.TaskID, &disclosure.ShareID,
			&disclosure.RecipientPrincipalID, &disclosure.RecipientDeviceID,
			&disclosure.RecipientPeerID, &disclosure.AccessEpoch,
			&disclosure.VisibilityEpoch, &disclosure.ProjectionSHA256,
			&disclosure.ProjectionBytes, &disclosure.EgressProfile,
			&disclosedAt); err != nil {
			return nil, err
		}
		disclosure.DisclosedAt = unixTime(disclosedAt)
		disclosures = append(disclosures, disclosure)
	}
	return disclosures, rows.Err()
}
