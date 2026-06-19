// task_meta_filter_test.go — exercises the meta_match / meta_has_key /
// meta_in filter args added by migration 072. Seed a workspace with a
// dozen tasks across varied meta shapes and assert each filter
// returns exactly the rows we expect.
package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// seedMetaTasks inserts 10 tasks with varied meta shapes for the
// filter-tests below. Returns the workspace id + a {id → expected
// meta keys} map so callers can spot-check.
func seedMetaTasks(t *testing.T, d *DB) (string, []store.Task) {
	t.Helper()
	wsID := seedWorkspace(t, d, "meta-filters")
	tasks := []struct {
		title, meta string
	}{
		{"child-of-epic-1", `{"composed_by":"01EPIC"}`},
		{"child-of-epic-2", `{"composed_by":"01EPIC","branch":"feat/a"}`},
		{"child-of-epic-2", `{"composed_by":["01EPIC","01OTHER"]}`},
		{"child-of-other", `{"composed_by":"01OTHER"}`},
		{"epic-itself", `{"composes":["01ROW1","01ROW2","01ROW3"]}`},
		{"with-worktree-only", `{"worktree":"/tmp/wt"}`},
		{"with-branch-and-pr", `{"branch":"feat/b","pr":"https://example.com/pr/1"}`},
		{"with-status-kind-working", `{"status_kind":"working"}`},
		{"with-status-kind-blocked", `{"status_kind":"blocked"}`},
		{"no-meta", ""},
	}
	out := make([]store.Task, 0, len(tasks))
	for _, ts := range tasks {
		row := &store.Task{
			WorkspaceID: wsID,
			Title:       ts.title,
			Status:      "open",
			Priority:    "normal",
			TagsJSON:    json.RawMessage("[]"),
			Meta:        ts.meta,
		}
		if err := d.CreateTask(context.Background(), row); err != nil {
			t.Fatalf("seed %s: %v", ts.title, err)
		}
		out = append(out, *row)
	}
	return wsID, out
}

// TestListTasksMetaMatchScalar — the composed_by index path. Hits
// the generated column.
func TestListTasksMetaMatchScalar(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaMatch:   map[string]string{"composed_by": "01EPIC"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 tasks composed_by=01EPIC, got %d: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaMatchNonGenerated — exercises the json_extract
// fallback path on a non-indexed key (worktree).
func TestListTasksMetaMatchNonGenerated(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaMatch:   map[string]string{"worktree": "/tmp/wt"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 task with worktree=/tmp/wt, got %d: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaMatchArrayContainment — `composes` is stored as
// an array on the epic row; matching by element must work.
func TestListTasksMetaMatchArrayContainment(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaMatch:   map[string]string{"composes": "01ROW1"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "epic-itself" {
		t.Fatalf("array containment failed: got %d rows: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaMatchMultipleKeys — AND across keys.
func TestListTasksMetaMatchMultipleKeys(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaMatch:   map[string]string{"composed_by": "01EPIC", "branch": "feat/a"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 task matching both keys, got %d: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaHasKey — presence regardless of value.
func TestListTasksMetaHasKey(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaHasKey:  []string{"branch"},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 tasks with `branch` meta key, got %d: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaIn — value at key is one of N.
func TestListTasksMetaIn(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaIn:      map[string][]string{"status_kind": {"working", "blocked"}},
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 tasks with status_kind in {working, blocked}, got %d: %v", len(rows), titles(rows))
	}
}

// TestListTasksMetaFiltersAgainstLegacyFrontmatter — legacy rows
// silently skip the filter (don't raise). They'll match again once
// they go through MetaToJSON on the next service-level write.
func TestListTasksMetaFiltersAgainstLegacyFrontmatter(t *testing.T) {
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "legacy")
	// Direct insert bypassing service-level normalisation — simulates
	// a row that survived pre-072 and hasn't been touched since.
	if err := d.CreateTask(context.Background(), &store.Task{
		WorkspaceID: wsID, Title: "legacy", Status: "open",
		Priority: "normal", TagsJSON: json.RawMessage("[]"),
		Meta: "composed_by: 01EPIC",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaMatch:   map[string]string{"composed_by": "01EPIC"},
	})
	if err != nil {
		t.Fatalf("ListTasks should not raise on legacy meta: %v", err)
	}
	// Legacy meta deliberately doesn't match the JSON path filter —
	// it'll get rewritten by the backfill or the next write. The
	// contract is "no error", not "matches as if it were JSON".
	if len(rows) != 0 {
		t.Errorf("legacy meta unexpectedly matched: %v", titles(rows))
	}
}

// titles is a debug helper that pulls out task titles for assertion
// failure messages.
func titles(rows []store.Task) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Title
	}
	return out
}

// TestListTasksMetaKeyRejected — store-layer validation rejects unsafe
// meta keys containing characters outside [a-zA-Z0-9_-]. This is
// defense-in-depth: the handler/API layers validate upstream, but the
// store layer rejects independently so a key can never reach SQL
// interpolation without passing this check.
func TestListTasksMetaKeyRejected(t *testing.T) {
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "validation")

	cases := []struct {
		name string
		key  string
	}{
		{"semicolon", "branch;DROP TABLE tasks"},
		{"single_quote", "branch' OR 1=1--"},
		{"double_quote", `key"value`},
		{"space", "my key"},
		{"dot", "meta.key"},
		{"slash", "a/b"},
		{"unicode", "key\x00val"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.ListTasks(context.Background(), store.TaskFilter{
				WorkspaceID: wsID,
				MetaMatch:   map[string]string{tc.key: "val"},
			})
			if err == nil {
				t.Fatalf("expected error for meta key %q, got nil", tc.key)
			}
		})
	}
}

// TestListTasksMetaHasKeyRejected — same validation for meta_has_key.
func TestListTasksMetaHasKeyRejected(t *testing.T) {
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "validation-haskey")

	_, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaHasKey:  []string{"bad;key"},
	})
	if err == nil {
		t.Fatal("expected error for unsafe meta_has_key, got nil")
	}
}

// TestListTasksMetaInRejected — same validation for meta_in.
func TestListTasksMetaInRejected(t *testing.T) {
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "validation-in")

	_, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaIn:      map[string][]string{"bad/key": {"a", "b"}},
	})
	if err == nil {
		t.Fatal("expected error for unsafe meta_in key, got nil")
	}
}

// TestListTasksMetaKeyValid — valid keys still work.
func TestListTasksMetaKeyValid(t *testing.T) {
	d := newMemDB(t)
	wsID, _ := seedMetaTasks(t, d)
	rows, err := d.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: wsID,
		MetaHasKey:  []string{"branch"},
	})
	if err != nil {
		t.Fatalf("ListTasks with valid key: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(rows))
	}
}
