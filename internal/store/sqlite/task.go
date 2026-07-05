// task.go — SQLite implementation of store.TaskStore for the tasks
// table (migration 061). Mirrors memory.go conventions: Unix epoch
// INTEGER timestamps, normalizeJSON helper for JSON columns,
// soft-delete via deleted_at, ULID ids.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/taskstatus"
	"github.com/oklog/ulid/v2"
)

const taskSelectCols = `id, workspace_id, title, description, status, closed_at,
	priority, due_at, tags_json, meta,
	assignee_session_id, assignee_origin_kind, assignee_peer_id, assignee_user_id,
	assigned_by_session_id, assigned_by_peer_id, assigned_at,
	lease_expires_at,
	source_kind, source_session_id, source_tool_call_id,
	created_by_session_id, updated_by_session_id, origin_peer_id,
	status_history_json, hlc_at, pinned, deleted_at, created_at, updated_at`

// CreateTask inserts a new task row.
func (d *DB) CreateTask(ctx context.Context, t *store.Task) error {
	if t == nil {
		return errors.New("CreateTask: nil task")
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("CreateTask: title required")
	}
	if strings.TrimSpace(t.WorkspaceID) == "" {
		return errors.New("CreateTask: workspace_id required")
	}
	if t.ID == "" {
		t.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
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

	tags := normalizeJSON(t.TagsJSON, "[]")
	statusHistory := normalizeJSON(t.StatusHistoryJSON, "[]")
	pinned := 0
	if t.Pinned {
		pinned = 1
	}
	// Stamp HLC if the service layer didn't. Empty HlcAt on a Create is
	// the common path — the gossip layer doesn't decorate Create calls;
	// the store owns the contract that every row has a stamp before it
	// hits SQL.
	if t.HlcAt == "" {
		t.HlcAt = clock.Now()
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO tasks (
			id, workspace_id, title, description, status, closed_at,
			priority, due_at, tags_json, meta,
			assignee_session_id, assignee_origin_kind, assignee_peer_id, assignee_user_id,
			assigned_by_session_id, assigned_by_peer_id, assigned_at,
			lease_expires_at,
			source_kind, source_session_id, source_tool_call_id,
			created_by_session_id, updated_by_session_id, origin_peer_id,
			status_history_json, hlc_at, pinned, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.WorkspaceID, t.Title, t.Description, t.Status, unixOrNil(t.ClosedAt),
		t.Priority, unixOrNil(t.DueAt), tags, t.Meta,
		nullString(t.AssigneeSessionID), t.AssigneeOriginKind, t.AssigneePeerID, t.AssigneeUserID,
		nullString(t.AssignedBySessionID), t.AssignedByPeerID, unixOrNil(t.AssignedAt),
		unixOrNil(t.LeaseExpiresAt),
		t.SourceKind, nullString(t.SourceSessionID), nullString(t.SourceToolCallID),
		nullString(t.CreatedBySessionID), nullString(t.UpdatedBySessionID), t.OriginPeerID,
		statusHistory, t.HlcAt, pinned, t.CreatedAt.Unix(), t.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", mapConstraintError(err))
	}
	return nil
}

// GetTask returns one row by ID. Excludes soft-deleted rows.
func (d *DB) GetTask(ctx context.Context, id string) (*store.Task, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE id = ? AND deleted_at IS NULL`, id)
	t, err := scanTask(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// UpdateTask replaces the row. Caller is responsible for appending to
// StatusHistoryJSON before the call.
//
// HLC stamping policy: callers that want to preserve a remote-origin
// HLC (gossip apply) pre-populate t.HlcAt; everyone else lets the store
// stamp a fresh one here. This keeps the contract simple — every UPDATE
// touches hlc_at exactly once, and gossip apply gets the authority it
// needs to replay remote mutations without re-stamping them as local.
func (d *DB) UpdateTask(ctx context.Context, t *store.Task) error {
	if t == nil || t.ID == "" {
		return errors.New("UpdateTask: id required")
	}
	t.UpdatedAt = time.Now().UTC()
	if t.HlcAt == "" {
		t.HlcAt = clock.Now()
	}
	tags := normalizeJSON(t.TagsJSON, "[]")
	statusHistory := normalizeJSON(t.StatusHistoryJSON, "[]")
	pinned := 0
	if t.Pinned {
		pinned = 1
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE tasks SET
			workspace_id = ?, title = ?, description = ?, status = ?, closed_at = ?,
			priority = ?, due_at = ?, tags_json = ?, meta = ?,
			assignee_session_id = ?, assignee_origin_kind = ?, assignee_peer_id = ?, assignee_user_id = ?,
			assigned_by_session_id = ?, assigned_by_peer_id = ?, assigned_at = ?,
			lease_expires_at = ?,
			updated_by_session_id = ?,
			status_history_json = ?, hlc_at = ?, pinned = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		t.WorkspaceID, t.Title, t.Description, t.Status, unixOrNil(t.ClosedAt),
		t.Priority, unixOrNil(t.DueAt), tags, t.Meta,
		nullString(t.AssigneeSessionID), t.AssigneeOriginKind, t.AssigneePeerID, t.AssigneeUserID,
		nullString(t.AssignedBySessionID), t.AssignedByPeerID, unixOrNil(t.AssignedAt),
		unixOrNil(t.LeaseExpiresAt),
		nullString(t.UpdatedBySessionID),
		statusHistory, t.HlcAt, pinned, t.UpdatedAt.Unix(),
		t.ID,
	)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return checkRowsAffected(res)
}

// ClaimTask atomically assigns an unassigned task to claimantSession.
// The UPDATE is identical to UpdateTask but adds a CAS guard to the
// WHERE clause so only one claimant wins when two sessions race for
// the same unassigned row:
//
//	WHERE ... AND (assignee_session_id IS NULL
//	               OR assignee_session_id = ''
//	               OR assignee_session_id = ?)
//
// When zero rows match, we distinguish ErrNotFound (row doesn't exist)
// from ErrTaskAlreadyClaimed (row exists but someone else owns it) by
// probing with GetTask.
func (d *DB) ClaimTask(ctx context.Context, t *store.Task, claimantSession string) error {
	if t == nil || t.ID == "" {
		return errors.New("ClaimTask: id required")
	}
	if claimantSession == "" {
		return errors.New("ClaimTask: claimantSession required")
	}
	t.UpdatedAt = time.Now().UTC()
	if t.HlcAt == "" {
		t.HlcAt = clock.Now()
	}
	tags := normalizeJSON(t.TagsJSON, "[]")
	statusHistory := normalizeJSON(t.StatusHistoryJSON, "[]")
	pinned := 0
	if t.Pinned {
		pinned = 1
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE tasks SET
			title = ?, description = ?, status = ?, closed_at = ?,
			priority = ?, due_at = ?, tags_json = ?, meta = ?,
			assignee_session_id = ?, assignee_origin_kind = ?, assignee_peer_id = ?, assignee_user_id = ?,
			assigned_by_session_id = ?, assigned_by_peer_id = ?, assigned_at = ?,
			lease_expires_at = ?,
			updated_by_session_id = ?,
			status_history_json = ?, hlc_at = ?, pinned = ?, updated_at = ?
		WHERE id = ?
		  AND deleted_at IS NULL
		  AND (
			(COALESCE(assignee_session_id, '') = '' AND COALESCE(assignee_peer_id, '') = '' AND COALESCE(assignee_user_id, '') = '')
			OR assignee_session_id = ?
		  )`,
		t.Title, t.Description, t.Status, unixOrNil(t.ClosedAt),
		t.Priority, unixOrNil(t.DueAt), tags, t.Meta,
		nullString(t.AssigneeSessionID), t.AssigneeOriginKind, t.AssigneePeerID, t.AssigneeUserID,
		nullString(t.AssignedBySessionID), t.AssignedByPeerID, unixOrNil(t.AssignedAt),
		unixOrNil(t.LeaseExpiresAt),
		nullString(t.UpdatedBySessionID),
		statusHistory, t.HlcAt, pinned, t.UpdatedAt.Unix(),
		t.ID, claimantSession,
	)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim task rows: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := d.GetTask(ctx, t.ID); err != nil {
		return err
	}
	return store.ErrTaskAlreadyClaimed
}

// SoftDeleteTask stamps deleted_at. Idempotent in the sense that
// repeated calls on an already-deleted row return ErrNotFound.
func (d *DB) SoftDeleteTask(ctx context.Context, id string) error {
	now := time.Now().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE tasks SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return fmt.Errorf("soft delete task: %w", err)
	}
	return checkRowsAffected(res)
}

