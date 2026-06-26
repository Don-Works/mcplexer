// task_companions_test.go — coverage of the Phase 5 admin helpers on
// the tasks store layer: SelectDistinctTaskStatuses and
// RebindPeerInTasks. Existing CRUD coverage for notes / vocabulary /
// offers / bindings lives in task_test.go.
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSelectDistinctTaskStatuses_GroupsByStatus(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-status")

	seed := []struct {
		title, status string
	}{
		{"a", "open"},
		{"b", "open"},
		{"c", "open"},
		{"d", "in-progress"},
		{"e", "in-progress"},
		{"f", "done"},
	}
	for _, s := range seed {
		if err := d.CreateTask(ctx, &store.Task{
			WorkspaceID: wsID, Title: s.title, Status: s.status,
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", s.title, err)
		}
	}

	counts, err := d.SelectDistinctTaskStatuses(ctx, wsID)
	if err != nil {
		t.Fatalf("SelectDistinctTaskStatuses: %v", err)
	}
	want := map[string]int{"open": 3, "in-progress": 2, "done": 1}
	if len(counts) != len(want) {
		t.Fatalf("expected %d distinct statuses, got %d (%v)", len(want), len(counts), counts)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%q] = %d, want %d", k, counts[k], v)
		}
	}
}

func TestSelectDistinctTaskStatuses_IgnoresDeleted(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-deleted")

	live := &store.Task{WorkspaceID: wsID, Title: "live", Status: "doing"}
	gone := &store.Task{WorkspaceID: wsID, Title: "gone", Status: "doing"}
	if err := d.CreateTask(ctx, live); err != nil {
		t.Fatalf("CreateTask live: %v", err)
	}
	if err := d.CreateTask(ctx, gone); err != nil {
		t.Fatalf("CreateTask gone: %v", err)
	}
	if err := d.SoftDeleteTask(ctx, gone.ID); err != nil {
		t.Fatalf("SoftDeleteTask: %v", err)
	}

	counts, err := d.SelectDistinctTaskStatuses(ctx, wsID)
	if err != nil {
		t.Fatalf("SelectDistinctTaskStatuses: %v", err)
	}
	if counts["doing"] != 1 {
		t.Errorf("expected 1 live doing row after soft-delete, got %d", counts["doing"])
	}
}

func TestSelectDistinctTaskStatuses_ScopesToWorkspace(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws1 := seedWorkspace(t, d, "ws-1")
	ws2 := seedWorkspace(t, d, "ws-2")
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: ws1, Title: "x", Status: "review"})
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: ws2, Title: "y", Status: "review"})
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: ws2, Title: "z", Status: "blocked"})

	counts1, _ := d.SelectDistinctTaskStatuses(ctx, ws1)
	if counts1["review"] != 1 || len(counts1) != 1 {
		t.Errorf("ws1 expected {review:1}, got %v", counts1)
	}
	counts2, _ := d.SelectDistinctTaskStatuses(ctx, ws2)
	if counts2["review"] != 1 || counts2["blocked"] != 1 || len(counts2) != 2 {
		t.Errorf("ws2 expected {review:1, blocked:1}, got %v", counts2)
	}
}

func TestCountTaskStatusesFiltersStateAndWorkspace(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws1 := seedWorkspace(t, d, "ws-status-state-1")
	ws2 := seedWorkspace(t, d, "ws-status-state-2")
	now := time.Now().UTC()

	rows := []*store.Task{
		{WorkspaceID: ws1, Title: "open a", Status: "triage"},
		{WorkspaceID: ws1, Title: "open b", Status: "triage"},
		{WorkspaceID: ws1, Title: "working", Status: "coding"},
		{WorkspaceID: ws1, Title: "closed", Status: "done", ClosedAt: &now},
		{WorkspaceID: ws2, Title: "other", Status: "remote"},
	}
	for _, row := range rows {
		if err := d.CreateTask(ctx, row); err != nil {
			t.Fatalf("CreateTask %s: %v", row.Title, err)
		}
	}
	deleted := &store.Task{WorkspaceID: ws1, Title: "deleted", Status: "ghost"}
	if err := d.CreateTask(ctx, deleted); err != nil {
		t.Fatalf("CreateTask deleted: %v", err)
	}
	if err := d.SoftDeleteTask(ctx, deleted.ID); err != nil {
		t.Fatalf("SoftDeleteTask: %v", err)
	}

	openCounts, err := d.CountTaskStatuses(ctx, ws1, "open")
	if err != nil {
		t.Fatalf("CountTaskStatuses open: %v", err)
	}
	if openCounts["triage"] != 2 || openCounts["coding"] != 1 || openCounts["done"] != 0 || openCounts["ghost"] != 0 {
		t.Fatalf("open counts = %v, want triage/coding only", openCounts)
	}
	closedCounts, err := d.CountTaskStatuses(ctx, ws1, "closed")
	if err != nil {
		t.Fatalf("CountTaskStatuses closed: %v", err)
	}
	if len(closedCounts) != 1 || closedCounts["done"] != 1 {
		t.Fatalf("closed counts = %v, want {done:1}", closedCounts)
	}
	allCounts, err := d.CountTaskStatuses(ctx, "", "all")
	if err != nil {
		t.Fatalf("CountTaskStatuses all: %v", err)
	}
	if allCounts["remote"] != 1 || allCounts["done"] != 1 || allCounts["ghost"] != 0 {
		t.Fatalf("all counts = %v, want remote and done but not ghost", allCounts)
	}
}

