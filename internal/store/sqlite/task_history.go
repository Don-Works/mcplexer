package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// AppendTaskHistory inserts one immutable task history entry. Revision
// defaults to the next per-task revision when omitted.
func (d *DB) AppendTaskHistory(ctx context.Context, h *store.TaskHistoryEntry) error {
	if h == nil {
		return errors.New("AppendTaskHistory: nil history")
	}
	if strings.TrimSpace(h.TaskID) == "" {
		return errors.New("AppendTaskHistory: task_id required")
	}
	if strings.TrimSpace(h.WorkspaceID) == "" {
		return errors.New("AppendTaskHistory: workspace_id required")
	}
	if strings.TrimSpace(h.Action) == "" {
		return errors.New("AppendTaskHistory: action required")
	}
	if h.ID == "" {
		h.ID = ulid.Make().String()
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	if h.Revision <= 0 {
		if err := d.q.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(revision), 0) + 1
			FROM task_history
			WHERE task_id = ?`, h.TaskID).Scan(&h.Revision); err != nil {
			return fmt.Errorf("next task history revision: %w", err)
		}
	}
	changedFields := normalizeJSON(h.ChangedFieldsJSON, "[]")
	var related any
	if h.RelatedRevision > 0 {
		related = h.RelatedRevision
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_history (
			id, task_id, workspace_id, revision, action,
			actor_kind, actor_session_id, actor_peer_id, actor_user_id,
			source_kind, source_session_id, source_tool_call_id,
			workspace_path, origin_peer_id, related_revision,
			changed_fields_json, note, before_json, after_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.TaskID, h.WorkspaceID, h.Revision, h.Action,
		h.ActorKind, h.ActorSessionID, h.ActorPeerID, h.ActorUserID,
		h.SourceKind, h.SourceSessionID, h.SourceToolCallID,
		h.WorkspacePath, h.OriginPeerID, related,
		changedFields, h.Note, rawJSONOrNil(h.BeforeJSON), rawJSONOrNil(h.AfterJSON), h.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("append task history: %w", err)
	}
	return nil
}

// ListTaskHistory returns history rows for one task, newest revision first.
func (d *DB) ListTaskHistory(ctx context.Context, taskID string, limit int) ([]store.TaskHistoryEntry, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, errors.New("ListTaskHistory: task_id required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, task_id, workspace_id, revision, action,
			actor_kind, actor_session_id, actor_peer_id, actor_user_id,
			source_kind, source_session_id, source_tool_call_id,
			workspace_path, origin_peer_id, related_revision,
			changed_fields_json, note, before_json, after_json, created_at
		FROM task_history
		WHERE task_id = ?
		ORDER BY revision DESC
		LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("list task history: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	out := []store.TaskHistoryEntry{}
	for rows.Next() {
		h, err := scanTaskHistory(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// GetTaskHistoryRevision returns one history row by task id + revision.
func (d *DB) GetTaskHistoryRevision(ctx context.Context, taskID string, revision int) (*store.TaskHistoryEntry, error) {
	if strings.TrimSpace(taskID) == "" || revision <= 0 {
		return nil, errors.New("GetTaskHistoryRevision: task_id and positive revision required")
	}
	row := d.q.QueryRowContext(ctx, `
		SELECT id, task_id, workspace_id, revision, action,
			actor_kind, actor_session_id, actor_peer_id, actor_user_id,
			source_kind, source_session_id, source_tool_call_id,
			workspace_path, origin_peer_id, related_revision,
			changed_fields_json, note, before_json, after_json, created_at
		FROM task_history
		WHERE task_id = ? AND revision = ?`, taskID, revision)
	h, err := scanTaskHistory(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task history revision: %w", err)
	}
	return h, nil
}

// GetTaskIncludingDeleted returns a task row regardless of deleted_at.
func (d *DB) GetTaskIncludingDeleted(ctx context.Context, id string) (*store.Task, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE id = ?`, id)
	t, err := scanTask(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task including deleted: %w", err)
	}
	return t, nil
}

