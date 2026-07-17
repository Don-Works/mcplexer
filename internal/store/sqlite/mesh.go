package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// GetMeshMessageCountsBucketed returns the count of mesh_messages.created_at
// per time bucket. Used by the dashboard to render the mesh-activity tile.
// Only buckets with at least one message are returned; the dashboard handler
// zero-fills missing buckets so the sparkline remains continuous.
func (d *DB) GetMeshMessageCountsBucketed(
	ctx context.Context, after, before time.Time, bucketSec int,
) (map[int64]int, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT
			(CAST(strftime('%s', created_at) AS INTEGER) / ?) * ? AS bucket_unix,
			COUNT(*) AS n
		FROM mesh_messages
		WHERE created_at >= ? AND created_at <= ?
		GROUP BY bucket_unix
		ORDER BY bucket_unix ASC`,
		bucketSec, bucketSec, formatTime(after), formatTime(before),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]int)
	for rows.Next() {
		var bucket int64
		var n int
		if err := rows.Scan(&bucket, &n); err != nil {
			return nil, fmt.Errorf("scan mesh message bucket: %w", err)
		}
		out[bucket] = n
	}
	return out, rows.Err()
}

func (d *DB) InsertMeshMessage(ctx context.Context, m *store.MeshMessage) error {
	actor := m.ActorKind
	if actor == "" {
		actor = "agent"
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO mesh_messages
			(id, workspace_id, session_id, agent_name, kind, priority, content,
			 audience, tags, reply_to, thread_root, reply_count, status, expires_at, created_at,
			 repo, branch, workspace_path, repo_remote, actor_kind)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.WorkspaceID, m.SessionID, m.AgentName, m.Kind, m.Priority,
		m.Content, m.Audience, m.Tags, m.ReplyTo, m.ThreadRoot, m.ReplyCount,
		m.Status, formatTime(m.ExpiresAt), formatTime(m.CreatedAt),
		m.Repo, m.Branch, m.WorkspacePath, m.RepoRemote, actor,
	)
	return mapConstraintError(err)
}

func (d *DB) GetMeshMessage(ctx context.Context, id string) (*store.MeshMessage, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, workspace_id, session_id, agent_name, kind, priority, content,
			   audience, tags, reply_to, thread_root, reply_count, status,
			   expires_at, created_at, repo, branch, workspace_path, repo_remote,
			   actor_kind
		FROM mesh_messages WHERE id = ?`, id)
	return scanMeshMessage(row)
}