// ListTasks returns rows matching the filter, ordered by updated_at DESC.
func (d *DB) ListTasks(ctx context.Context, f store.TaskFilter) ([]store.Task, error) {
	where, args, err := buildTaskWhere(f)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	args = append(args, limit, f.Offset)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE `+where+`
		ORDER BY updated_at DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

// ListTaskIDsByPrefix returns task IDs in `workspaceID` whose ID
// starts with `prefix` (case-insensitive). Excludes soft-deleted rows.
// Capped at `limit`. ULIDs are uppercase by convention but the
// compose_into resolver should tolerate lowercase input from agents.
func (d *DB) ListTaskIDsByPrefix(ctx context.Context, workspaceID, prefix string, limit int) ([]string, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errors.New("ListTaskIDsByPrefix: workspace_id required")
	}
	if strings.TrimSpace(prefix) == "" {
		return nil, errors.New("ListTaskIDsByPrefix: prefix required")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	// LIKE pattern must NOT contain LIKE-metacharacters from a
	// hostile caller. ULIDs are [0-9A-HJKMNP-TV-Z] only (Crockford
	// alphabet) — strip anything outside that range AND ascii
	// alphanumerics defensively, then suffix the wildcard.
	clean := sanitizePrefix(prefix)
	if clean == "" {
		return nil, errors.New("ListTaskIDsByPrefix: prefix contains no valid ULID characters")
	}
	pattern := clean + "%"

	rows, err := d.q.QueryContext(ctx, `
		SELECT id
		FROM tasks
		WHERE workspace_id = ?
		  AND deleted_at IS NULL
		  AND id LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, workspaceID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("list task ids by prefix: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// sanitizePrefix uppercases input and drops anything not in the
// ULID Crockford alphabet (0-9 A-Z minus I L O U). Returns "" if the
// cleaned prefix is empty.
func sanitizePrefix(in string) string {
	upper := strings.ToUpper(in)
	var b strings.Builder
	for _, r := range upper {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z' && r != 'I' && r != 'L' && r != 'O' && r != 'U':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SearchTasks runs human task-id ref lookup (full id or displayed
// 6-char suffix) merged ahead of an FTS5 match intersected with the
// filter.
func (d *DB) SearchTasks(ctx context.Context, f store.TaskFilter, query string) ([]store.Task, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return d.ListTasks(ctx, f)
	}
	limit := taskSearchLimit(f.Limit)
	idRows, err := d.searchTasksByIDRef(ctx, f, q, limit)
	if err != nil {
		return nil, err
	}
	ftsRows, err := d.searchTasksFTS(ctx, f, q, limit)
	if err != nil {
		return nil, err
	}
	merged := mergeTaskSearchRows(idRows, ftsRows, limit)
	if len(merged) > 0 {
		return merged, nil
	}
	// Fallback: direct LIKE on title and tags_json when FTS + ID
	// search both return zero hits. Catches porter-stemmer
	// tokenisation mismatches, partial ID prefixes not yet long enough
	// for the ID-ref path, and tags stored as plain strings.
	titleRows, err := d.searchTasksTitleFallback(ctx, f, q, limit)
	if err != nil {
		return nil, err
	}
	tagRows, err := d.searchTasksTagFallback(ctx, f, q, limit)
	if err != nil {
		return nil, err
	}
	return mergeTaskSearchRows(titleRows, tagRows, limit), nil
}

func taskSearchLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func (d *DB) searchTasksFTS(ctx context.Context, f store.TaskFilter, query string, limit int) ([]store.Task, error) {
	where, args, err := buildTaskWhere(f)
	if err != nil {
		return nil, err
	}
	args = append([]any{escapeFTS5Query(query)}, args...)
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectColsPrefixed("t")+`
		FROM tasks t
		JOIN tasks_fts fts ON fts.rowid = t.rowid
		WHERE fts.tasks_fts MATCH ? AND `+strings.ReplaceAll(where, "tasks.", "t.")+`
		ORDER BY bm25(tasks_fts)
		LIMIT ?`, args...)
	if err != nil {
		return nil, rewriteFTS5Error(err, query)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func (d *DB) searchTasksTitleFallback(ctx context.Context, f store.TaskFilter, query string, limit int) ([]store.Task, error) {
	where, args, err := buildTaskWhere(f)
	if err != nil {
		return nil, err
	}
	likePattern := "%" + escapeLikePattern(query) + "%"
	where += " AND tasks.title LIKE ?"
	args = append(args, likePattern, limit)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE `+where+`
		ORDER BY updated_at DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search tasks title fallback: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func (d *DB) searchTasksTagFallback(ctx context.Context, f store.TaskFilter, query string, limit int) ([]store.Task, error) {
	where, args, err := buildTaskWhere(f)
	if err != nil {
		return nil, err
	}
	likePattern := "%" + escapeLikePattern(query) + "%"
	where += " AND tasks.tags_json LIKE ?"
	args = append(args, likePattern, limit)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE `+where+`
		ORDER BY updated_at DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search tasks tag fallback: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func (d *DB) searchTasksByIDRef(ctx context.Context, f store.TaskFilter, query string, limit int) ([]store.Task, error) {
	mode, ref := taskIDSearchRef(query)
	if mode == "" {
		return nil, nil
	}
	where, args, err := buildTaskWhere(f)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "full":
		where += " AND tasks.id = ?"
		args = append(args, ref)
	case "suffix":
		where += " AND substr(tasks.id, -6) = ?"
		args = append(args, ref)
	case "prefix":
		where += " AND tasks.id LIKE ?"
		args = append(args, ref+"%")
	default:
		return nil, nil
	}
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE `+where+`
		ORDER BY updated_at DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search tasks by id ref: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func taskIDSearchRef(query string) (mode, ref string) {
	s := strings.TrimSpace(query)
	if s == "" {
		return "", ""
	}
	for {
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "task:"):
			s = strings.TrimSpace(s[len("task:"):])
		case strings.HasPrefix(s, "#"):
			s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
		default:
			goto stripped
		}
	}
stripped:
	upper := strings.ToUpper(s)
	if sanitizePrefix(s) != upper {
		return "", ""
	}
	switch len(upper) {
	case 6:
		return "suffix", upper
	case 26:
		return "full", upper
	default:
		if len(upper) >= 2 {
			return "prefix", upper
		}
		return "", ""
	}
}

func mergeTaskSearchRows(primary, secondary []store.Task, limit int) []store.Task {
	if len(primary) == 0 {
		if len(secondary) > limit {
			return secondary[:limit]
		}
		return secondary
	}
	out := make([]store.Task, 0, len(primary)+len(secondary))
	seen := map[string]bool{}
	appendRow := func(t store.Task) {
		if seen[t.ID] {
			return
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	for _, t := range primary {
		appendRow(t)
	}
	for _, t := range secondary {
		appendRow(t)
	}
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

// ListTasksSinceHLC streams non-deleted tasks in the workspace whose
// HLC stamp is strictly greater than sinceHLC, ordered by HLC so gossip
// receivers apply events in local commit order.
func (d *DB) ListTasksSinceHLC(
	ctx context.Context, workspaceID, sinceHLC string, limit int,
) ([]store.Task, error) {
	if workspaceID == "" {
		return nil, errors.New("ListTasksSinceHLC: workspace_id required")
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE workspace_id = ? AND deleted_at IS NULL AND hlc_at > ?
		ORDER BY hlc_at ASC
		LIMIT ?`, workspaceID, sinceHLC, limit)
	if err != nil {
		return nil, fmt.Errorf("list tasks since hlc: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

// MaxHLCForWorkspace returns the highest hlc_at among non-deleted tasks
// in the workspace, or "" when none exist. Backs the task-sync client's
// per-workspace watermark.
func (d *DB) MaxHLCForWorkspace(ctx context.Context, workspaceID string) (string, error) {
	if workspaceID == "" {
		return "", errors.New("MaxHLCForWorkspace: workspace_id required")
	}
	var max string
	err := d.q.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(hlc_at), '') FROM tasks
		WHERE workspace_id = ? AND deleted_at IS NULL`, workspaceID).Scan(&max)
	if err != nil {
		return "", fmt.Errorf("max hlc for workspace: %w", err)
	}
	return max, nil
}

// CountTasksByStatus returns counts grouped by status for a workspace.
func (d *DB) CountTasksByStatus(ctx context.Context, workspaceID string) (map[string]int, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM tasks
		WHERE workspace_id = ? AND deleted_at IS NULL
		GROUP BY status
		ORDER BY status`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

func taskSelectColsPrefixed(prefix string) string {
	parts := strings.Split(taskSelectCols, ", ")
	for i, p := range parts {
		// Strip any leading whitespace from continuation lines.
		parts[i] = prefix + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// buildTaskWhere returns the WHERE clause + bound args for ListTasks /
// SearchTasks. Always anchors on deleted_at IS NULL unless filter says
// otherwise. Uses prefix "tasks." so the same builder works for both
// the bare table query and the FTS JOIN query.
func buildTaskWhere(f store.TaskFilter) (string, []any, error) {
	conds := []string{}
	args := []any{}
	if !f.IncludeDeleted {
		conds = append(conds, "tasks.deleted_at IS NULL")
	}
	if f.WorkspaceID != "" {
		conds = append(conds, "tasks.workspace_id = ?")
		args = append(args, f.WorkspaceID)
	}
	if f.Status != "" {
		conds = append(conds, "tasks.status = ?")
		args = append(args, f.Status)
	}
	if f.OnlyTerminal != nil {
		terminal := taskStatusTerminalPredicate("tasks")
		if *f.OnlyTerminal {
			conds = append(conds, "(tasks.closed_at IS NOT NULL OR "+terminal+")")
		} else {
			conds = append(conds, "tasks.closed_at IS NULL")
			conds = append(conds, "NOT "+terminal)
		}
	}
	if f.AssigneeSessionID != "" {
		conds = append(conds, "tasks.assignee_session_id = ?")
		args = append(args, f.AssigneeSessionID)
	}
	if f.AssigneeOriginKind != "" {
		conds = append(conds, "tasks.assignee_origin_kind = ?")
		args = append(args, f.AssigneeOriginKind)
	}
	if f.AssigneePeerID != "" {
		conds = append(conds, "tasks.assignee_peer_id = ?")
		args = append(args, f.AssigneePeerID)
	}
	if f.AssigneeUserID != "" {
		conds = append(conds, "tasks.assignee_user_id = ?")
		args = append(args, f.AssigneeUserID)
	}
	if f.AssignedBySessionID != "" {
		conds = append(conds, "tasks.assigned_by_session_id = ?")
		args = append(args, f.AssignedBySessionID)
	}
	if f.AssignedByPeerID != "" {
		conds = append(conds, "tasks.assigned_by_peer_id = ?")
		args = append(args, f.AssignedByPeerID)
	}
	if f.SourceSessionID != "" {
		conds = append(conds, "tasks.source_session_id = ?")
		args = append(args, f.SourceSessionID)
	}
	if f.OriginPeerID != "" {
		conds = append(conds, "tasks.origin_peer_id = ?")
		args = append(args, f.OriginPeerID)
	}
	if f.UpdatedAfter != nil {
		conds = append(conds, "tasks.updated_at >= ?")
		args = append(args, f.UpdatedAfter.Unix())
	}
	if f.CreatedAfter != nil {
		conds = append(conds, "tasks.created_at >= ?")
		args = append(args, f.CreatedAfter.Unix())
	}
	// Meta filters — translate to indexed SQL where we have a
	// generated column (composed_by), fall back to json_extract for
	// arbitrary keys. The dual-read service layer means a row whose
	// meta is still legacy frontmatter will not match any meta filter
	// — that's the intended behaviour (backfill rewrites them on the
	// next service-level write, or up-front via the post-migration
	// hook in backfillTasksMetaJSON).
	for k, v := range f.MetaMatch {
		if err := validMetaKey(k); err != nil {
			return "", nil, err
		}
		conds = append(conds, metaMatchSQL(k, "tasks"))
		args = append(args, v, v)
	}
	for _, k := range f.MetaHasKey {
		if err := validMetaKey(k); err != nil {
			return "", nil, err
		}
		conds = append(conds, metaHasKeySQL(k, "tasks"))
	}
	for k, opts := range f.MetaIn {
		if len(opts) == 0 {
			continue
		}
		if err := validMetaKey(k); err != nil {
			return "", nil, err
		}
		clause, vs := metaInSQL(k, opts, "tasks")
		conds = append(conds, clause)
		args = append(args, vs...)
	}
	for _, t := range f.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		conds = append(conds, "EXISTS (SELECT 1 FROM json_each(tasks.tags_json) WHERE lower(json_each.value) = ?)")
		args = append(args, t)
	}
	if len(conds) == 0 {
		return "1=1", args, nil
	}
	return strings.Join(conds, " AND "), args, nil
}

// metaMatchSQL emits the WHERE fragment that asserts "meta has key
// `key` whose value is the supplied parameter". The fragment is shaped
// as `<alias>.id IN (SELECT id FROM tasks WHERE ... UNION ALL ...)` so
// SQLite's planner sees TWO independent branches — the scalar-equality
// branch is indexable by the migration-078 expression indices (or the
// migration-072 virtual column for composed_by), and the array-branch
// keeps the json_each EXISTS subquery for the rare multi-element rows.
//
// The earlier shape (`<alias>.meta_composed_by = ? OR (... EXISTS ...)`)
// hand-wove the two cases together with a top-level OR, which defeats
// the OR-by-union optimisation when one OR arm carries a correlated
// subquery — the planner falls back to a full SCAN even when an index
// would have answered the scalar arm.
//
// Every json_extract call is guarded by json_valid(meta) so legacy
// frontmatter rows (pre-072 backfill) silently skip the filter
// instead of raising "malformed JSON" — they'll get rewritten by the
// next service-level mutation through MetaToJSON and become
// queryable then.
//
// The fragment binds TWO params per call (scalar branch + array-branch
// EXISTS); callers don't need to special-case the scalar-only path.
func metaMatchSQL(key, alias string) string {
	if key == "composed_by" {
		// composed_by has a virtual generated column (meta_composed_by)
		// from migration 072. The scalar branch hits its index directly;
		// the array branch falls back to json_each, gated by json_type
		// so json_each is only called on actual arrays (calling it on a
		// scalar raises "malformed JSON").
		return fmt.Sprintf(`%[1]s.id IN (`+
			`SELECT id FROM tasks WHERE meta_composed_by = ?`+
			` UNION ALL `+
			`SELECT id FROM tasks WHERE json_valid(meta)`+
			` AND json_type(meta, '$.composed_by') = 'array'`+
			` AND EXISTS (SELECT 1 FROM json_each(json_extract(meta, '$.composed_by'))`+
			` WHERE json_each.value = ?))`, alias)
	}
	jsonPath := "$." + key
	// Other keys: the scalar arm is backed by the partial expression
	// index from migration 078 (for the hot keys — branch, worktree,
	// pr, linear, mesh_thread, source_mesh_msg_id, composes,
	// touches_files). Keys without an index just SCAN this arm — same
	// cost as before but the indexed keys speed up dramatically.
	return fmt.Sprintf(`%[1]s.id IN (`+
		`SELECT id FROM tasks WHERE json_valid(meta) AND json_extract(meta, '%[2]s') = ?`+
		` UNION ALL `+
		`SELECT id FROM tasks WHERE json_valid(meta) AND json_type(meta, '%[2]s') = 'array'`+
		` AND EXISTS (SELECT 1 FROM json_each(json_extract(meta, '%[2]s'))`+
		` WHERE json_each.value = ?))`, alias, jsonPath)
}

// metaHasKeySQL emits the predicate "meta has a key called `key`".
// `key` is a controlled identifier (validated by the handler before
// reaching here) so inline interpolation is safe — there's no
// parameter slot we could bind it to since SQLite's JSON1 paths are
// expression strings, not placeholders.
//
// json_valid wrapper rationale: same as metaMatchSQL — legacy
// frontmatter rows silently fail the filter without raising.
func metaHasKeySQL(key, alias string) string {
	jsonPath := "$." + key
	return fmt.Sprintf(`(json_valid(%[1]s.meta) AND json_type(%[1]s.meta, '%[2]s') IS NOT NULL)`, alias, jsonPath)
}

// metaInSQL renders "value at `key` is one of `opts`". Implementation
// uses metaMatchSQL chained with OR — keeps the index path active
// for composed_by and the same EXISTS subquery for other keys. Each
// option binds two parameters (the scalar value plus the EXISTS
// duplicate) for the same reason as metaMatchSQL.
func metaInSQL(key string, opts []string, alias string) (string, []any) {
	clauses := make([]string, 0, len(opts))
	args := make([]any, 0, 2*len(opts))
	for _, v := range opts {
		clauses = append(clauses, metaMatchSQL(key, alias))
		args = append(args, v, v)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

func scanTasks(rows *sql.Rows) ([]store.Task, error) {
	var out []store.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

func scanTask(scan func(...any) error) (*store.Task, error) {
	var (
		t                                                      store.Task
		tags, statusHistory                                    string
		closedAt, dueAt, assignedAt, leaseExpiresAt, deletedAt sql.NullInt64
		assigneeSession, assignedBySession                     sql.NullString
		assigneeUserID                                         sql.NullString
		sourceSession, sourceToolCall, createdBy, updatedBy    sql.NullString
		createdAt, updatedAt                                   int64
		pinned                                                 int
	)
	if err := scan(
		&t.ID, &t.WorkspaceID, &t.Title, &t.Description, &t.Status, &closedAt,
		&t.Priority, &dueAt, &tags, &t.Meta,
		&assigneeSession, &t.AssigneeOriginKind, &t.AssigneePeerID, &assigneeUserID,
		&assignedBySession, &t.AssignedByPeerID, &assignedAt,
		&leaseExpiresAt,
		&t.SourceKind, &sourceSession, &sourceToolCall,
		&createdBy, &updatedBy, &t.OriginPeerID,
		&statusHistory, &t.HlcAt, &pinned, &deletedAt, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	t.AssigneeUserID = assigneeUserID.String
	t.TagsJSON = json.RawMessage(tags)
	t.StatusHistoryJSON = json.RawMessage(statusHistory)
	if closedAt.Valid {
		tt := time.Unix(closedAt.Int64, 0).UTC()
		t.ClosedAt = &tt
	}
	if dueAt.Valid {
		tt := time.Unix(dueAt.Int64, 0).UTC()
		t.DueAt = &tt
	}
	if assignedAt.Valid {
		tt := time.Unix(assignedAt.Int64, 0).UTC()
		t.AssignedAt = &tt
	}
	if leaseExpiresAt.Valid {
		tt := time.Unix(leaseExpiresAt.Int64, 0).UTC()
		t.LeaseExpiresAt = &tt
	}
	if deletedAt.Valid {
		tt := time.Unix(deletedAt.Int64, 0).UTC()
		t.DeletedAt = &tt
	}
	if assigneeSession.Valid {
		t.AssigneeSessionID = assigneeSession.String
	}
	if assignedBySession.Valid {
		t.AssignedBySessionID = assignedBySession.String
	}
	if sourceSession.Valid {
		t.SourceSessionID = sourceSession.String
	}
	if sourceToolCall.Valid {
		t.SourceToolCallID = sourceToolCall.String
	}
	if createdBy.Valid {
		t.CreatedBySessionID = createdBy.String
	}
	if updatedBy.Valid {
		t.UpdatedBySessionID = updatedBy.String
	}
	t.Pinned = pinned != 0
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &t, nil
}

// unixOrNil maps a nullable time pointer to a *int64 driver value.
func unixOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// HeartbeatTask bumps lease_expires_at to now+ttl ONLY when the caller
// matches the current assignee_session_id. Returns (true, nil) if a
// row matched + was bumped, (false, nil) if the caller is not the
// current assignee (silent no-op semantics — peers can't extend each
// other's leases). Excludes soft-deleted rows.
func (d *DB) HeartbeatTask(ctx context.Context, id, sessionID string, ttl time.Duration) (bool, error) {
	if id == "" || sessionID == "" {
		return false, nil
	}
	now := time.Now().UTC()
	expires := now.Add(ttl).Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE tasks
		   SET lease_expires_at = ?, updated_at = ?
		 WHERE id = ?
		   AND assignee_session_id = ?
		   AND deleted_at IS NULL`,
		expires, now.Unix(), id, sessionID,
	)
	if err != nil {
		return false, fmt.Errorf("heartbeat task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("heartbeat task rows: %w", err)
	}
	return n > 0, nil
}

var workingFallbackStatuses = func() []string {
	out := []string{}
	for status, kind := range taskstatus.DefaultKinds {
		if kind == taskstatus.KindWorking {
			out = append(out, status)
		}
	}
	return out
}()

// taskWorkingStatusPredicate is a SQL fragment (referencing the `tasks`
// table alias) that is TRUE when a row's freeform status counts as
// "actively being worked on". It mirrors Service.isWorkingStatus
// exactly: the per-workspace task_status_vocabulary kind='working'
// classification wins; absent ANY vocab row for that status the shared
// taskstatus working fallback applies so a fresh install with no declared
// vocab is still reclaimable. Keeping this in lock-step with the service-layer
// helper is load-bearing — the Clear* functions return the ids of the
// rows they reclaim and the service re-runs isWorkingStatus on each to
// decide whether to demote status; if the two predicates disagreed a
// row could be un-leased here yet left in a working status there.
var taskWorkingStatusPredicate = `(
		EXISTS (SELECT 1 FROM task_status_vocabulary v
		         WHERE v.workspace_id = tasks.workspace_id
		           AND v.status_text = tasks.status
		           AND v.kind = 'working')
		OR (
		    NOT EXISTS (SELECT 1 FROM task_status_vocabulary v
		                 WHERE v.workspace_id = tasks.workspace_id
		                   AND v.status_text = tasks.status)
		    AND LOWER(tasks.status) IN (` + sqlStringLiterals(workingFallbackStatuses) + `)
		)
	)`

// taskReclaimableExpr selects rows the lease machinery should reclaim:
//   - past-lease rows — a real lease that elapsed (status-agnostic; a
//     blocked row past lease still wants its dead assignee + lease
//     cleared, the service just won't demote a non-working status); OR
//   - no-lease working zombies — a row in a working status carrying an
//     assignee but NO lease at all. A correctly-claimed working row
//     ALWAYS holds a lease, so an assignee-without-lease working row is
//     by definition an unreclaimable zombie under the old predicate
//     (lease_expires_at IS NOT NULL filtered it out of BOTH release
//     paths). This second arm is the structural fix.
var taskReclaimableExpr = `(
		(tasks.lease_expires_at IS NOT NULL AND tasks.lease_expires_at < ?)
		OR (tasks.assignee_session_id IS NOT NULL
		    AND tasks.lease_expires_at IS NULL
		    AND ` + taskWorkingStatusPredicate + `)
	)`

// taskSessionReclaimExpr selects rows a DISCONNECTED session should
// release. It differs from taskReclaimableExpr in one crucial way: the
// session is gone, so the row releases regardless of how far in the
// future its lease runs — holding a lease is not a reason to keep a dead
// session's row. The two arms are:
//   - any row carrying a lease (the session held it, period); OR
//   - a no-lease working zombie (assignee set, no lease, working status)
//     — the structural-fix arm, identical to taskReclaimableExpr's.
//
// Callers MUST scope this with `assignee_session_id = ?` (done in
// ClearSessionTaskLeases) so it never reclaims another session's row.
// Takes NO bind parameters.
var taskSessionReclaimExpr = `(
		tasks.lease_expires_at IS NOT NULL
		OR (tasks.lease_expires_at IS NULL
		    AND ` + taskWorkingStatusPredicate + `)
	)`

// ClearExpiredTaskLeases finds every row that is reclaimable — past its
// lease window OR a no-lease working zombie — nulls its assignee + lease
// columns, and returns the ids so the service layer can append
// evt=lease_expired (and, for working statuses, demote to open) to each
// row's status_history. Excludes soft-deleted rows.
//
// The SELECT (gather ids) and UPDATE (re-evaluate the same reclaimable
// predicate) run inside ONE transaction so a concurrent HeartbeatTask
// cannot land between them and extend a lease the SELECT already saw as
// expired — that interleave would otherwise return an id that the UPDATE
// silently skipped, making the "authoritative" id set lie and causing
// the service layer to spuriously demote a still-leased, actively-
// heartbeating task. Wrapping both statements in a single tx (reusing an
// outer tx via withTx if one is active) closes that window: the snapshot
// the SELECT reads is the snapshot the UPDATE mutates.
func (d *DB) ClearExpiredTaskLeases(ctx context.Context, now time.Time) ([]string, error) {
	cutoff := now.UTC().Unix()
	var ids []string
	err := d.withTx(ctx, func(q queryable) error {
		rows, err := q.QueryContext(ctx, `
			SELECT id FROM tasks
			 WHERE `+taskReclaimableExpr+`
			   AND deleted_at IS NULL
			   AND COALESCE(assignee_user_id, '') = ''`, cutoff)
		if err != nil {
			return fmt.Errorf("scan expired leases: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan expired lease id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close expired leases: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		// updated_at is bumped too so SSE subscribers see the row tick
		// past their last-seen cursor and refresh the lease state in the UI.
		// Human-assigned tasks are NOT reclaimed by lease logic.
		if _, err := q.ExecContext(ctx, `
			UPDATE tasks
			   SET assignee_session_id = NULL,
			       assignee_origin_kind = 'local',
			       assignee_peer_id = '',
			       assignee_user_id = '',
			       lease_expires_at = NULL,
			       updated_at = ?
			 WHERE `+taskReclaimableExpr+`
			   AND deleted_at IS NULL
			   AND COALESCE(assignee_user_id, '') = ''`,
			cutoff, cutoff,
		); err != nil {
			return fmt.Errorf("clear expired leases: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ClearSessionTaskLeases reclaims every row held by `sessionID` on
// disconnect: ANY row carrying a lease (the session is gone — a still-
// live lease is no reason to keep its row) PLUS no-lease working zombies
// (assignee set, lease NULL, working status). The no-lease arm is the
// structural fix: a working row whose holder never set a lease (or whose
// lease was nulled out of band) was previously invisible to this path
// because of the `lease_expires_at IS NOT NULL` filter, leaving a
// permanent dead assignee + working status. Session-scoped (never
// touches another session's rows) and guards an empty session id
// (returns nil — never a workspace-wide wipe).
func (d *DB) ClearSessionTaskLeases(ctx context.Context, sessionID string) ([]string, error) {
	if sessionID == "" {
		return nil, nil
	}
	now := time.Now().UTC().Unix()
	var ids []string
	// Same SELECT+UPDATE-in-one-tx fix as ClearExpiredTaskLeases: the
	// window is narrower here (the session is already gone, so a stray
	// heartbeat from it is unexpected) but the shape is identical and a
	// lying authoritative id set is just as corrupting, so close it too.
	err := d.withTx(ctx, func(q queryable) error {
		rows, err := q.QueryContext(ctx, `
			SELECT id FROM tasks
			 WHERE tasks.assignee_session_id = ?
			   AND `+taskSessionReclaimExpr+`
			   AND deleted_at IS NULL`, sessionID)
		if err != nil {
			return fmt.Errorf("scan session leases: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan session lease id: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close session leases: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := q.ExecContext(ctx, `
			UPDATE tasks
			   SET assignee_session_id = NULL,
			       assignee_origin_kind = 'local',
			       assignee_peer_id = '',
			       lease_expires_at = NULL,
			       updated_at = ?
			 WHERE tasks.assignee_session_id = ?
			   AND `+taskSessionReclaimExpr+`
			   AND deleted_at IS NULL`,
			now, sessionID,
		); err != nil {
			return fmt.Errorf("clear session leases: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// rewriteFTS5Error inspects a driver error from the FTS5 MATCH path
// and rewrites well-known patterns to a typed store.FieldError so the
// caller sees `(code=fts5_reserved_syntax, field=q, value=<query>,
// hint=...)` instead of a nested wrapper string. Belt-and-braces:
// escapeFTS5Query() should keep FTS5 happy for normal input, this
// catches anything that slipped past (raw queries from other call
// sites, future syntax additions, etc.). Unknown errors fall through
// with the original `search tasks:` wrapper preserved as Cause.
func rewriteFTS5Error(err error, originalQuery string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// FTS5's column-not-found surfaces when a term like `foo-bar` is
	// parsed as `col foo AND NOT bar` and `foo` isn't a real column,
	// or when a bare operator like `AND` ends up at parse time.
	if strings.Contains(msg, "no such column") {
		fe := store.NewFieldError(
			"fts5_reserved_syntax",
			"q",
			originalQuery,
			"search query contains FTS5 reserved syntax",
			"wrap multi-word terms in double-quotes, or remove operators (AND OR NOT NEAR) and punctuation (- : / .) — escapeFTS5Query handles this automatically for the standard path",
		)
		fe.Cause = err
		return fe
	}
	// fts5: syntax error — bare operators in odd positions, dangling
	// quotes, etc.
	if strings.Contains(msg, "fts5: syntax error") || strings.Contains(msg, "syntax error near") {
		fe := store.NewFieldError(
			"fts5_syntax_error",
			"q",
			originalQuery,
			"search query is not valid FTS5 syntax",
			"check for unbalanced quotes or stray operators; plain text terms separated by spaces always work",
		)
		fe.Cause = err
		return fe
	}
	return fmt.Errorf("search tasks: %w", err)
}

// escapeFTS5Query wraps each whitespace-separated term in double
// quotes so SQLite FTS5 treats hyphens, slashes, colons, and the
// magic operators (AND / OR / NOT / NEAR) as literal characters of a
// search term instead of query syntax. Internal `"` chars are doubled
// (FTS5's standard escape).
func escapeFTS5Query(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	terms := strings.Fields(q)
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		out = append(out, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(out, " ")
}
