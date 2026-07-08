// monitoring_host.go — sqlite impl of the RemoteHost slice of
// store.MonitoringStore (migration 128). Validation runs here so every
// surface (MCP admin tools, REST, future importers) gets the same
// rejections — see store.ValidateRemoteHost + ADR 0007.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

const remoteHostCols = `id, workspace_id, name, ssh_user, ssh_host,
    ssh_port, auth_scope_id, host_key_pin, enabled, created_at, updated_at`

// CreateRemoteHost inserts a new host row. ID is generated when empty;
// SSHPort defaults to 22.
func (d *DB) CreateRemoteHost(ctx context.Context, h *store.RemoteHost) error {
	if h == nil {
		return errors.New("CreateRemoteHost: host required")
	}
	if h.WorkspaceID == "" {
		return errors.New("CreateRemoteHost: workspace_id required")
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if err := store.ValidateRemoteHost(h); err != nil {
		return err
	}
	if h.ID == "" {
		h.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if h.CreatedAt.IsZero() {
		h.CreatedAt = now
	}
	h.UpdatedAt = now
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO remote_hosts (`+remoteHostCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.WorkspaceID, h.Name, h.SSHUser, h.SSHHost,
		h.SSHPort, h.AuthScopeID, h.HostKeyPin, boolToInt(h.Enabled),
		formatTime(h.CreatedAt), formatTime(h.UpdatedAt),
	)
	return mapConstraintError(err)
}

// GetRemoteHost returns one row by id or ErrRemoteHostNotFound.
func (d *DB) GetRemoteHost(ctx context.Context, id string) (*store.RemoteHost, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+remoteHostCols+` FROM remote_hosts WHERE id = ?`, id)
	h, err := scanRemoteHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrRemoteHostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get remote host: %w", err)
	}
	return h, nil
}

// ListRemoteHosts returns every host in a workspace ordered by name.
func (d *DB) ListRemoteHosts(ctx context.Context, workspaceID string) ([]*store.RemoteHost, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+remoteHostCols+` FROM remote_hosts
		WHERE workspace_id = ? ORDER BY name ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list remote hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*store.RemoteHost{}
	for rows.Next() {
		h, err := scanRemoteHost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan remote host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// UpdateRemoteHost persists a full host row (read-modify-write at the
// caller). Returns ErrRemoteHostNotFound when the row is missing.
func (d *DB) UpdateRemoteHost(ctx context.Context, h *store.RemoteHost) error {
	if h == nil || h.ID == "" {
		return errors.New("UpdateRemoteHost: id required")
	}
	if err := store.ValidateRemoteHost(h); err != nil {
		return err
	}
	h.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE remote_hosts SET name = ?, ssh_user = ?, ssh_host = ?,
			ssh_port = ?, auth_scope_id = ?, host_key_pin = ?,
			enabled = ?, updated_at = ?
		WHERE id = ?`,
		h.Name, h.SSHUser, h.SSHHost, h.SSHPort, h.AuthScopeID,
		h.HostKeyPin, boolToInt(h.Enabled), formatTime(h.UpdatedAt), h.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return requireRowAffected(res, store.ErrRemoteHostNotFound)
}

// DeleteRemoteHost hard-deletes a host; its log_sources cascade.
func (d *DB) DeleteRemoteHost(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx, `DELETE FROM remote_hosts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete remote host: %w", err)
	}
	return requireRowAffected(res, store.ErrRemoteHostNotFound)
}

// SetRemoteHostPin records the TOFU host-key fingerprint. Pass "" to
// clear before a deliberate operator re-pin (ADR 0007 §3).
func (d *DB) SetRemoteHostPin(ctx context.Context, id, pin string) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE remote_hosts SET host_key_pin = ?, updated_at = ?
		WHERE id = ?`, pin, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set remote host pin: %w", err)
	}
	return requireRowAffected(res, store.ErrRemoteHostNotFound)
}

func scanRemoteHost(row interface{ Scan(...any) error }) (*store.RemoteHost, error) {
	var h store.RemoteHost
	var enabled int
	var createdAt, updatedAt string
	err := row.Scan(&h.ID, &h.WorkspaceID, &h.Name, &h.SSHUser, &h.SSHHost,
		&h.SSHPort, &h.AuthScopeID, &h.HostKeyPin, &enabled,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	h.Enabled = enabled != 0
	h.CreatedAt = parseTime(createdAt)
	h.UpdatedAt = parseTime(updatedAt)
	return &h, nil
}

// requireRowAffected converts a zero-rows UPDATE/DELETE into the
// entity's sentinel not-found error.
func requireRowAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}