func (d *DB) QueryMeshMessages(ctx context.Context, f store.MeshMessageFilter) ([]store.MeshMessage, error) {
	var where []string
	var args []any

	if len(f.WorkspaceIDs) > 0 {
		placeholders := make([]string, len(f.WorkspaceIDs))
		for i, id := range f.WorkspaceIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, fmt.Sprintf("workspace_id IN (%s)", strings.Join(placeholders, ",")))
	}

	if f.SinceID != "" {
		where = append(where, "id > ?")
		args = append(args, f.SinceID)
	}

	if f.SinceTime != nil {
		where = append(where, "created_at > ?")
		args = append(args, formatTime(*f.SinceTime))
	}

	if f.StatusLive {
		where = append(where, "status = 'live'")
	}

	if f.ExcludeSessionID != "" {
		where = append(where, "session_id != ?")
		args = append(args, f.ExcludeSessionID)
	}

	if f.ThreadRoot != "" {
		where = append(where, "thread_root = ?")
		args = append(args, f.ThreadRoot)
	}

	// Kind / actor-kind whitelists + blacklists (mesh signal/noise).
	where, args = appendInFilter(where, args, "kind", f.Kinds, false)
	where, args = appendInFilter(where, args, "kind", f.ExcludeKinds, true)
	where, args = appendInFilter(where, args, "actor_kind", f.ActorKinds, false)
	where, args = appendInFilter(where, args, "actor_kind", f.ExcludeActorKinds, true)

	if f.Tags != "" {
		// Match any of the comma-separated tags.
		for _, tag := range strings.Split(f.Tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				where = append(where, "tags LIKE ?")
				args = append(args, "%"+tag+"%")
			}
		}
	}

	// M7.3 — repo/branch/workspace_path scoping.
	if len(f.Repos) > 0 {
		placeholders := make([]string, len(f.Repos))
		for i, r := range f.Repos {
			placeholders[i] = "?"
			args = append(args, r)
		}
		where = append(where, fmt.Sprintf("repo IN (%s)", strings.Join(placeholders, ",")))
	} else if f.Repo != "" {
		where = append(where, "repo = ?")
		args = append(args, f.Repo)
	}
	if f.Branch != "" {
		where = append(where, "branch = ?")
		args = append(args, f.Branch)
	}
	if f.WorkspacePath != "" {
		where = append(where, "workspace_path = ?")
		args = append(args, f.WorkspacePath)
	}

	// Audience matching: broadcast, agent's role, or agent's session.
	if f.Audience != "" || f.AgentRole != "" {
		var audienceClauses []string
		audienceClauses = append(audienceClauses, "audience = '*'")
		if f.Audience != "" {
			audienceClauses = append(audienceClauses, "audience = ?")
			args = append(args, f.Audience)
		}
		if f.AgentRole != "" {
			audienceClauses = append(audienceClauses, "audience = ?")
			args = append(args, f.AgentRole)
		}
		where = append(where, "("+strings.Join(audienceClauses, " OR ")+")")
	}

	query := "SELECT id, workspace_id, session_id, agent_name, kind, priority, content, audience, tags, reply_to, thread_root, reply_count, status, expires_at, created_at, repo, branch, workspace_path, repo_remote, actor_kind FROM mesh_messages"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if f.OrderRecent {
		// Recency-only window: id is a ULID (lexicographically time-ordered),
		// so id DESC is strict newest-first regardless of priority. Used by
		// "recent conversation" callers (e.g. telegram mesh_history) where a
		// low/normal-priority agent-outbound row must not be pushed below
		// high-priority inbound traffic and lost from the window.
		query += " ORDER BY id DESC"
	} else if f.OrderOldest {
		// Oldest-first contiguous window: a LIMIT then drops only the NEWEST
		// rows, so the filter=new cursor scan can advance its cursor to the
		// delivered batch's max id without ever skipping an older row that a
		// priority-first LIMIT would have cut.
		query += " ORDER BY id ASC"
	} else {
		query += " ORDER BY priority_order(priority), id DESC"

		// SQLite doesn't have a priority_order function, so use CASE instead.
		query = strings.Replace(query, "priority_order(priority)", `CASE priority
		WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END`, 1)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query mesh messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []store.MeshMessage
	for rows.Next() {
		var m store.MeshMessage
		var expiresAt, createdAt string
		if err := rows.Scan(
			&m.ID, &m.WorkspaceID, &m.SessionID, &m.AgentName, &m.Kind,
			&m.Priority, &m.Content, &m.Audience, &m.Tags, &m.ReplyTo,
			&m.ThreadRoot, &m.ReplyCount, &m.Status, &expiresAt, &createdAt,
			&m.Repo, &m.Branch, &m.WorkspacePath, &m.RepoRemote, &m.ActorKind,
		); err != nil {
			return nil, fmt.Errorf("scan mesh message: %w", err)
		}
		m.ExpiresAt = parseTime(expiresAt)
		m.CreatedAt = parseTime(createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// appendInFilter appends `col IN (...)` (or NOT IN when negate) to the
// WHERE clause builder. Empty vals is a no-op so unset filters cost nothing.
func appendInFilter(where []string, args []any, col string, vals []string, negate bool) ([]string, []any) {
	if len(vals) == 0 {
		return where, args
	}
	placeholders := make([]string, len(vals))
	for i, v := range vals {
		placeholders[i] = "?"
		args = append(args, v)
	}
	op := "IN"
	if negate {
		op = "NOT IN"
	}
	where = append(where, fmt.Sprintf("%s %s (%s)", col, op, strings.Join(placeholders, ",")))
	return where, args
}

func (d *DB) IncrementReplyCount(ctx context.Context, messageID string) error {
	res, err := d.q.ExecContext(ctx,
		"UPDATE mesh_messages SET reply_count = reply_count + 1 WHERE id = ?",
		messageID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) ExtendMessageExpiry(ctx context.Context, messageID string, expiresAt time.Time) error {
	res, err := d.q.ExecContext(ctx,
		"UPDATE mesh_messages SET expires_at = ? WHERE id = ? AND expires_at < ?",
		formatTime(expiresAt), messageID, formatTime(expiresAt),
	)
	if err != nil {
		return err
	}
	// Don't check rows affected — it's ok if the current expiry is already later.
	_ = res
	return nil
}

func (d *DB) ArchiveExpiredMessages(ctx context.Context, now time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		"UPDATE mesh_messages SET status = 'archived' WHERE status = 'live' AND expires_at < ?",
		formatTime(now),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) DeleteArchivedMessages(ctx context.Context, before time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		"DELETE FROM mesh_messages WHERE status = 'archived' AND expires_at < ?",
		formatTime(before),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) CountLiveMessages(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := d.q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM mesh_messages WHERE workspace_id = ? AND status = 'live'",
		workspaceID,
	).Scan(&count)
	return count, err
}

func (d *DB) ArchiveLowestPriority(ctx context.Context, workspaceID string, count int) (int, error) {
	res, err := d.q.ExecContext(ctx, `
		UPDATE mesh_messages SET status = 'archived'
		WHERE id IN (
			SELECT id FROM mesh_messages
			WHERE workspace_id = ? AND status = 'live'
			ORDER BY CASE priority
				WHEN 'low' THEN 0 WHEN 'normal' THEN 1
				WHEN 'high' THEN 2 WHEN 'critical' THEN 3 ELSE 0 END,
				created_at ASC
			LIMIT ?
		)`, workspaceID, count,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) ArchiveMessagesBySenderAndKinds(ctx context.Context, senderSessionIDs []string, kinds []string) (int, error) {
	if len(senderSessionIDs) == 0 || len(kinds) == 0 {
		return 0, nil
	}
	senderPh := make([]string, len(senderSessionIDs))
	senderArgs := make([]any, len(senderSessionIDs))
	for i, s := range senderSessionIDs {
		senderPh[i] = "?"
		senderArgs[i] = s
	}
	kindPh := make([]string, len(kinds))
	kindArgs := make([]any, len(kinds))
	for i, k := range kinds {
		kindPh[i] = "?"
		kindArgs[i] = k
	}
	query := fmt.Sprintf(
		"UPDATE mesh_messages SET status = 'archived' WHERE status = 'live' AND session_id IN (%s) AND kind IN (%s)",
		strings.Join(senderPh, ","),
		strings.Join(kindPh, ","),
	)
	args := append(senderArgs, kindArgs...)
	res, err := d.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) ArchiveOldWorkerFindings(ctx context.Context, olderThan time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		"UPDATE mesh_messages SET status = 'archived' WHERE status = 'live' AND actor_kind = 'worker' AND kind IN ('finding', 'reply') AND created_at < ?",
		formatTime(olderThan),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) UpsertMeshAgent(ctx context.Context, a *store.MeshAgent) error {
	// Insert-side default lands when the caller didn't pick a tag (e.g. a
	// legacy code path); update-side preserves the existing column on
	// empty input so a remote (peer:*) row is never silently demoted to
	// local by an unrelated metadata touch.
	insertOrigin := a.Origin
	if insertOrigin == "" {
		insertOrigin = store.MeshAgentOriginLocal
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO mesh_agents
			(session_id, workspace_id, name, role, client_type, model_hint, cursor, origin, status, tmux_session, tmux_window, tmux_pane, last_seen_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			-- G8 — workspace_id is immutable after first set. An inbound
			-- upsert from a different origin (peer crossing a local
			-- session_id, or vice versa) cannot silently relocate the
			-- agent into another workspace. Once the row owns a
			-- workspace_id, that's the row's workspace forever.
			workspace_id = CASE WHEN mesh_agents.workspace_id != '' THEN mesh_agents.workspace_id ELSE excluded.workspace_id END,
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE mesh_agents.name END,
			role = CASE WHEN excluded.role != '' THEN excluded.role ELSE mesh_agents.role END,
			client_type = CASE WHEN excluded.client_type != '' THEN excluded.client_type ELSE mesh_agents.client_type END,
			model_hint = CASE WHEN excluded.model_hint != '' THEN excluded.model_hint ELSE mesh_agents.model_hint END,
			origin = CASE WHEN ? != '' THEN ? ELSE mesh_agents.origin END,
			status = CASE WHEN excluded.status != '' THEN excluded.status ELSE mesh_agents.status END,
			tmux_session = CASE WHEN excluded.tmux_session != '' THEN excluded.tmux_session ELSE mesh_agents.tmux_session END,
			tmux_window = CASE WHEN excluded.tmux_window != '' THEN excluded.tmux_window ELSE mesh_agents.tmux_window END,
			tmux_pane = CASE WHEN excluded.tmux_pane != '' THEN excluded.tmux_pane ELSE mesh_agents.tmux_pane END,
			last_seen_at = excluded.last_seen_at`,
		a.SessionID, a.WorkspaceID, a.Name, a.Role, a.ClientType,
		a.ModelHint, a.Cursor, insertOrigin, a.Status,
		a.TmuxSession, a.TmuxWindow, a.TmuxPane,
		formatTime(a.LastSeenAt), formatTime(a.CreatedAt),
		a.Origin, a.Origin,
	)
	return err
}

// SetMeshAgentTerminalLocator updates only the tmux_* fields for an
// existing session. Bumps last_seen_at as a liveness signal — the act
// of setting your locator is itself a heartbeat. Silently no-ops if
// the row doesn't exist (caller is expected to have upserted first).
func (d *DB) SetMeshAgentTerminalLocator(ctx context.Context, sessionID, tmuxSession, tmuxWindow, tmuxPane string, now time.Time) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE mesh_agents SET tmux_session = ?, tmux_window = ?, tmux_pane = ?, last_seen_at = ? WHERE session_id = ?`,
		tmuxSession, tmuxWindow, tmuxPane, formatTime(now.UTC()), sessionID)
	return err
}

// FindRecentLocalAgentByClient returns the most-recent prior local row
// for (workspace_id, client_type), excluding excludeSessionID. Used by
// the mesh manager to inherit name/role/status/locator across process
// restarts: session_id is per-process, so a fresh row would otherwise
// drop the agent's identity. Returns (nil, nil) when no match.
func (d *DB) FindRecentLocalAgentByClient(ctx context.Context, workspaceID, clientType, excludeSessionID string) (*store.MeshAgent, error) {
	// Filter to rows that actually carry a name — a row with no name
	// can't usefully be inherited (would re-bake the very state we're
	// trying to fix).
	row := d.q.QueryRowContext(ctx, `
		SELECT session_id, workspace_id, name, role, client_type, model_hint,
			   cursor, origin, status, tmux_session, tmux_window, tmux_pane,
			   last_seen_at, created_at
		FROM mesh_agents
		WHERE workspace_id = ?
		  AND client_type = ?
		  AND origin = ?
		  AND session_id != ?
		  AND name != ''
		ORDER BY last_seen_at DESC
		LIMIT 1`,
		workspaceID, clientType, store.MeshAgentOriginLocal, excludeSessionID)
	var a store.MeshAgent
	var lastSeenAt, createdAt string
	if err := row.Scan(
		&a.SessionID, &a.WorkspaceID, &a.Name, &a.Role, &a.ClientType,
		&a.ModelHint, &a.Cursor, &a.Origin, &a.Status,
		&a.TmuxSession, &a.TmuxWindow, &a.TmuxPane,
		&lastSeenAt, &createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	a.LastSeenAt = parseTime(lastSeenAt)
	a.CreatedAt = parseTime(createdAt)
	return &a, nil
}

// SetMeshAgentStatus updates ONLY the status field for an existing
// session. Bumps last_seen_at as a side-effect since setting status is
// itself a liveness signal. Silently no-ops if the row doesn't exist —
// the caller is expected to have called UpsertMeshAgent on first
// connect.
func (d *DB) SetMeshAgentStatus(ctx context.Context, sessionID, status string, now time.Time) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE mesh_agents SET status = ?, last_seen_at = ? WHERE session_id = ?`,
		status, formatTime(now.UTC()), sessionID)
	return err
}

func (d *DB) GetMeshAgent(ctx context.Context, sessionID string) (*store.MeshAgent, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT session_id, workspace_id, name, role, client_type, model_hint,
			   cursor, origin, status, tmux_session, tmux_window, tmux_pane,
			   last_seen_at, created_at
		FROM mesh_agents WHERE session_id = ?`, sessionID)

	var a store.MeshAgent
	var lastSeenAt, createdAt string
	if err := row.Scan(
		&a.SessionID, &a.WorkspaceID, &a.Name, &a.Role, &a.ClientType,
		&a.ModelHint, &a.Cursor, &a.Origin, &a.Status,
		&a.TmuxSession, &a.TmuxWindow, &a.TmuxPane,
		&lastSeenAt, &createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	a.LastSeenAt = parseTime(lastSeenAt)
	a.CreatedAt = parseTime(createdAt)
	return &a, nil
}

func (d *DB) ListActiveMeshAgents(ctx context.Context, workspaceID string, since time.Time) ([]store.MeshAgent, error) {
	var query string
	var args []any
	if workspaceID == "" {
		query = `SELECT session_id, workspace_id, name, role, client_type, model_hint,
				   cursor, origin, status, tmux_session, tmux_window, tmux_pane,
				   last_seen_at, created_at
			FROM mesh_agents WHERE last_seen_at > ? ORDER BY last_seen_at DESC`
		args = []any{formatTime(since)}
	} else {
		query = `SELECT session_id, workspace_id, name, role, client_type, model_hint,
				   cursor, origin, status, tmux_session, tmux_window, tmux_pane,
				   last_seen_at, created_at
			FROM mesh_agents WHERE workspace_id = ? AND last_seen_at > ? ORDER BY last_seen_at DESC`
		args = []any{workspaceID, formatTime(since)}
	}
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var agents []store.MeshAgent
	for rows.Next() {
		var a store.MeshAgent
		var lastSeenAt, createdAt string
		if err := rows.Scan(
			&a.SessionID, &a.WorkspaceID, &a.Name, &a.Role, &a.ClientType,
			&a.ModelHint, &a.Cursor, &a.Origin, &a.Status,
			&a.TmuxSession, &a.TmuxWindow, &a.TmuxPane,
			&lastSeenAt, &createdAt,
		); err != nil {
			return nil, err
		}
		a.LastSeenAt = parseTime(lastSeenAt)
		a.CreatedAt = parseTime(createdAt)
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// ListActiveMeshAgentsInWorkspaces returns active local-origin agents whose
// workspace_id is in wsIDs. This is the outbound filter for workspace-scoped
// peer gossip: a peer paired to workspaces {X,Y} only ever sees agents in X
// or Y. An empty wsIDs slice returns no rows (default-deny) — a peer with no
// workspace binding gets an empty snapshot. Peer-origin rows are excluded so
// a peer's agents are never echoed back across the wire.
func (d *DB) ListActiveMeshAgentsInWorkspaces(ctx context.Context, wsIDs []string, since time.Time) ([]store.MeshAgent, error) {
	if len(wsIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(wsIDs))
	args := make([]any, 0, len(wsIDs)+1)
	for i, id := range wsIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, formatTime(since))
	query := `SELECT session_id, workspace_id, name, role, client_type, model_hint,
			   cursor, origin, status, tmux_session, tmux_window, tmux_pane,
			   last_seen_at, created_at
		FROM mesh_agents
		WHERE workspace_id IN (` + strings.Join(placeholders, ",") + `)
		  AND origin = 'local' AND last_seen_at > ?
		ORDER BY last_seen_at DESC`
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var agents []store.MeshAgent
	for rows.Next() {
		var a store.MeshAgent
		var lastSeenAt, createdAt string
		if err := rows.Scan(
			&a.SessionID, &a.WorkspaceID, &a.Name, &a.Role, &a.ClientType,
			&a.ModelHint, &a.Cursor, &a.Origin, &a.Status,
			&a.TmuxSession, &a.TmuxWindow, &a.TmuxPane,
			&lastSeenAt, &createdAt,
		); err != nil {
			return nil, err
		}
		a.LastSeenAt = parseTime(lastSeenAt)
		a.CreatedAt = parseTime(createdAt)
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (d *DB) DeleteMeshAgent(ctx context.Context, sessionID string) error {
	_, err := d.q.ExecContext(ctx,
		"DELETE FROM mesh_agents WHERE session_id = ?",
		sessionID,
	)
	return err
}

func (d *DB) DeleteAllMeshAgents(ctx context.Context) (int, error) {
	res, err := d.q.ExecContext(ctx, "DELETE FROM mesh_agents")
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteMeshAgentsByOrigin removes every mesh_agents row whose origin
// column matches origin exactly. The agent-directory gossip receiver uses
// this for snapshot-replace ("origin = peer:<sender>") and for the bye
// frame ("drop everything from this peer"). Returns the row count for
// telemetry; an empty match is not an error.
func (d *DB) DeleteMeshAgentsByOrigin(ctx context.Context, origin string) (int, error) {
	if origin == "" {
		return 0, nil
	}
	res, err := d.q.ExecContext(ctx,
		"DELETE FROM mesh_agents WHERE origin = ?",
		origin,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (d *DB) UpdateAgentCursor(ctx context.Context, sessionID, cursor string) error {
	_, err := d.q.ExecContext(ctx,
		"UPDATE mesh_agents SET cursor = ? WHERE session_id = ?",
		cursor, sessionID,
	)
	return err
}

func (d *DB) TouchMeshAgent(ctx context.Context, sessionID string) error {
	_, err := d.q.ExecContext(ctx,
		"UPDATE mesh_agents SET last_seen_at = ? WHERE session_id = ?",
		formatTime(time.Now().UTC()), sessionID,
	)
	return err
}

type meshRow interface {
	Scan(dest ...any) error
}

func scanMeshMessage(row meshRow) (*store.MeshMessage, error) {
	var m store.MeshMessage
	var expiresAt, createdAt string
	if err := row.Scan(
		&m.ID, &m.WorkspaceID, &m.SessionID, &m.AgentName, &m.Kind,
		&m.Priority, &m.Content, &m.Audience, &m.Tags, &m.ReplyTo,
		&m.ThreadRoot, &m.ReplyCount, &m.Status, &expiresAt, &createdAt,
		&m.Repo, &m.Branch, &m.WorkspacePath, &m.RepoRemote, &m.ActorKind,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	m.ExpiresAt = parseTime(expiresAt)
	m.CreatedAt = parseTime(createdAt)
	return &m, nil
}