// TestMaxHLCForWorkspace pins the watermark accessor: empty workspace
// returns "", the max tracks the latest write, soft-deleted rows are
// excluded, and other workspaces don't leak in.
func TestMaxHLCForWorkspace(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA := seedWorkspace(t, d, "ws-hlc-a")
	wsB := seedWorkspace(t, d, "ws-hlc-b")

	// Empty workspace — no error, empty watermark.
	got, err := d.MaxHLCForWorkspace(ctx, wsA)
	if err != nil {
		t.Fatalf("MaxHLCForWorkspace empty: %v", err)
	}
	if got != "" {
		t.Fatalf("empty workspace watermark = %q, want \"\"", got)
	}

	t1 := &store.Task{WorkspaceID: wsA, Title: "first"}
	t2 := &store.Task{WorkspaceID: wsA, Title: "second"}
	other := &store.Task{WorkspaceID: wsB, Title: "other"}
	for _, row := range []*store.Task{t1, t2, other} {
		if err := d.CreateTask(ctx, row); err != nil {
			t.Fatalf("CreateTask %s: %v", row.Title, err)
		}
	}
	got, err = d.MaxHLCForWorkspace(ctx, wsA)
	if err != nil {
		t.Fatalf("MaxHLCForWorkspace: %v", err)
	}
	if got != t2.HlcAt {
		t.Fatalf("watermark = %q, want latest stamp %q", got, t2.HlcAt)
	}
	if got == other.HlcAt {
		t.Fatalf("watermark leaked from another workspace: %q", got)
	}

	// Soft-deleting the newest row rolls the watermark back to t1.
	if err := d.SoftDeleteTask(ctx, t2.ID); err != nil {
		t.Fatalf("SoftDeleteTask: %v", err)
	}
	got, err = d.MaxHLCForWorkspace(ctx, wsA)
	if err != nil {
		t.Fatalf("MaxHLCForWorkspace post-delete: %v", err)
	}
	if got != t1.HlcAt {
		t.Fatalf("post-delete watermark = %q, want %q", got, t1.HlcAt)
	}

	// Required arg guard.
	if _, err := d.MaxHLCForWorkspace(ctx, ""); err == nil {
		t.Fatalf("expected error for empty workspace_id")
	}
}

