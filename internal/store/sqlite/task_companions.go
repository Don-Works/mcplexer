// task_companions.go — store CRUD for the tasks subsystem's companion
// tables: task_notes (append-only notes), task_status_vocabulary
// (per-workspace status learning), workspace_peer_bindings (cross-peer
// workspace identity memoization), task_assign_throttles (per-peer
// direct-assign throttle window).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// ----------------------------------------------------------------------
// task_notes
// ----------------------------------------------------------------------

func (d *DB) AppendTaskNote(ctx context.Context, n *store.TaskNote) error {
	if n == nil {
		return errors.New("AppendTaskNote: nil note")
	}
	if n.TaskID == "" {
		return errors.New("AppendTaskNote: task_id required")
	}
	if n.Body == "" {
		return errors.New("AppendTaskNote: body required")
	}
	if n.ID == "" {
		n.ID = ulid.Make().String()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	if n.AuthorKind == "" {
		n.AuthorKind = store.TaskSourceAgent
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_notes (id, task_id, author_session_id, author_kind, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		n.ID, n.TaskID, nullString(n.AuthorSessionID), n.AuthorKind, n.Body, n.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("append task note: %w", err)
	}
	return nil
}

func (d *DB) ListTaskNotes(ctx context.Context, taskID string, limit int) ([]store.TaskNote, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, task_id, author_session_id, author_kind, body, created_at
		FROM task_notes WHERE task_id = ?
		ORDER BY created_at DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("list task notes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.TaskNote
	for rows.Next() {
		var n store.TaskNote
		var author sql.NullString
		var created int64
		if err := rows.Scan(&n.ID, &n.TaskID, &author, &n.AuthorKind, &n.Body, &created); err != nil {
			return nil, err
		}
		if author.Valid {
			n.AuthorSessionID = author.String
		}
		n.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// ----------------------------------------------------------------------
// task_status_vocabulary
// ----------------------------------------------------------------------

func (d *DB) UpsertTaskStatusVocab(ctx context.Context, v *store.TaskStatusVocab) error {
	if v == nil {
		return errors.New("UpsertTaskStatusVocab: nil vocab")
	}
	if v.WorkspaceID == "" || v.StatusText == "" {
		return errors.New("UpsertTaskStatusVocab: workspace_id + status_text required")
	}
	if v.ManagedBy == "" {
		v.ManagedBy = "user"
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	// Kind classifies the freeform status into one of the canonical
	// buckets the UI + service layer can drive without hardcoding the
	// six suggested defaults. Empty kind → 'open' (the conservative
	// answer matching the column's DDL default).
	if v.Kind == "" {
		v.Kind = "open"
	}
	terminal := 0
	if v.IsTerminal {
		terminal = 1
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_status_vocabulary
			(workspace_id, status_text, is_terminal, kind, display_color, display_order, managed_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, status_text) DO UPDATE SET
			is_terminal = excluded.is_terminal,
			kind = excluded.kind,
			display_color = excluded.display_color,
			display_order = excluded.display_order,
			managed_by = excluded.managed_by,
			updated_at = excluded.updated_at`,
		v.WorkspaceID, v.StatusText, terminal, v.Kind, v.DisplayColor, v.DisplayOrder, v.ManagedBy, v.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert task status vocab: %w", err)
	}
	return nil
}

func (d *DB) ListTaskStatusVocab(ctx context.Context, workspaceID string) ([]store.TaskStatusVocab, error) {
	// COALESCE on kind guards against pre-migration-070 rows in the
	// extremely unlikely case a row pre-dates schema_version=70 and the
	// invariant self-heal hasn't run yet.
	rows, err := d.q.QueryContext(ctx, `
		SELECT workspace_id, status_text, is_terminal,
			COALESCE(kind, 'open') AS kind,
			display_color, display_order, managed_by, updated_at
		FROM task_status_vocabulary
		WHERE workspace_id = ?
		ORDER BY display_order, status_text`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list task status vocab: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.TaskStatusVocab
	for rows.Next() {
		v, err := scanTaskVocab(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func scanTaskVocab(scan func(...any) error) (*store.TaskStatusVocab, error) {
	var v store.TaskStatusVocab
	var color sql.NullString
	var kind sql.NullString
	var terminal int
	var updated int64
	if err := scan(&v.WorkspaceID, &v.StatusText, &terminal, &kind, &color, &v.DisplayOrder, &v.ManagedBy, &updated); err != nil {
		return nil, err
	}
	v.IsTerminal = terminal != 0
	if kind.Valid {
		v.Kind = kind.String
	} else {
		v.Kind = "open"
	}
	if color.Valid {
		v.DisplayColor = color.String
	}
	v.UpdatedAt = time.Unix(updated, 0).UTC()
	return &v, nil
}

func (d *DB) DeleteTaskStatusVocab(ctx context.Context, workspaceID, statusText string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM task_status_vocabulary WHERE workspace_id = ? AND status_text = ?`,
		workspaceID, statusText)
	if err != nil {
		return fmt.Errorf("delete task status vocab: %w", err)
	}
	return checkRowsAffected(res)
}

func (d *DB) IsTerminalStatus(ctx context.Context, workspaceID, status string) (bool, error) {
	var n int
	err := d.q.QueryRowContext(ctx,
		`SELECT is_terminal FROM task_status_vocabulary WHERE workspace_id = ? AND status_text = ?`,
		workspaceID, status,
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		// No vocab entry yet — non-terminal by default. The cleanup skill
		// or the user will mark terminal statuses as they emerge.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is terminal status: %w", err)
	}
	return n != 0, nil
}

// ----------------------------------------------------------------------
// workspace_peer_bindings
// ----------------------------------------------------------------------

func (d *DB) UpsertWorkspacePeerBinding(ctx context.Context, b *store.WorkspacePeerBinding) error {
	if b == nil {
		return errors.New("UpsertWorkspacePeerBinding: nil binding")
	}
	if b.PeerID == "" || b.RemoteWorkspaceID == "" || b.LocalWorkspaceID == "" {
		return errors.New("UpsertWorkspacePeerBinding: peer_id, remote_workspace_id, local_workspace_id required")
	}
	if b.EstablishedAt.IsZero() {
		b.EstablishedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO workspace_peer_bindings
			(peer_id, remote_workspace_id, local_workspace_id, remote_workspace_name, established_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(peer_id, remote_workspace_id) DO UPDATE SET
			local_workspace_id = excluded.local_workspace_id,
			remote_workspace_name = excluded.remote_workspace_name`,
		b.PeerID, b.RemoteWorkspaceID, b.LocalWorkspaceID, b.RemoteWorkspaceName, b.EstablishedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert workspace peer binding: %w", err)
	}
	return nil
}

// bindingSelectCols is the canonical column list for scanWorkspaceBinding.
// Keep it in lock-step with the scan helper below.
const bindingSelectCols = `peer_id, remote_workspace_id, local_workspace_id,
	remote_workspace_name, established_at, linked, link_established_by, link_established_at`

// scanWorkspaceBinding reads one row selected via bindingSelectCols.
func scanWorkspaceBinding(scan func(...any) error) (*store.WorkspacePeerBinding, error) {
	var b store.WorkspacePeerBinding
	var established int64
	var linked int
	var linkAt sql.NullInt64
	if err := scan(&b.PeerID, &b.RemoteWorkspaceID, &b.LocalWorkspaceID,
		&b.RemoteWorkspaceName, &established, &linked, &b.LinkEstablishedBy, &linkAt); err != nil {
		return nil, err
	}
	b.EstablishedAt = time.Unix(established, 0).UTC()
	b.Linked = linked != 0
	if linkAt.Valid {
		t := time.Unix(linkAt.Int64, 0).UTC()
		b.LinkEstablishedAt = &t
	}
	return &b, nil
}

func (d *DB) GetWorkspacePeerBinding(ctx context.Context, peerID, remoteWorkspaceID string) (*store.WorkspacePeerBinding, error) {
	b, err := scanWorkspaceBinding(d.q.QueryRowContext(ctx, `
		SELECT `+bindingSelectCols+`
		FROM workspace_peer_bindings WHERE peer_id = ? AND remote_workspace_id = ?`,
		peerID, remoteWorkspaceID,
	).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace peer binding: %w", err)
	}
	return b, nil
}

// ListLocalWorkspaceIDsForPeer returns the distinct local workspace IDs a
// peer is bound to — the authoritative authorization set for
// workspace-scoped pairing. Empty slice => default-deny.
func (d *DB) ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT DISTINCT local_workspace_id
		FROM workspace_peer_bindings WHERE peer_id = ?`,
		peerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list local workspaces for peer: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ListWorkspacePeerBindingsForPeer returns every binding row for a peer.
func (d *DB) ListWorkspacePeerBindingsForPeer(ctx context.Context, peerID string) ([]store.WorkspacePeerBinding, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+bindingSelectCols+`
		FROM workspace_peer_bindings WHERE peer_id = ?`,
		peerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list bindings for peer: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.WorkspacePeerBinding
	for rows.Next() {
		b, err := scanWorkspaceBinding(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// SetWorkspaceLink promotes (or creates) a binding into an explicit linked
// workspace. Upserts so a link can be declared before any offer has run.
// On conflict it sets the link columns AND refreshes local_workspace_id /
// remote_workspace_name (an explicit link declaration is authoritative
// about which local workspace the pair maps to).
func (d *DB) SetWorkspaceLink(ctx context.Context, b *store.WorkspacePeerBinding, establishedBy string) error {
	if b == nil {
		return errors.New("SetWorkspaceLink: nil binding")
	}
	if b.PeerID == "" || b.RemoteWorkspaceID == "" || b.LocalWorkspaceID == "" {
		return errors.New("SetWorkspaceLink: peer_id, remote_workspace_id, local_workspace_id required")
	}
	if establishedBy != "local" && establishedBy != "peer" {
		return fmt.Errorf("SetWorkspaceLink: establishedBy must be 'local' or 'peer', got %q", establishedBy)
	}
	now := time.Now().UTC()
	if b.EstablishedAt.IsZero() {
		b.EstablishedAt = now
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO workspace_peer_bindings
			(peer_id, remote_workspace_id, local_workspace_id, remote_workspace_name,
			 established_at, linked, link_established_by, link_established_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(peer_id, remote_workspace_id) DO UPDATE SET
			local_workspace_id = excluded.local_workspace_id,
			remote_workspace_name = excluded.remote_workspace_name,
			linked = 1,
			link_established_by = excluded.link_established_by,
			link_established_at = excluded.link_established_at`,
		b.PeerID, b.RemoteWorkspaceID, b.LocalWorkspaceID, b.RemoteWorkspaceName,
		b.EstablishedAt.Unix(), establishedBy, now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("set workspace link: %w", err)
	}
	b.Linked = true
	b.LinkEstablishedBy = establishedBy
	b.LinkEstablishedAt = &now
	return nil
}

// ClearWorkspaceLink demotes a binding back to a plain offer-routing memo.
// The routing row is preserved so in-flight offers still resolve. No-op
// when the row doesn't exist.
func (d *DB) ClearWorkspaceLink(ctx context.Context, peerID, remoteWorkspaceID string) error {
	_, err := d.q.ExecContext(ctx, `
		UPDATE workspace_peer_bindings
		SET linked = 0, link_established_by = '', link_established_at = NULL
		WHERE peer_id = ? AND remote_workspace_id = ?`,
		peerID, remoteWorkspaceID,
	)
	if err != nil {
		return fmt.Errorf("clear workspace link: %w", err)
	}
	return nil
}

// ListWorkspaceLinks returns every linked binding across all peers.
func (d *DB) ListWorkspaceLinks(ctx context.Context) ([]store.WorkspacePeerBinding, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+bindingSelectCols+`
		FROM workspace_peer_bindings WHERE linked = 1
		ORDER BY local_workspace_id, peer_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.WorkspacePeerBinding
	for rows.Next() {
		b, err := scanWorkspaceBinding(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListLinkedPeersForWorkspace returns the distinct peer IDs a local
// workspace is linked to — the send-side gate for task replication.
func (d *DB) ListLinkedPeersForWorkspace(ctx context.Context, localWorkspaceID string) ([]string, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT DISTINCT peer_id FROM workspace_peer_bindings
		WHERE local_workspace_id = ? AND linked = 1`,
		localWorkspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list linked peers for workspace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ----------------------------------------------------------------------
// task_assign_throttles
// ----------------------------------------------------------------------

func (d *DB) UpsertTaskAssignThrottle(ctx context.Context, t *store.TaskAssignThrottle) error {
	if t == nil {
		return errors.New("UpsertTaskAssignThrottle: nil throttle")
	}
	if t.PeerID == "" || t.WorkspaceID == "" {
		return errors.New("UpsertTaskAssignThrottle: peer_id + workspace_id required")
	}
	if t.LastAssignAt.IsZero() {
		t.LastAssignAt = time.Now().UTC()
	}
	if t.WindowStartedAt.IsZero() {
		t.WindowStartedAt = t.LastAssignAt
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_assign_throttles
			(peer_id, workspace_id, last_assign_at, count_in_window, window_started_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(peer_id, workspace_id) DO UPDATE SET
			last_assign_at = excluded.last_assign_at,
			count_in_window = excluded.count_in_window,
			window_started_at = excluded.window_started_at`,
		t.PeerID, t.WorkspaceID, t.LastAssignAt.Unix(), t.CountInWindow, t.WindowStartedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert task assign throttle: %w", err)
	}
	return nil
}

func (d *DB) GetTaskAssignThrottle(ctx context.Context, peerID, workspaceID string) (*store.TaskAssignThrottle, error) {
	var t store.TaskAssignThrottle
	var last, window int64
	err := d.q.QueryRowContext(ctx, `
		SELECT peer_id, workspace_id, last_assign_at, count_in_window, window_started_at
		FROM task_assign_throttles WHERE peer_id = ? AND workspace_id = ?`,
		peerID, workspaceID,
	).Scan(&t.PeerID, &t.WorkspaceID, &last, &t.CountInWindow, &window)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task assign throttle: %w", err)
	}
	t.LastAssignAt = time.Unix(last, 0).UTC()
	t.WindowStartedAt = time.Unix(window, 0).UTC()
	return &t, nil
}

// Phase 5 admin helpers (SelectDistinctTaskStatuses, RebindPeerInTasks)
// live in task_admin.go to keep this file focused on CRUD.
