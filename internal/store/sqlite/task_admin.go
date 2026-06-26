// task_admin.go — Phase 5 admin store helpers on top of the tasks
// subsystem. Separated from task_companions.go (general CRUD over
// notes / vocabulary / offers / bindings / throttles) so the admin
// surface — exposed through the CWD-gated MCP tools — stays easy to
// find and audit.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SelectDistinctTaskStatuses returns status_text → count of live
// (non-deleted) tasks in a workspace. Backs task__consolidate_statuses
// (Phase 5 admin) — operators see real frequencies before approving a
// merge plan.
func (d *DB) SelectDistinctTaskStatuses(ctx context.Context, workspaceID string) (map[string]int, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT status, COUNT(*) AS n
		FROM tasks
		WHERE workspace_id = ? AND deleted_at IS NULL
		GROUP BY status
		ORDER BY n DESC, status ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("select distinct task statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// CountTaskStatuses returns status_text → count for non-deleted tasks,
// optionally scoped to one workspace and open/closed/all task state. It backs
// the dashboard status filter, where the dropdown should reflect statuses
// actually present in the active task population rather than the configured
// vocabulary.
func (d *DB) CountTaskStatuses(ctx context.Context, workspaceID, state string) (map[string]int, error) {
	conds := []string{"deleted_at IS NULL"}
	args := []any{}
	if workspaceID != "" {
		conds = append(conds, "workspace_id = ?")
		args = append(args, workspaceID)
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "open":
		conds = append(conds, "closed_at IS NULL")
	case "closed":
		conds = append(conds, "closed_at IS NOT NULL")
	case "all", "any":
		// non-deleted only
	default:
		return nil, fmt.Errorf("CountTaskStatuses: unknown state %q", state)
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT status, COUNT(*) AS n
		FROM tasks
		WHERE `+strings.Join(conds, " AND ")+`
		GROUP BY status
		ORDER BY n DESC, status ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("count task statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// rebindUpdate describes one UPDATE statement applied during
// RebindPeerInTasks. Kept here (not inline) so RebindPeerInTasks stays
// under the 50-line per-function ceiling.
type rebindUpdate struct {
	key string
	sql string
}

// rebindStatements is the canonical list of (table, column) updates
// for re-pair recovery. Maintenance rule: every column in the task
// subsystem that stores a peer id MUST appear here so a rebind sweeps
// the whole graph cleanly.
var rebindStatements = []rebindUpdate{
	{"tasks_assignee", `UPDATE tasks SET assignee_peer_id = ? WHERE assignee_peer_id = ?`},
	{"tasks_origin", `UPDATE tasks SET origin_peer_id = ? WHERE origin_peer_id = ?`},
	{"tasks_assigned_by", `UPDATE tasks SET assigned_by_peer_id = ? WHERE assigned_by_peer_id = ?`},
	{"task_offers_from", `UPDATE task_offers SET from_peer_id = ? WHERE from_peer_id = ?`},
	{"task_offers_to", `UPDATE task_offers SET to_peer_id = ? WHERE to_peer_id = ?`},
	{"workspace_peer_bindings", `UPDATE workspace_peer_bindings SET peer_id = ? WHERE peer_id = ?`},
}

// RebindPeerInTasks rewrites every reference to oldPeerID inside the
// task subsystem to newPeerID, atomically. Used by the
// task__rebind_peer admin tool after a re-pair / device-key rotation.
//
// Touched tables: tasks (assignee_peer_id, origin_peer_id,
// assigned_by_peer_id), task_offers (from_peer_id, to_peer_id),
// workspace_peer_bindings (peer_id). Returns the per-table row count
// keyed by a short name. All writes happen inside a single
// transaction so a partial rebind never leaks.
func (d *DB) RebindPeerInTasks(ctx context.Context, oldPeerID, newPeerID string) (map[string]int, error) {
	if oldPeerID == "" || newPeerID == "" {
		return nil, errors.New("RebindPeerInTasks: old + new peer ids required")
	}
	if oldPeerID == newPeerID {
		return nil, errors.New("RebindPeerInTasks: old and new peer ids are the same")
	}
	counts := map[string]int{}
	err := d.withTx(ctx, func(q queryable) error {
		for _, u := range rebindStatements {
			res, err := q.ExecContext(ctx, u.sql, newPeerID, oldPeerID)
			if err != nil {
				return fmt.Errorf("rebind %s: %w", u.key, err)
			}
			n, _ := res.RowsAffected()
			counts[u.key] = int(n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