func TestRebindPeerInTasks_RewritesAllReferences(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-rebind")

	// One task assigned to peer-old, one with origin = peer-old, one
	// with assigned_by = peer-old. RebindPeer should touch all three.
	t1 := &store.Task{
		WorkspaceID: wsID, Title: "assignee",
		AssigneeOriginKind: store.TaskAssigneePeer, AssigneePeerID: "peer-old",
	}
	t2 := &store.Task{WorkspaceID: wsID, Title: "origin", OriginPeerID: "peer-old"}
	t3 := &store.Task{
		WorkspaceID: wsID, Title: "assigned_by", AssignedByPeerID: "peer-old",
	}
	for _, x := range []*store.Task{t1, t2, t3} {
		if err := d.CreateTask(ctx, x); err != nil {
			t.Fatalf("CreateTask %s: %v", x.Title, err)
		}
	}

	// Offers + binding referencing peer-old.
	offer := &store.TaskOffer{
		RemoteTaskID: "rem-1", FromPeerID: "peer-old", ToPeerID: "self",
		RemoteWorkspaceID: "rw1", Title: "off", EnvelopeNonce: "n1", Direction: "incoming",
	}
	if err := d.CreateTaskOffer(ctx, offer); err != nil {
		t.Fatalf("CreateTaskOffer: %v", err)
	}
	if err := d.UpsertWorkspacePeerBinding(ctx, &store.WorkspacePeerBinding{
		PeerID: "peer-old", RemoteWorkspaceID: "rw1", LocalWorkspaceID: wsID,
	}); err != nil {
		t.Fatalf("UpsertWorkspacePeerBinding: %v", err)
	}

	counts, err := d.RebindPeerInTasks(ctx, "peer-old", "peer-new")
	if err != nil {
		t.Fatalf("RebindPeerInTasks: %v", err)
	}
	want := map[string]int{
		"tasks_assignee":          1,
		"tasks_origin":            1,
		"tasks_assigned_by":       1,
		"task_offers_from":        1,
		"task_offers_to":          0,
		"workspace_peer_bindings": 1,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%q] = %d, want %d", k, counts[k], v)
		}
	}

	// Spot-check the rewrites landed.
	g1, _ := d.GetTask(ctx, t1.ID)
	if g1.AssigneePeerID != "peer-new" {
		t.Errorf("t1.AssigneePeerID = %q, want peer-new", g1.AssigneePeerID)
	}
	g2, _ := d.GetTask(ctx, t2.ID)
	if g2.OriginPeerID != "peer-new" {
		t.Errorf("t2.OriginPeerID = %q, want peer-new", g2.OriginPeerID)
	}
	g3, _ := d.GetTask(ctx, t3.ID)
	if g3.AssignedByPeerID != "peer-new" {
		t.Errorf("t3.AssignedByPeerID = %q, want peer-new", g3.AssignedByPeerID)
	}
}

func TestRebindPeerInTasks_NoMatchesIsZeroCounts(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	counts, err := d.RebindPeerInTasks(ctx, "ghost", "spirit")
	if err != nil {
		t.Fatalf("RebindPeerInTasks: %v", err)
	}
	for k, v := range counts {
		if v != 0 {
			t.Errorf("expected 0 for %q with empty DB, got %d", k, v)
		}
	}
}

func TestRebindPeerInTasks_RejectsEmptyOrIdentical(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	if _, err := d.RebindPeerInTasks(ctx, "", "x"); err == nil {
		t.Fatal("expected error for empty old peer id")
	}
	if _, err := d.RebindPeerInTasks(ctx, "x", ""); err == nil {
		t.Fatal("expected error for empty new peer id")
	}
	if _, err := d.RebindPeerInTasks(ctx, "a", "a"); err == nil {
		t.Fatal("expected error when old == new")
	}
}

// TestMigration064_BundledTaskTemplatePresent verifies migration 064
// seeded the task-status-consolidator worker_templates row on a fresh
// install. The migrate() pipeline runs all .sql files in order, so this
// test only needs to open a fresh DB and inspect worker_templates.
func TestMigration064_BundledTaskTemplatePresent(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	var name string
	err := d.Raw().QueryRowContext(ctx,
		`SELECT name FROM worker_templates
		 WHERE id = 'template-bundled-task-status-consolidator'`).Scan(&name)
	if err != nil {
		t.Fatalf("expected bundled task-status-consolidator template row: %v", err)
	}
	if name != "task-status-consolidator" {
		t.Fatalf("name = %q, want task-status-consolidator", name)
	}
}

// TestMigration064_IsIdempotent re-applies the INSERT OR IGNORE
// payload of 064 and confirms the row count stays at 1. This is the
// "re-runs on a partially-seeded DB are a no-op" guarantee the file
// header advertises.
func TestMigration064_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	const sql = `INSERT OR IGNORE INTO worker_templates (
		id, name, version, content_hash, description, body,
		metadata_json, tags_json, author, parent_version,
		published_at, created_by_agent_id, workspace_id
	) VALUES (
		'template-bundled-task-status-consolidator',
		'task-status-consolidator',
		1,
		'bundled-builtin-task-status-consolidator-v1',
		'desc', '{}', '{}', '[]', 'mcplexer-builtin', NULL,
		CAST(strftime('%s', '2026-05-22') AS INTEGER), NULL, NULL
	)`
	if _, err := d.Raw().ExecContext(ctx, sql); err != nil {
		t.Fatalf("re-apply migration body: %v", err)
	}
	var n int
	if err := d.Raw().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM worker_templates WHERE id = 'template-bundled-task-status-consolidator'`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row after re-apply, got %d", n)
	}
}