// RestoreTask replaces a task row from a history snapshot, including
// deleted_at. The restore itself receives a fresh updated_at + HLC.
func (d *DB) RestoreTask(ctx context.Context, t *store.Task) error {
	if t == nil || t.ID == "" {
		return errors.New("RestoreTask: id required")
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("RestoreTask: title required")
	}
	if strings.TrimSpace(t.WorkspaceID) == "" {
		return errors.New("RestoreTask: workspace_id required")
	}
	if t.Status == "" {
		t.Status = "open"
	}
	if t.Priority == "" {
		t.Priority = "normal"
	}
	if t.SourceKind == "" {
		t.SourceKind = store.TaskSourceAgent
	}
	if t.AssigneeOriginKind == "" {
		t.AssigneeOriginKind = store.TaskAssigneeLocal
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = time.Now().UTC()
	t.HlcAt = clock.Now()
	tags := normalizeJSON(t.TagsJSON, "[]")
	statusHistory := normalizeJSON(t.StatusHistoryJSON, "[]")
	pinned := 0
	if t.Pinned {
		pinned = 1
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE tasks SET
			workspace_id = ?,
			title = ?, description = ?, status = ?, closed_at = ?,
			priority = ?, due_at = ?, tags_json = ?, meta = ?,
			assignee_session_id = ?, assignee_origin_kind = ?, assignee_peer_id = ?, assignee_user_id = ?,
			assigned_by_session_id = ?, assigned_by_peer_id = ?, assigned_at = ?,
			lease_expires_at = ?,
			source_kind = ?, source_session_id = ?, source_tool_call_id = ?,
			created_by_session_id = ?, updated_by_session_id = ?, origin_peer_id = ?,
			status_history_json = ?, hlc_at = ?, pinned = ?, deleted_at = ?,
			created_at = ?, updated_at = ?
		WHERE id = ?`,
		t.WorkspaceID,
		t.Title, t.Description, t.Status, unixOrNil(t.ClosedAt),
		t.Priority, unixOrNil(t.DueAt), tags, t.Meta,
		nullString(t.AssigneeSessionID), t.AssigneeOriginKind, t.AssigneePeerID, t.AssigneeUserID,
		nullString(t.AssignedBySessionID), t.AssignedByPeerID, unixOrNil(t.AssignedAt),
		unixOrNil(t.LeaseExpiresAt),
		t.SourceKind, nullString(t.SourceSessionID), nullString(t.SourceToolCallID),
		nullString(t.CreatedBySessionID), nullString(t.UpdatedBySessionID), t.OriginPeerID,
		statusHistory, t.HlcAt, pinned, unixOrNil(t.DeletedAt),
		t.CreatedAt.Unix(), t.UpdatedAt.Unix(),
		t.ID,
	)
	if err != nil {
		return fmt.Errorf("restore task: %w", err)
	}
	return checkRowsAffected(res)
}

func scanTaskHistory(scan func(...any) error) (*store.TaskHistoryEntry, error) {
	var (
		h                      store.TaskHistoryEntry
		changed, before, after sql.NullString
		related                sql.NullInt64
		createdAt              int64
	)
	if err := scan(
		&h.ID, &h.TaskID, &h.WorkspaceID, &h.Revision, &h.Action,
		&h.ActorKind, &h.ActorSessionID, &h.ActorPeerID, &h.ActorUserID,
		&h.SourceKind, &h.SourceSessionID, &h.SourceToolCallID,
		&h.WorkspacePath, &h.OriginPeerID, &related,
		&changed, &h.Note, &before, &after, &createdAt,
	); err != nil {
		return nil, err
	}
	if related.Valid {
		h.RelatedRevision = int(related.Int64)
	}
	if changed.Valid {
		h.ChangedFieldsJSON = []byte(changed.String)
	}
	if before.Valid {
		h.BeforeJSON = []byte(before.String)
	}
	if after.Valid {
		h.AfterJSON = []byte(after.String)
	}
	h.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &h, nil
}

func rawJSONOrNil(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
