package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// ensureTaskHLC heals databases whose schema_version was bumped past
// migration 072 without its hlc_at half ever applying (branch swaps /
// parallel-worktree integrations that collided on migration numbers).
// The meta_composed_by half of 072 is healed separately by
// ensureTasksMetaJSONSchema; this invariant owns the HLC column, its
// updated_at-derived backfill, and the gossip watermark index.
// Idempotent: detect via PRAGMA, ALTER + backfill only when missing.
func ensureTaskHLC(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_xinfo(tasks)`)
	if err != nil {
		return fmt.Errorf("pragma table_xinfo tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
			hidden      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk, &hidden); err != nil {
			return fmt.Errorf("scan tasks pragma row: %w", err)
		}
		if name == "hlc_at" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tasks pragma rows: %w", err)
	}
	if !tableExists {
		return nil // table is created by migration 061; nothing to heal yet.
	}
	if !hasColumn {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE tasks ADD COLUMN hlc_at TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add tasks.hlc_at column: %w", err)
		}
		// Same updated_at-derived stamp as 072's backfill, so peers that
		// already hold a watermark don't re-receive every healed row.
		if _, err := db.ExecContext(ctx, `
			UPDATE tasks
			SET hlc_at = printf('%016x%016x', updated_at * 1000, 0)
			WHERE hlc_at = ''`,
		); err != nil {
			return fmt.Errorf("backfill tasks.hlc_at: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_tasks_workspace_hlc
			ON tasks(workspace_id, hlc_at)
			WHERE deleted_at IS NULL`,
	); err != nil {
		return fmt.Errorf("create tasks workspace/hlc index: %w", err)
	}
	return nil
}
