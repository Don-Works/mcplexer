package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) CreateWorkspace(ctx context.Context, w *store.Workspace) error {
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now

	tags := normalizeJSON(w.Tags, "[]")
	if w.Source == "" {
		w.Source = "api"
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO workspaces (id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.RootPath, nullString(w.ParentID), tags, w.DefaultPolicy, w.Source,
		formatTime(w.CreatedAt), formatTime(w.UpdatedAt),
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return nil
}

func (d *DB) GetWorkspace(ctx context.Context, id string) (*store.Workspace, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at
		FROM workspaces WHERE id = ?`, id)
	return scanWorkspace(row)
}

func (d *DB) GetWorkspaceByName(ctx context.Context, name string) (*store.Workspace, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at
		FROM workspaces WHERE name = ?`, name)
	return scanWorkspace(row)
}

func (d *DB) ListWorkspaces(ctx context.Context) ([]store.Workspace, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at
		FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Workspace
	for rows.Next() {
		w, err := scanWorkspaceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (d *DB) UpdateWorkspace(ctx context.Context, w *store.Workspace) error {
	w.UpdatedAt = time.Now().UTC()
	tags := normalizeJSON(w.Tags, "[]")
	if w.Source == "" {
		w.Source = "api"
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE workspaces
		SET name = ?, root_path = ?, parent_id = ?, tags = ?, default_policy = ?, source = ?, updated_at = ?
		WHERE id = ?`,
		w.Name, w.RootPath, nullString(w.ParentID), tags, w.DefaultPolicy, w.Source,
		formatTime(w.UpdatedAt), w.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return checkRowsAffected(res)
}

func (d *DB) DeleteWorkspace(ctx context.Context, id string) error {
	return d.withTx(ctx, func(q queryable) error {
		if _, err := q.ExecContext(ctx,
			`DELETE FROM route_rules WHERE workspace_id = ?`, id); err != nil {
			return fmt.Errorf("cascade delete route_rules: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`UPDATE tool_approvals SET status = 'cancelled', resolved_at = ?
			 WHERE workspace_id = ? AND status = 'pending'`,
			formatTime(time.Now().UTC()), id); err != nil {
			return fmt.Errorf("cascade cancel tool_approvals: %w", err)
		}
		// Tasks (migration 061) — soft-delete rather than hard-delete to
		// preserve audit/mesh references to task IDs. Companion tables
		// (vocab + peer bindings) drop hard since they have no audit
		// value once the workspace itself is gone.
		nowUnix := time.Now().UTC().Unix()
		if _, err := q.ExecContext(ctx,
			`UPDATE tasks SET deleted_at = ?, updated_at = ?
			 WHERE workspace_id = ? AND deleted_at IS NULL`,
			nowUnix, nowUnix, id); err != nil {
			return fmt.Errorf("cascade soft-delete tasks: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`DELETE FROM task_status_vocabulary WHERE workspace_id = ?`, id); err != nil {
			return fmt.Errorf("cascade delete task_status_vocabulary: %w", err)
		}
		// Task attachments (migration 078) — soft-delete in parallel with
		// the tasks soft-delete above so the audit trail is preserved.
		// The on-disk blobs under <data_dir>/attachments/<workspace_id>/
		// stay put; a future GC sweep can reclaim them out of band.
		if _, err := q.ExecContext(ctx,
			`UPDATE task_attachments SET deleted_at = ?
			 WHERE workspace_id = ? AND deleted_at IS NULL`,
			nowUnix, id); err != nil {
			return fmt.Errorf("cascade soft-delete task_attachments: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`UPDATE crm_person SET deleted_at = ?, updated_at = ?
			 WHERE workspace_id = ? AND deleted_at IS NULL`,
			nowUnix, nowUnix, id); err != nil {
			return fmt.Errorf("cascade soft-delete crm_person: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`DELETE FROM workspace_peer_bindings WHERE local_workspace_id = ?`, id); err != nil {
			return fmt.Errorf("cascade delete workspace_peer_bindings: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`DELETE FROM task_assign_throttles WHERE workspace_id = ?`, id); err != nil {
			return fmt.Errorf("cascade delete task_assign_throttles: %w", err)
		}
		res, err := q.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

func scanWorkspace(row *sql.Row) (*store.Workspace, error) {
	var w store.Workspace
	var createdAt, updatedAt, tags string
	var parentID sql.NullString
	err := row.Scan(&w.ID, &w.Name, &w.RootPath, &parentID, &tags,
		&w.DefaultPolicy, &w.Source, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if parentID.Valid {
		w.ParentID = parentID.String
	}
	w.Tags = json.RawMessage(tags)
	w.CreatedAt = parseTime(createdAt)
	w.UpdatedAt = parseTime(updatedAt)
	return &w, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceRow(row rowScanner) (*store.Workspace, error) {
	var w store.Workspace
	var createdAt, updatedAt, tags string
	var parentID sql.NullString
	err := row.Scan(&w.ID, &w.Name, &w.RootPath, &parentID, &tags,
		&w.DefaultPolicy, &w.Source, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if parentID.Valid {
		w.ParentID = parentID.String
	}
	w.Tags = json.RawMessage(tags)
	w.CreatedAt = parseTime(createdAt)
	w.UpdatedAt = parseTime(updatedAt)
	return &w, nil
}
