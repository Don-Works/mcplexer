package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ensureTasksMetaJSONSchema heals databases that recorded migration 072
// when it only contained task HLC, before the meta JSON generated column
// was folded into the same migration during branch integration.
func ensureTasksMetaJSONSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_xinfo(tasks)`)
	if err != nil {
		return fmt.Errorf("pragma table_info tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	haveMetaComposedBy := false
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
		if name == "meta_composed_by" {
			haveMetaComposedBy = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tasks pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}

	addedColumn := false
	if !haveMetaComposedBy {
		if _, err := db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN meta_composed_by TEXT
			GENERATED ALWAYS AS (
				CASE
					WHEN NOT json_valid(meta) THEN NULL
					WHEN json_type(meta, '$.composed_by') = 'array'
						THEN json_extract(meta, '$.composed_by[0]')
					ELSE json_extract(meta, '$.composed_by')
				END
			) VIRTUAL`); err != nil {
			return fmt.Errorf("add tasks.meta_composed_by column: %w", err)
		}
		addedColumn = true
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_tasks_meta_composed_by
		ON tasks(meta_composed_by)
		WHERE meta_composed_by IS NOT NULL`); err != nil {
		return fmt.Errorf("create tasks meta_composed_by index: %w", err)
	}
	if addedColumn {
		if err := backfillTasksMetaJSON(ctx, db); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// backfillTasksMetaJSON converts every tasks.meta value that is still
// in the legacy frontmatter shape (one `key: a, b, c` line per key)
// to the canonical JSON-object shape introduced by migration 072.
//
// Idempotent: rows whose meta is already JSON, or empty, are left
// alone. Rows that fail to parse (corrupt frontmatter) are left
// alone too — the dual-read service layer continues to handle them
// on read, and the next write through the service rewrites them.
//
// Why a Go hook (instead of pure SQL): the frontmatter shape is
// line-oriented with comma-separated values per line; SQLite has no
// regex_split helper. A row-by-row loop in Go is straightforward and
// runs once per install (the loop touches every task row, but each
// workspace has on the order of 10²–10³ rows, so even a cold cache
// boot completes in well under a second).
//
// The hook runs OUTSIDE the migration's transaction (per
// migrate.go's postMigrationHooks contract) so a corrupted row can't
// poison the entire upgrade. We wrap the rewrite phase in our own
// transaction so the UPDATEs are atomic per-batch.
func backfillTasksMetaJSON(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, meta FROM tasks WHERE deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("scan tasks for meta backfill: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	type pending struct {
		id   string
		meta string
	}
	var todo []pending
	for rows.Next() {
		var id, meta string
		if err := rows.Scan(&id, &meta); err != nil {
			return fmt.Errorf("scan task meta row: %w", err)
		}
		// Skip empty + already-JSON rows. Empty stays empty (the
		// "no metadata" sentinel); JSON-shaped meta starts with `{`.
		t := strings.TrimSpace(meta)
		if t == "" || strings.HasPrefix(t, "{") {
			continue
		}
		js, err := frontmatterToJSON(meta)
		if err != nil || js == "" {
			continue
		}
		if js == meta {
			continue
		}
		todo = append(todo, pending{id: id, meta: js})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate task meta rows: %w", err)
	}
	if len(todo) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin meta backfill tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `UPDATE tasks SET meta = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare meta backfill update: %w", err)
	}
	defer stmt.Close() //nolint:errcheck

	for _, p := range todo {
		if _, err := stmt.ExecContext(ctx, p.meta, p.id); err != nil {
			return fmt.Errorf("backfill meta for %s: %w", p.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit meta backfill: %w", err)
	}
	return nil
}

// frontmatterToJSON is a small standalone parser for the pre-072
// `key: a, b, c` line-oriented meta shape. Returns a JSON object
// string with sorted keys; the service layer's MetaToJSON would
// also do this but importing internal/tasks from internal/store
// would introduce a dependency cycle. Keeping the converter local
// is cheaper than restructuring.
func frontmatterToJSON(meta string) (string, error) {
	obj := map[string]any{}
	for _, line := range strings.Split(meta, "\n") {
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}
		body := strings.TrimSpace(line[idx+1:])
		parts := []string{}
		for _, p := range strings.Split(body, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
		switch len(parts) {
		case 0:
			obj[key] = ""
		case 1:
			obj[key] = parts[0]
		default:
			arr := make([]any, len(parts))
			for i, p := range parts {
				arr[i] = p
			}
			obj[key] = arr
		}
	}
	if len(obj) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		kj, _ := json.Marshal(k)
		vj, err := json.Marshal(obj[k])
		if err != nil {
			return "", err
		}
		pairs = append(pairs, string(kj)+":"+string(vj))
	}
	return "{" + strings.Join(pairs, ",") + "}", nil
}
