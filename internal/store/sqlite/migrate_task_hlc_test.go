package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestEnsureTaskHLC verifies the invariant heals a database whose
// schema_version passed 072 without the hlc_at column ever applying
// (parallel-branch migration-number collision). Idempotent re-runs.
func TestEnsureTaskHLC(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Simulate the broken state: drop the index then the column, as if
	// 072's HLC half never ran while schema_version moved past it.
	if _, err := db.ExecContext(ctx, `DROP INDEX idx_tasks_workspace_hlc`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE tasks DROP COLUMN hlc_at`); err != nil {
		t.Fatalf("drop column: %v", err)
	}

	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tasks (id, workspace_id, title, meta, source_kind,
			assignee_origin_kind, assignee_peer_id, assigned_by_peer_id,
			priority, tags_json, status_history_json, status,
			created_at, updated_at)
		VALUES ('01HLCTASK', 'ws1', 't', '', 'agent', 'local', '', '',
			'normal', '[]', '[]', 'open', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := ensureTaskHLC(ctx, db); err != nil {
		t.Fatalf("ensureTaskHLC: %v", err)
	}
	// Idempotent second run.
	if err := ensureTaskHLC(ctx, db); err != nil {
		t.Fatalf("ensureTaskHLC rerun: %v", err)
	}

	var hlc string
	if err := db.QueryRowContext(ctx,
		`SELECT hlc_at FROM tasks WHERE id = '01HLCTASK'`,
	).Scan(&hlc); err != nil {
		t.Fatalf("select hlc_at: %v", err)
	}
	want := fmt.Sprintf("%016x%016x", now*1000, 0)
	if hlc != want {
		t.Errorf("hlc_at backfill mismatch: want %q got %q", want, hlc)
	}

	var idx int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_tasks_workspace_hlc'`,
	).Scan(&idx); err != nil {
		t.Fatalf("check index: %v", err)
	}
	if idx != 1 {
		t.Errorf("idx_tasks_workspace_hlc missing after heal")
	}
}
