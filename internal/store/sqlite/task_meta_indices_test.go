// task_meta_indices_test.go — exercises migration 078's partial
// expression indices on tasks.meta hot paths.
//
// Two layers of coverage:
//
//  1. TestMigration078IndicesPresent — confirms every CREATE INDEX in
//     the migration is actually present in sqlite_master after migrate.
//
//  2. TestMigration078IndicesUsedByPlanner — for each indexed key,
//     runs EXPLAIN QUERY PLAN against the production metaMatchSQL
//     shape and asserts the partial index name appears in the plan.
//     Without ANALYZE the planner won't pick the partial index — the
//     migration runs ANALYZE at the end so this test is deterministic.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// hotMetaKeys is the set of meta keys that migration 078 indices.
// Keep in sync with migrations/078_tasks_meta_expression_indices.sql.
var hotMetaKeys = []struct {
	key   string
	index string
}{
	{"branch", "idx_tasks_meta_branch"},
	{"worktree", "idx_tasks_meta_worktree"},
	{"pr", "idx_tasks_meta_pr"},
	{"linear", "idx_tasks_meta_linear"},
	{"mesh_thread", "idx_tasks_meta_mesh_thread"},
	{"source_mesh_msg_id", "idx_tasks_meta_source_mesh_msg_id"},
	{"composes", "idx_tasks_meta_composes"},
	{"touches_files", "idx_tasks_meta_touches_files"},
}

// TestMigration078IndicesPresent — fail-fast: every CREATE INDEX in
// the migration file must end up in sqlite_master.
func TestMigration078IndicesPresent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, k := range hotMetaKeys {
		if !indexExists(t, db, k.index) {
			t.Errorf("expected index %q after migration 078, not found", k.index)
		}
	}
}

// TestMigration078IndicesUsedByPlanner — the load-bearing assertion.
// Without the indices the planner emits SCAN tasks for every
// meta_match query (verified in the migration's commit message). With
// the migration applied + ANALYZE run + metaMatchSQL rewritten to the
// id-IN-UNION shape, each indexed key's plan contains the
// corresponding index name on the scalar branch.
func TestMigration078IndicesUsedByPlanner(t *testing.T) {
	t.Parallel()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "indices")

	// Seed enough rows that the planner has incentive to choose an
	// index over a small-table SCAN. SQLite's sqlite_stat tables are
	// only populated by ANALYZE; migration 078 runs ANALYZE itself so
	// the planner has stats by the time this test runs.
	for i := 0; i < 300; i++ {
		_, err := d.q.ExecContext(context.Background(),
			`INSERT INTO tasks (id, workspace_id, title, meta, source_kind,
				assignee_origin_kind, assignee_peer_id, assigned_by_peer_id,
				priority, tags_json, status_history_json, status,
				created_at, updated_at)
			 VALUES (?, ?, 'noise', ?, 'agent', 'local', '', '',
				'normal', '[]', '[]', 'open', ?, ?)`,
			fmt.Sprintf("01PAD%05d", i),
			wsID,
			fmt.Sprintf(`{"branch":"feat/x-%d","worktree":"/tmp/w-%d","pr":"p-%d","linear":"lin-%d","mesh_thread":"mt-%d","source_mesh_msg_id":"src-%d","composes":"c-%d","touches_files":["a/%d.go"]}`,
				i, i, i, i, i, i, i, i),
			int64(1000+i), int64(1000+i),
		)
		if err != nil {
			t.Fatalf("seed pad: %v", err)
		}
	}
	// Re-ANALYZE after seeding to refresh stats — the migration's own
	// ANALYZE ran against an empty table.
	if _, err := d.q.ExecContext(context.Background(), "ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	for _, k := range hotMetaKeys {
		k := k
		t.Run(k.key, func(t *testing.T) {
			plan := explainQueryPlan(t, d.q, k.key, wsID)
			if !strings.Contains(plan, k.index) {
				t.Errorf("expected index %q in plan for meta_match key %q, got:\n%s",
					k.index, k.key, plan)
			}
		})
	}
}

// indexExists reports whether sqlite_master carries an index with the
// given name.
func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	row := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return n == 1
}

// explainQueryPlan runs EXPLAIN QUERY PLAN against the production
// metaMatchSQL shape for `key` and returns the concatenated `detail`
// column — the human-readable plan that contains the index name when
// the planner uses it.
func explainQueryPlan(t *testing.T, db dbQuerier, key, wsID string) string {
	t.Helper()
	sqlStr := `SELECT id FROM tasks WHERE workspace_id = ? AND deleted_at IS NULL AND ` +
		metaMatchSQL(key, "tasks")
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+sqlStr,
		wsID, "any-value", "any-value")
	if err != nil {
		t.Fatalf("explain (%s): %v", key, err)
	}
	defer func() { _ = rows.Close() }()

	var out strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&out, "  id=%d parent=%d %s\n", id, parent, detail)
	}
	return out.String()
}

// dbQuerier is the minimal interface we need for EXPLAIN — both *sql.DB
// and *sql.Tx satisfy it, as does DB.q.
type dbQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
