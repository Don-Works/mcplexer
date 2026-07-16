package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const workspaceMembershipColumns = `
	share_id, home_peer_id, remote_workspace_id, local_workspace_id,
	workspace_name, capabilities_json, access_epoch, status,
	cursor_hlc, joined_at, updated_at, revoked_at`

func (d *DB) UpsertWorkspaceMembership(ctx context.Context, membership *store.WorkspaceMembership) error {
	if membership == nil {
		return fmt.Errorf("workspace membership is required")
	}
	for label, value := range map[string]string{
		"share ID": membership.ShareID, "home peer ID": membership.HomePeerID,
		"remote workspace ID": membership.RemoteWorkspaceID,
		"local workspace ID":  membership.LocalWorkspaceID,
		"workspace name":      membership.WorkspaceName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	capabilities, err := normalizedCapabilities(membership.Capabilities)
	if err != nil {
		return err
	}
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	if membership.AccessEpoch < 1 {
		return fmt.Errorf("workspace membership access epoch must be positive")
	}
	if membership.Status == "" {
		membership.Status = store.WorkspaceShareStatusActive
	}
	if membership.Status != store.WorkspaceShareStatusActive {
		return fmt.Errorf("new workspace membership must be active")
	}
	now := collaborationTime(membership.UpdatedAt)
	if membership.JoinedAt.IsZero() {
		membership.JoinedAt = now
	} else {
		membership.JoinedAt = collaborationTime(membership.JoinedAt)
	}
	membership.UpdatedAt = now
	membership.Capabilities = capabilities
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO p2p_workspace_memberships (
			share_id, home_peer_id, remote_workspace_id, local_workspace_id,
			workspace_name, capabilities_json, access_epoch, status, cursor_hlc,
			joined_at, updated_at, revoked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', '', ?, ?, NULL)
		ON CONFLICT(share_id) DO UPDATE SET
			home_peer_id = excluded.home_peer_id,
			remote_workspace_id = excluded.remote_workspace_id,
			local_workspace_id = excluded.local_workspace_id,
			workspace_name = excluded.workspace_name,
			capabilities_json = excluded.capabilities_json,
			access_epoch = excluded.access_epoch,
			status = 'active', updated_at = excluded.updated_at, revoked_at = NULL`,
		membership.ShareID, membership.HomePeerID, membership.RemoteWorkspaceID,
		membership.LocalWorkspaceID, membership.WorkspaceName, string(capabilitiesJSON),
		membership.AccessEpoch, membership.JoinedAt.Unix(), membership.UpdatedAt.Unix())
	return mapConstraintError(err)
}

func (d *DB) GetWorkspaceMembership(ctx context.Context, shareID string) (*store.WorkspaceMembership, error) {
	return getWorkspaceMembershipByQ(ctx, d.q, "share_id", shareID)
}

func (d *DB) GetWorkspaceMembershipByLocalWorkspaceID(ctx context.Context, workspaceID string) (*store.WorkspaceMembership, error) {
	return getWorkspaceMembershipByQ(ctx, d.q, "local_workspace_id", workspaceID)
}

func getWorkspaceMembershipByQ(ctx context.Context, q queryable, field, value string) (*store.WorkspaceMembership, error) {
	if field != "share_id" && field != "local_workspace_id" {
		return nil, fmt.Errorf("unsupported workspace membership lookup")
	}
	membership, err := scanWorkspaceMembership(q.QueryRowContext(ctx,
		`SELECT `+workspaceMembershipColumns+` FROM p2p_workspace_memberships WHERE `+field+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return membership, err
}

func (d *DB) ListWorkspaceMemberships(ctx context.Context) ([]store.WorkspaceMembership, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+workspaceMembershipColumns+`
		FROM p2p_workspace_memberships ORDER BY workspace_name, share_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var memberships []store.WorkspaceMembership
	for rows.Next() {
		membership, err := scanWorkspaceMembership(rows)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, *membership)
	}
	return memberships, rows.Err()
}

func scanWorkspaceMembership(scanner rowScanner) (*store.WorkspaceMembership, error) {
	var membership store.WorkspaceMembership
	var capabilities string
	var joinedAt, updatedAt int64
	var revokedAt sql.NullInt64
	if err := scanner.Scan(&membership.ShareID, &membership.HomePeerID,
		&membership.RemoteWorkspaceID, &membership.LocalWorkspaceID,
		&membership.WorkspaceName, &capabilities, &membership.AccessEpoch,
		&membership.Status, &membership.CursorHLC, &joinedAt, &updatedAt, &revokedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(capabilities), &membership.Capabilities); err != nil {
		return nil, err
	}
	sort.Strings(membership.Capabilities)
	membership.JoinedAt, membership.UpdatedAt = unixTime(joinedAt), unixTime(updatedAt)
	membership.RevokedAt = unixTimePtr(revokedAt)
	return &membership, nil
}

func (d *DB) IsActiveWorkspaceHome(ctx context.Context, peerID string) (bool, error) {
	var exists int
	err := d.q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM p2p_workspace_memberships
		WHERE home_peer_id = ? AND status = 'active' AND revoked_at IS NULL
	)`, strings.TrimSpace(peerID)).Scan(&exists)
	return exists == 1, err
}

func (d *DB) AdvanceWorkspaceMembershipCursor(ctx context.Context, shareID, hlc string, at time.Time) error {
	if strings.TrimSpace(shareID) == "" || strings.TrimSpace(hlc) == "" {
		return fmt.Errorf("workspace membership share and cursor are required")
	}
	result, err := d.q.ExecContext(ctx, `UPDATE p2p_workspace_memberships
		SET cursor_hlc = CASE WHEN cursor_hlc < ? THEN ? ELSE cursor_hlc END,
			updated_at = ?
		WHERE share_id = ? AND status = 'active'`,
		hlc, hlc, collaborationTime(at).Unix(), shareID)
	if err != nil {
		return err
	}
	return checkRowsAffected(result)
}

func (d *DB) RevokeWorkspaceMembership(ctx context.Context, shareID string, at time.Time) error {
	result, err := d.q.ExecContext(ctx, `UPDATE p2p_workspace_memberships
		SET status = 'revoked', revoked_at = ?, updated_at = ?
		WHERE share_id = ? AND status = 'active'`, collaborationTime(at).Unix(), collaborationTime(at).Unix(), shareID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		membership, lookupErr := d.GetWorkspaceMembership(ctx, shareID)
		if lookupErr != nil {
			return lookupErr
		}
		if membership.Status != store.WorkspaceShareStatusRevoked {
			return store.ErrConflict
		}
	}
	return nil
}
