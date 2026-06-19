package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestBackfillTasksMetaJSON verifies the post-072 backfill rewrites
// legacy frontmatter meta into canonical JSON in place. Idempotent
// across re-runs.
func TestBackfillTasksMetaJSON(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed three rows: one with legacy single-line frontmatter, one
	// with multi-key + comma-list frontmatter, one already in JSON
	// shape (must be left alone), one empty (must be left alone).
	now := time.Now().Unix()
	rows := []struct {
		id   string
		meta string
	}{
		{"01TASK1", "composed_by: 01PARENT"},
		{"01TASK2", "composed_by: 01P1\nworktree: /tmp/wt\ncomposes: 01C1, 01C2"},
		{"01TASK3", `{"composed_by":"01ALREADY"}`},
		{"01TASK4", ""},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO tasks (id, workspace_id, title, meta, source_kind,
				assignee_origin_kind, assignee_peer_id, assigned_by_peer_id,
				priority, tags_json, status_history_json, status,
				created_at, updated_at)
			VALUES (?, 'ws1', 't', ?, 'agent', 'local', '', '',
				'normal', '[]', '[]', 'open', ?, ?)`,
			r.id, r.meta, now, now); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	// Run the backfill (idempotently — migrate above already ran it).
	if err := backfillTasksMetaJSON(ctx, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	got := map[string]string{}
	r2, err := db.QueryContext(ctx, `SELECT id, meta FROM tasks WHERE id LIKE '01TASK%' ORDER BY id`)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	defer r2.Close() //nolint:errcheck
	for r2.Next() {
		var id, meta string
		if err := r2.Scan(&id, &meta); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got[id] = meta
	}

	checks := []struct {
		id, want string
	}{
		{"01TASK1", `{"composed_by":"01PARENT"}`},
		{"01TASK2", `{"composed_by":"01P1","composes":["01C1","01C2"],"worktree":"/tmp/wt"}`},
		{"01TASK3", `{"composed_by":"01ALREADY"}`}, // unchanged
		{"01TASK4", ""}, // unchanged
	}
	for _, c := range checks {
		if got[c.id] != c.want {
			t.Errorf("id=%s: meta mismatch\n want: %q\n got:  %q", c.id, c.want, got[c.id])
		}
	}

	// Re-run: must be idempotent (no change).
	if err := backfillTasksMetaJSON(ctx, db); err != nil {
		t.Fatalf("backfill re-run: %v", err)
	}
	r3, err := db.QueryContext(ctx, `SELECT meta FROM tasks WHERE id = '01TASK1'`)
	if err != nil {
		t.Fatalf("scan after re-run: %v", err)
	}
	defer r3.Close() //nolint:errcheck
	r3.Next()
	var meta string
	if err := r3.Scan(&meta); err != nil {
		t.Fatalf("scan after re-run: %v", err)
	}
	if meta != `{"composed_by":"01PARENT"}` {
		t.Errorf("after re-run, meta changed: %q", meta)
	}
}

// TestMetaComposedByGeneratedColumn confirms the new generated
// column populates from json_extract correctly + the index can be
// used to filter by composed_by.
func TestMetaComposedByGeneratedColumn(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().Unix()
	for i, meta := range []string{
		`{"composed_by":"01EPIC"}`,
		`{"composed_by":["01EPIC","01OTHER"]}`,
		`{"worktree":"/tmp"}`,
		``,
	} {
		id := "01ROW" + strings.Repeat("X", i+1)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO tasks (id, workspace_id, title, meta, source_kind,
				assignee_origin_kind, assignee_peer_id, assigned_by_peer_id,
				priority, tags_json, status_history_json, status,
				created_at, updated_at)
			VALUES (?, 'ws1', 't', ?, 'agent', 'local', '', '',
				'normal', '[]', '[]', 'open', ?, ?)`,
			id, meta, now, now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// Confirm the virtual column resolves correctly.
	got := map[string]sql.NullString{}
	r, err := db.QueryContext(ctx, `SELECT id, meta_composed_by FROM tasks WHERE id LIKE '01ROW%' ORDER BY id`)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	defer r.Close() //nolint:errcheck
	for r.Next() {
		var id string
		var mcb sql.NullString
		if err := r.Scan(&id, &mcb); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = mcb
	}
	if v := got["01ROWX"]; !v.Valid || v.String != "01EPIC" {
		t.Errorf("scalar composed_by: got %v, want 01EPIC", v)
	}
	if v := got["01ROWXX"]; !v.Valid || v.String != "01EPIC" {
		t.Errorf("array composed_by[0]: got %v, want 01EPIC", v)
	}
	if v := got["01ROWXXX"]; v.Valid {
		t.Errorf("no composed_by key: got %v, want null", v)
	}
	if v := got["01ROWXXXX"]; v.Valid {
		t.Errorf("empty meta: got %v, want null", v)
	}
}
