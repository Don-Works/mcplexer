// task_test.go — coverage of the tasks store layer (migration 061).
// Mirrors memory_test.go conventions. Exercises CRUD, soft-delete, the
// FTS5 mirror via SearchTasks, the per-workspace status vocabulary,
// task offers, task notes, and the workspace cascade.
package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedWorkspace inserts a workspace row so tests have a valid FK
// target without depending on the config layer.
func seedWorkspace(t *testing.T, d *DB, name string) string {
	t.Helper()
	w := &store.Workspace{Name: name, RootPath: "/tmp/" + name, Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return w.ID
}

func TestCreateTaskInsertsRow(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")

	task := &store.Task{
		WorkspaceID: wsID,
		Title:       "Fix the build",
		Status:      "open",
		Priority:    "normal",
	}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	got, err := d.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Title != "Fix the build" {
		t.Fatalf("title mismatch: %q", got.Title)
	}
	if got.SourceKind != store.TaskSourceAgent {
		t.Fatalf("expected default source=agent, got %q", got.SourceKind)
	}
	if got.AssigneeOriginKind != store.TaskAssigneeLocal {
		t.Fatalf("expected default assignee_origin_kind=local, got %q", got.AssigneeOriginKind)
	}
}

func TestUpdateTaskTouchesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")

	task := &store.Task{WorkspaceID: wsID, Title: "Initial"}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	original := task.UpdatedAt
	time.Sleep(1100 * time.Millisecond) // unix-second resolution
	task.Title = "Updated"
	if err := d.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	got, _ := d.GetTask(ctx, task.ID)
	if got.Title != "Updated" {
		t.Fatalf("title not persisted: %q", got.Title)
	}
	if !got.UpdatedAt.After(original) {
		t.Fatalf("updated_at not bumped: %v vs %v", got.UpdatedAt, original)
	}
}

func TestSoftDeleteHidesFromGetAndList(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")

	task := &store.Task{WorkspaceID: wsID, Title: "Going away"}
	_ = d.CreateTask(ctx, task)

	if err := d.SoftDeleteTask(ctx, task.ID); err != nil {
		t.Fatalf("SoftDeleteTask: %v", err)
	}
	_, err := d.GetTask(ctx, task.ID)
	if err == nil {
		t.Fatalf("expected ErrNotFound after soft delete")
	}
	rows, _ := d.ListTasks(ctx, store.TaskFilter{WorkspaceID: wsID})
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", len(rows))
	}
}

func TestListTasksFiltersByAssignee(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")
	for i, title := range []string{"a", "b", "c"} {
		assignee := "agent-a"
		if i == 1 {
			assignee = "elliot"
		}
		_ = d.CreateTask(ctx, &store.Task{
			WorkspaceID:       wsID,
			Title:             title,
			AssigneeSessionID: assignee,
		})
	}
	rows, _ := d.ListTasks(ctx, store.TaskFilter{
		WorkspaceID:       wsID,
		AssigneeSessionID: "agent-a",
	})
	if len(rows) != 2 {
		t.Fatalf("expected 2 max-assigned tasks, got %d", len(rows))
	}
}

func TestListTaskIDsByPrefix(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA := seedWorkspace(t, d, "ws-prefix-a")
	wsB := seedWorkspace(t, d, "ws-prefix-b")

	// Seed deterministic ids in workspace A (Crockford alphabet, no I/L/O/U).
	idsA := []string{
		"01KSPREFX0000000000000001A",
		"01KSPREFX0000000000000002B",
		"01KSPREFY0000000000000003C", // sibling that should NOT match the "PREFX" probe
	}
	for _, id := range idsA {
		if err := d.CreateTask(ctx, &store.Task{
			ID: id, WorkspaceID: wsA, Title: "a-" + id,
		}); err != nil {
			t.Fatalf("seed CreateTask %s: %v", id, err)
		}
	}
	// A task in workspace B with the same prefix — must NOT leak across
	// workspaces. Closes the cross-workspace existence-leak concern.
	if err := d.CreateTask(ctx, &store.Task{
		ID: "01KSPREFX0BLEEDS0VER000000", WorkspaceID: wsB, Title: "b",
	}); err != nil {
		t.Fatalf("seed cross-ws task: %v", err)
	}

	hits, err := d.ListTaskIDsByPrefix(ctx, wsA, "01KSPREFX", 10)
	if err != nil {
		t.Fatalf("ListTaskIDsByPrefix: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits in wsA, got %d: %v", len(hits), hits)
	}
	got := map[string]bool{}
	for _, id := range hits {
		got[id] = true
	}
	if !got["01KSPREFX0000000000000001A"] || !got["01KSPREFX0000000000000002B"] {
		t.Errorf("hits missing expected ids, got %v", hits)
	}

	// Case-insensitive: lowercase input must resolve through the
	// sanitizer to the same canonical uppercase pattern.
	lower, err := d.ListTaskIDsByPrefix(ctx, wsA, "01ksprefix", 10)
	if err != nil {
		t.Fatalf("ListTaskIDsByPrefix lowercase: %v", err)
	}
	if len(lower) != 2 {
		t.Errorf("case-insensitive: expected 2 hits, got %d", len(lower))
	}

	// Workspace isolation: prefix that matches in B must not surface in A.
	none, err := d.ListTaskIDsByPrefix(ctx, wsA, "01KSPREFX0BLEEDS", 10)
	if err != nil {
		t.Fatalf("ListTaskIDsByPrefix isolation: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("expected 0 hits across workspaces, got %d: %v", len(none), none)
	}

	// Empty prefix is rejected (the resolver should never pass "" but
	// belt-and-braces the surface so a regression can't quietly turn
	// into a workspace-wide scan).
	if _, err := d.ListTaskIDsByPrefix(ctx, wsA, "", 10); err == nil {
		t.Error("expected error on empty prefix, got nil")
	}
}

func TestSearchTasksHitsFTSMirror(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Refactor the gateway dispatcher"})
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Document the daemon", Description: "describe the gateway lifecycle"})
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Unrelated"})

	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "gateway")
	if err != nil {
		t.Fatalf("SearchTasks: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected ≥2 FTS hits, got %d", len(rows))
	}
}

func TestSearchTasksMatchesDisplayedIDSuffix(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-suffix")
	otherWS := seedWorkspace(t, d, "ws-suffix-other")

	ids := []string{
		"01KSSHRA000000000000ABC123",
		"01KSSHRB000000000000ABC123",
		"01KSSHRC000000000000ZZZ999",
	}
	for _, id := range ids {
		if err := d.CreateTask(ctx, &store.Task{
			ID: id, WorkspaceID: wsID, Title: "task-" + id,
		}); err != nil {
			t.Fatalf("seed task %s: %v", id, err)
		}
	}
	if err := d.CreateTask(ctx, &store.Task{
		ID: "01KSSHRD000000000000ABC123", WorkspaceID: otherWS, Title: "other workspace",
	}); err != nil {
		t.Fatalf("seed other workspace task: %v", err)
	}

	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "ABC123")
	if err != nil {
		t.Fatalf("SearchTasks suffix: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 suffix matches in workspace, got %d: %v", len(rows), rows)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.ID] = true
	}
	if !got[ids[0]] || !got[ids[1]] || got[ids[2]] {
		t.Fatalf("unexpected suffix matches: %v", got)
	}

	lower, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "#abc123")
	if err != nil {
		t.Fatalf("SearchTasks lower/hash suffix: %v", err)
	}
	if len(lower) != 2 {
		t.Fatalf("expected 2 lower/hash suffix matches, got %d", len(lower))
	}
}

func TestSearchTasksMatchesExactTaskID(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-exact-id")
	id := "01KSEXACT00000000000ABC123"
	if err := d.CreateTask(ctx, &store.Task{
		ID: id, WorkspaceID: wsID, Title: "not searchable by title",
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "task:"+strings.ToLower(id))
	if err != nil {
		t.Fatalf("SearchTasks exact id: %v", err)
	}
	if len(rows) == 0 || rows[0].ID != id {
		t.Fatalf("expected exact id match first, got %+v", rows)
	}
}

// Acceptance: queries that the escape helper neutralises (the canonical
// path) must NOT trip the FTS5 error rewriter. This pins the behaviour
// for the LLM-erg acceptance criterion "task__list({q:'live-test'})
// doesn't regress".
func TestSearchTasksEscapeHelperKeepsLiveTestWorking(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-erg")
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Smoke test the SSE live-test probe"})

	for _, q := range []string{
		"live-test",
		"SSE live-test probe",
		"feat/foo:bar",
		"AND OR NEAR",
		`with "literal quotes"`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, q)
			if err != nil {
				t.Fatalf("SearchTasks(%q) regressed: %v", q, err)
			}
		})
	}
}

func TestSearchTasksMatchesIDPrefix(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-prefix-search")

	ids := []string{
		"01KSPFXA00000000000AAA111",
		"01KSPFXB00000000000AAA222",
		"01KSPFXC00000000000BBB333", // different suffix block
	}
	for _, id := range ids {
		if err := d.CreateTask(ctx, &store.Task{
			ID: id, WorkspaceID: wsID, Title: "task-" + id,
		}); err != nil {
			t.Fatalf("seed task %s: %v", id, err)
		}
	}

	// Prefix matching: "01KSPFXA" should match only the first task.
	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "01KSPFXA")
	if err != nil {
		t.Fatalf("SearchTasks prefix: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != ids[0] {
		t.Fatalf("expected 1 prefix match for 01KSPFXA, got %d: %v", len(rows), rows)
	}

	// Broader prefix: "01KSPFX" should match all three.
	rows, err = d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "01KSPFX")
	if err != nil {
		t.Fatalf("SearchTasks broader prefix: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 broader prefix matches, got %d", len(rows))
	}

	// Case-insensitive prefix.
	rows, err = d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "01kspfx")
	if err != nil {
		t.Fatalf("SearchTasks lowercase prefix: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 case-insensitive prefix matches, got %d", len(rows))
	}
}

func TestSearchTasksTitleFallback(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-title-fb")

	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Deploy the monitoring stack"})
	_ = d.CreateTask(ctx, &store.Task{WorkspaceID: wsID, Title: "Unrelated task"})

	// "monitoring" should match via FTS (porter stemmer).
	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "monitoring")
	if err != nil {
		t.Fatalf("SearchTasks FTS: %v", err)
	}
	if len(rows) < 1 {
		t.Fatal("expected at least 1 FTS hit for 'monitoring'")
	}

	// A query that FTS might not tokenise well but title LIKE catches.
	// "deploy-the" has a hyphen that FTS treats as separator.
	rows, err = d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "deploy-the")
	if err != nil {
		t.Fatalf("SearchTasks title fallback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 title fallback match for 'deploy-the', got %d", len(rows))
	}
}

func TestSearchTasksTagFallback(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-tag-fb")

	_ = d.CreateTask(ctx, &store.Task{
		WorkspaceID: wsID,
		Title:       "Tagged task",
		TagsJSON:    json.RawMessage(`["urgent","backend"]`),
	})
	_ = d.CreateTask(ctx, &store.Task{
		WorkspaceID: wsID,
		Title:       "No tags",
	})

	// FTS indexes tags as space-separated tokens, so "urgent" should work.
	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "urgent")
	if err != nil {
		t.Fatalf("SearchTasks tag FTS: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 tag FTS match, got %d", len(rows))
	}

	// Tag LIKE fallback: search for a tag that appears in JSON array.
	rows, err = d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "backend")
	if err != nil {
		t.Fatalf("SearchTasks tag fallback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 tag fallback match, got %d", len(rows))
	}
}

func TestSearchTasksClosedTasksIncluded(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-closed-search")

	_ = d.CreateTask(ctx, &store.Task{
		WorkspaceID: wsID,
		Title:       "Closed gateway task",
		Status:      "done",
		ClosedAt:    ptrTime(time.Now()),
	})
	_ = d.CreateTask(ctx, &store.Task{
		WorkspaceID: wsID,
		Title:       "Open gateway task",
		Status:      "open",
	})

	// SearchTasks does NOT apply OnlyTerminal, so both open and closed
	// tasks should be returned.
	rows, err := d.SearchTasks(ctx, store.TaskFilter{WorkspaceID: wsID}, "gateway")
	if err != nil {
		t.Fatalf("SearchTasks closed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 matches (open+closed), got %d", len(rows))
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestUpsertVocabAndIsTerminal(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")

	if got, _ := d.IsTerminalStatus(ctx, wsID, "anything"); got {
		t.Fatalf("expected false default for missing vocab entry")
	}
	if got, _ := d.IsTerminalStatus(ctx, wsID, "completed"); !got {
		t.Fatalf("expected completed to default terminal")
	}
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "completed", Kind: "open",
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab completed: %v", err)
	}
	if got, _ := d.IsTerminalStatus(ctx, wsID, "completed"); got {
		t.Fatalf("explicit completed/open vocab should override terminal fallback")
	}
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "shipped", IsTerminal: true,
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}
	got, err := d.IsTerminalStatus(ctx, wsID, "shipped")
	if err != nil || !got {
		t.Fatalf("IsTerminalStatus shipped: got=%v err=%v", got, err)
	}
}

// TestVocabKindRoundTrips — migration 070 added a `kind` column with
// the canonical bucket (open|working|blocked|done|cancelled). Upsert
// must persist the supplied kind and List must read it back; an empty
// kind on the input must default to "open" (matching the column DDL).
// The migration's seed UPDATEs are tested separately via the seeded
// defaults that the cleanup skill / consolidator populate at runtime.
func TestVocabKindRoundTrips(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")

	cases := []struct {
		statusText string
		kindIn     string
		kindWant   string
	}{
		{"triaging", "working", "working"},
		{"awaiting_review", "blocked", "blocked"},
		{"shipped", "done", "done"},
		{"canned", "cancelled", "cancelled"},
		{"draft", "", "open"}, // empty input → defaults to "open"
		{"open", "open", "open"},
	}
	for _, c := range cases {
		if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
			WorkspaceID: wsID, StatusText: c.statusText, Kind: c.kindIn,
		}); err != nil {
			t.Fatalf("UpsertTaskStatusVocab %s: %v", c.statusText, err)
		}
	}
	rows, err := d.ListTaskStatusVocab(ctx, wsID)
	if err != nil {
		t.Fatalf("ListTaskStatusVocab: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.StatusText] = r.Kind
	}
	for _, c := range cases {
		if got[c.statusText] != c.kindWant {
			t.Fatalf("kind for %q: got %q want %q (all=%+v)",
				c.statusText, got[c.statusText], c.kindWant, got)
		}
	}

	// Re-upserting the same row with a NEW kind must replace the
	// stored kind (idempotent reclassification).
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "triaging", Kind: "blocked",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	rows, _ = d.ListTaskStatusVocab(ctx, wsID)
	for _, r := range rows {
		if r.StatusText == "triaging" && r.Kind != "blocked" {
			t.Fatalf("expected triaging kind=blocked after re-upsert, got %q", r.Kind)
		}
	}
}

func TestTaskNoteAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")
	task := &store.Task{WorkspaceID: wsID, Title: "Multi-author task"}
	_ = d.CreateTask(ctx, task)

	for _, author := range []string{"agent-a", "elliot", "agent-a"} {
		_ = d.AppendTaskNote(ctx, &store.TaskNote{
			TaskID: task.ID, AuthorSessionID: author, Body: "comment from " + author,
		})
	}
	notes, err := d.ListTaskNotes(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("ListTaskNotes: %v", err)
	}
	if len(notes) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(notes))
	}
}

func TestWorkspaceDeleteCascadesToTasks(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws1")
	task := &store.Task{WorkspaceID: wsID, Title: "Will outlive its workspace"}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := d.DeleteWorkspace(ctx, wsID); err != nil {
		t.Fatalf("DeleteWorkspace: %v (cascade should soft-delete tasks)", err)
	}
	// Default GetTask filter excludes soft-deleted rows.
	if _, err := d.GetTask(ctx, task.ID); err == nil {
		t.Fatalf("expected task to be soft-deleted after workspace delete")
	}
}

func TestCreateGetRoundTripsLeaseExpiresAt(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-lease")

	expires := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	task := &store.Task{
		WorkspaceID:       wsID,
		Title:             "Leased",
		Status:            "doing",
		AssigneeSessionID: "alice",
		LeaseExpiresAt:    &expires,
	}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := d.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.LeaseExpiresAt == nil {
		t.Fatal("expected LeaseExpiresAt to round-trip, got nil")
	}
	if !got.LeaseExpiresAt.Equal(expires) {
		t.Fatalf("LeaseExpiresAt mismatch: got %v want %v", got.LeaseExpiresAt, expires)
	}
}

func TestHeartbeatTaskOnlyForCurrentAssignee(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-hb")

	task := &store.Task{
		WorkspaceID:       wsID,
		Title:             "hb-target",
		Status:            "doing",
		AssigneeSessionID: "alice",
	}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Wrong session — silent no-op.
	bumped, err := d.HeartbeatTask(ctx, task.ID, "bob", 5*time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatTask bob: %v", err)
	}
	if bumped {
		t.Fatal("expected bumped=false for non-assignee session")
	}
	// Confirm lease was NOT set.
	got, _ := d.GetTask(ctx, task.ID)
	if got.LeaseExpiresAt != nil {
		t.Fatalf("expected nil lease after non-assignee call, got %v", got.LeaseExpiresAt)
	}

	// Correct session — bump succeeds + lease populated.
	bumped, err = d.HeartbeatTask(ctx, task.ID, "alice", 5*time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatTask alice: %v", err)
	}
	if !bumped {
		t.Fatal("expected bumped=true for current assignee")
	}
	got, _ = d.GetTask(ctx, task.ID)
	if got.LeaseExpiresAt == nil {
		t.Fatal("expected non-nil lease after assignee heartbeat")
	}
	if !got.LeaseExpiresAt.After(time.Now().UTC().Add(4 * time.Minute)) {
		t.Fatalf("lease window too short: %v", got.LeaseExpiresAt)
	}
}

func TestClearExpiredTaskLeasesTargetsRightRows(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-clear")

	past := time.Now().UTC().Add(-1 * time.Minute)
	future := time.Now().UTC().Add(5 * time.Minute)

	expired := &store.Task{
		WorkspaceID:       wsID,
		Title:             "expired",
		Status:            "doing",
		AssigneeSessionID: "alice",
		LeaseExpiresAt:    &past,
	}
	live := &store.Task{
		WorkspaceID:       wsID,
		Title:             "live",
		Status:            "doing",
		AssigneeSessionID: "bob",
		LeaseExpiresAt:    &future,
	}
	noLease := &store.Task{
		WorkspaceID:       wsID,
		Title:             "no-lease",
		Status:            "open",
		AssigneeSessionID: "carol",
	}
	if err := d.CreateTask(ctx, expired); err != nil {
		t.Fatalf("CreateTask expired: %v", err)
	}
	if err := d.CreateTask(ctx, live); err != nil {
		t.Fatalf("CreateTask live: %v", err)
	}
	if err := d.CreateTask(ctx, noLease); err != nil {
		t.Fatalf("CreateTask no-lease: %v", err)
	}

	ids, err := d.ClearExpiredTaskLeases(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClearExpiredTaskLeases: %v", err)
	}
	if len(ids) != 1 || ids[0] != expired.ID {
		t.Fatalf("expected cleared=[%s], got %v", expired.ID, ids)
	}

	// Expired row: assignee cleared + lease nulled.
	gotExp, _ := d.GetTask(ctx, expired.ID)
	if gotExp.AssigneeSessionID != "" {
		t.Fatalf("expected expired row's assignee cleared, got %q", gotExp.AssigneeSessionID)
	}
	if gotExp.LeaseExpiresAt != nil {
		t.Fatalf("expected expired row's lease nulled, got %v", gotExp.LeaseExpiresAt)
	}

	// Live row: untouched.
	gotLive, _ := d.GetTask(ctx, live.ID)
	if gotLive.AssigneeSessionID != "bob" {
		t.Fatalf("expected live row's assignee preserved, got %q", gotLive.AssigneeSessionID)
	}
	if gotLive.LeaseExpiresAt == nil {
		t.Fatal("expected live row's lease preserved")
	}

	// No-lease row: untouched.
	gotNo, _ := d.GetTask(ctx, noLease.ID)
	if gotNo.AssigneeSessionID != "carol" {
		t.Fatalf("expected no-lease row's assignee preserved, got %q", gotNo.AssigneeSessionID)
	}
}

// TestClearSessionTaskLeasesScopesToSession pins criterion (c): the
// store-layer primitive is session-scoped — it reclaims rows owned by
// the named session and never touches another session's rows. It also
// pins the STRUCTURAL FIX (criterion d): a matching-session row in a
// WORKING status (here "doing") that carries an assignee but NO lease is
// a zombie and MUST be reclaimed — the old `lease_expires_at IS NOT
// NULL` filter hid it from this disconnect-release path, leaving a dead
// assignee + working status forever. A matching-session NON-working
// no-lease row (e.g. blocked) is left alone since there's nothing to
// reclaim.
func TestClearSessionTaskLeasesScopesToSession(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-sess-clear")

	future := time.Now().UTC().Add(5 * time.Minute)

	aliceLeased := &store.Task{
		WorkspaceID:       wsID,
		Title:             "alice-leased",
		Status:            "doing",
		AssigneeSessionID: "alice",
		LeaseExpiresAt:    &future,
	}
	// Working status, assignee set, NO lease — the zombie the fix targets.
	aliceWorkingNoLease := &store.Task{
		WorkspaceID:       wsID,
		Title:             "alice-working-no-lease",
		Status:            "doing",
		AssigneeSessionID: "alice",
	}
	// NON-working status, assignee set, no lease — nothing to reclaim.
	aliceBlockedNoLease := &store.Task{
		WorkspaceID:       wsID,
		Title:             "alice-blocked-no-lease",
		Status:            "blocked",
		AssigneeSessionID: "alice",
	}
	bobLeased := &store.Task{
		WorkspaceID:       wsID,
		Title:             "bob-leased",
		Status:            "doing",
		AssigneeSessionID: "bob",
		LeaseExpiresAt:    &future,
	}
	for _, tk := range []*store.Task{aliceLeased, aliceWorkingNoLease, aliceBlockedNoLease, bobLeased} {
		if err := d.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.Title, err)
		}
	}

	ids, err := d.ClearSessionTaskLeases(ctx, "alice")
	if err != nil {
		t.Fatalf("ClearSessionTaskLeases: %v", err)
	}
	// Both alice's leased row AND her working-no-lease zombie reclaim.
	gotIDs := map[string]bool{}
	for _, id := range ids {
		gotIDs[id] = true
	}
	if len(ids) != 2 || !gotIDs[aliceLeased.ID] || !gotIDs[aliceWorkingNoLease.ID] {
		t.Fatalf("expected cleared={%s,%s}, got %v", aliceLeased.ID, aliceWorkingNoLease.ID, ids)
	}

	// alice's leased row: assignee + lease cleared.
	gotAL, _ := d.GetTask(ctx, aliceLeased.ID)
	if gotAL.AssigneeSessionID != "" {
		t.Errorf("alice's leased row assignee should be cleared, got %q", gotAL.AssigneeSessionID)
	}
	if gotAL.LeaseExpiresAt != nil {
		t.Errorf("alice's leased row lease should be nulled, got %v", gotAL.LeaseExpiresAt)
	}

	// alice's working-no-lease zombie: assignee cleared (status stays
	// "doing" — the service layer demotes to "open" on the returned id).
	gotZ, _ := d.GetTask(ctx, aliceWorkingNoLease.ID)
	if gotZ.AssigneeSessionID != "" {
		t.Errorf("alice's working-no-lease zombie assignee must be reclaimed, got %q", gotZ.AssigneeSessionID)
	}

	// alice's blocked-no-lease row: non-working, nothing to reclaim.
	gotBN, _ := d.GetTask(ctx, aliceBlockedNoLease.ID)
	if gotBN.AssigneeSessionID != "alice" {
		t.Errorf("alice's blocked no-lease row must be left alone, got assignee %q", gotBN.AssigneeSessionID)
	}

	// bob's row: different session, untouched.
	gotB, _ := d.GetTask(ctx, bobLeased.ID)
	if gotB.AssigneeSessionID != "bob" {
		t.Errorf("bob's row assignee must be preserved, got %q", gotB.AssigneeSessionID)
	}
	if gotB.LeaseExpiresAt == nil {
		t.Error("bob's row lease must be preserved")
	}
}

// TestClearSessionTaskLeasesGuardsEmptySession pins criterion (c): an
// empty session id must be a no-op, never a workspace-wide assignee
// wipe.
func TestClearSessionTaskLeasesGuardsEmptySession(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-empty-sess")

	past := time.Now().UTC().Add(-1 * time.Minute)
	held := &store.Task{
		WorkspaceID:       wsID,
		Title:             "held",
		Status:            "doing",
		AssigneeSessionID: "alice",
		LeaseExpiresAt:    &past,
	}
	if err := d.CreateTask(ctx, held); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	ids, err := d.ClearSessionTaskLeases(ctx, "")
	if err != nil {
		t.Fatalf("ClearSessionTaskLeases(empty): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty session must clear nothing, got %v", ids)
	}
	got, _ := d.GetTask(ctx, held.ID)
	if got.AssigneeSessionID != "alice" {
		t.Errorf("empty-session call must not wipe other rows, got assignee %q", got.AssigneeSessionID)
	}
}

// TestClearExpiredTaskLeasesDemotesNonWorkingAndReclaimsZombie covers
// criteria (a), (b), and (d) for the global passive sweep:
//   - (a) a working row PAST its lease is reclaimed;
//   - (b) a NON-working (blocked) row past its lease has its lease +
//     assignee cleared but its STATUS is untouched by the store (the
//     service only demotes working statuses);
//   - (d) a NO-LEASE working zombie is reclaimed even though it never
//     held a lease at all.
func TestClearExpiredTaskLeasesDemotesNonWorkingAndReclaimsZombie(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-sweep")

	past := time.Now().UTC().Add(-1 * time.Minute)
	future := time.Now().UTC().Add(5 * time.Minute)

	workingExpired := &store.Task{
		WorkspaceID: wsID, Title: "working-expired",
		Status: "doing", AssigneeSessionID: "alice", LeaseExpiresAt: &past,
	}
	blockedExpired := &store.Task{
		WorkspaceID: wsID, Title: "blocked-expired",
		Status: "blocked", AssigneeSessionID: "bob", LeaseExpiresAt: &past,
	}
	workingNoLease := &store.Task{
		WorkspaceID: wsID, Title: "working-no-lease-zombie",
		Status: "doing", AssigneeSessionID: "carol",
	}
	blockedNoLease := &store.Task{
		WorkspaceID: wsID, Title: "blocked-no-lease",
		Status: "blocked", AssigneeSessionID: "dave",
	}
	workingLive := &store.Task{
		WorkspaceID: wsID, Title: "working-live",
		Status: "doing", AssigneeSessionID: "erin", LeaseExpiresAt: &future,
	}
	for _, tk := range []*store.Task{workingExpired, blockedExpired, workingNoLease, blockedNoLease, workingLive} {
		if err := d.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.Title, err)
		}
	}

	ids, err := d.ClearExpiredTaskLeases(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClearExpiredTaskLeases: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	// (a) working-expired, (b) blocked-expired, (d) working-no-lease.
	if len(ids) != 3 || !got[workingExpired.ID] || !got[blockedExpired.ID] || !got[workingNoLease.ID] {
		t.Fatalf("expected cleared={working-expired,blocked-expired,working-no-lease}, got %v", ids)
	}

	// (a) working past-lease: assignee + lease cleared.
	gotWE, _ := d.GetTask(ctx, workingExpired.ID)
	if gotWE.AssigneeSessionID != "" || gotWE.LeaseExpiresAt != nil {
		t.Errorf("working-expired must be reclaimed, got assignee=%q lease=%v", gotWE.AssigneeSessionID, gotWE.LeaseExpiresAt)
	}

	// (b) blocked past-lease: lease + assignee cleared, status preserved
	// (the STORE never rewrites status — the service skips non-working).
	gotBE, _ := d.GetTask(ctx, blockedExpired.ID)
	if gotBE.AssigneeSessionID != "" || gotBE.LeaseExpiresAt != nil {
		t.Errorf("blocked-expired must have lease+assignee cleared, got assignee=%q lease=%v", gotBE.AssigneeSessionID, gotBE.LeaseExpiresAt)
	}
	if gotBE.Status != "blocked" {
		t.Errorf("blocked-expired status must be left untouched by the store, got %q", gotBE.Status)
	}

	// (d) working no-lease zombie: reclaimed.
	gotZ, _ := d.GetTask(ctx, workingNoLease.ID)
	if gotZ.AssigneeSessionID != "" {
		t.Errorf("working-no-lease zombie must be reclaimed, got assignee %q", gotZ.AssigneeSessionID)
	}

	// blocked no-lease: nothing to reclaim, untouched.
	gotBN, _ := d.GetTask(ctx, blockedNoLease.ID)
	if gotBN.AssigneeSessionID != "dave" {
		t.Errorf("blocked-no-lease must be left alone, got assignee %q", gotBN.AssigneeSessionID)
	}

	// working live: lease in the future, untouched.
	gotWL, _ := d.GetTask(ctx, workingLive.ID)
	if gotWL.AssigneeSessionID != "erin" || gotWL.LeaseExpiresAt == nil {
		t.Errorf("working-live must be preserved, got assignee=%q lease=%v", gotWL.AssigneeSessionID, gotWL.LeaseExpiresAt)
	}
}

// TestClearExpiredTaskLeasesRespectsCustomVocab pins that the working-
// status predicate consults per-workspace vocabulary, not just the
// literal "doing": a custom working status ("in_progress") with no lease
// is a reclaimable zombie, while a custom non-working status ("paused")
// is not.
func TestClearExpiredTaskLeasesRespectsCustomVocab(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsID := seedWorkspace(t, d, "ws-custom-vocab")

	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "in_progress", Kind: "working",
	}); err != nil {
		t.Fatalf("seed working vocab: %v", err)
	}
	if err := d.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "paused", Kind: "blocked",
	}); err != nil {
		t.Fatalf("seed paused vocab: %v", err)
	}

	customWorking := &store.Task{
		WorkspaceID: wsID, Title: "custom-working-no-lease",
		Status: "in_progress", AssigneeSessionID: "alice",
	}
	customPaused := &store.Task{
		WorkspaceID: wsID, Title: "custom-paused-no-lease",
		Status: "paused", AssigneeSessionID: "bob",
	}
	for _, tk := range []*store.Task{customWorking, customPaused} {
		if err := d.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.Title, err)
		}
	}

	ids, err := d.ClearExpiredTaskLeases(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClearExpiredTaskLeases: %v", err)
	}
	if len(ids) != 1 || ids[0] != customWorking.ID {
		t.Fatalf("expected cleared=[%s], got %v", customWorking.ID, ids)
	}

	gotW, _ := d.GetTask(ctx, customWorking.ID)
	if gotW.AssigneeSessionID != "" {
		t.Errorf("custom working zombie must be reclaimed, got assignee %q", gotW.AssigneeSessionID)
	}
	gotP, _ := d.GetTask(ctx, customPaused.ID)
	if gotP.AssigneeSessionID != "bob" {
		t.Errorf("custom non-working (paused) row must be left alone, got assignee %q", gotP.AssigneeSessionID)
	}
}

// TestClearLeasesReturnedIDsAreAuthoritative pins the SELECT+UPDATE-in-one-
// tx fix: the documented contract is that the returned id slice is
// authoritative — every id MUST name a row that was actually cleared
// (assignee nulled + lease nulled), and no still-leased row may appear.
// Before the fix the SELECT and UPDATE ran as two separate statements, so
// a heartbeat could extend a lease between them: the SELECT returned an id
// the UPDATE then skipped, and the service layer spuriously demoted a live
// task. Running both in one transaction makes the snapshot the SELECT
// reads identical to the snapshot the UPDATE mutates, so the invariant
// "every returned id is genuinely cleared" holds. This test asserts that
// invariant directly across a mixed population for both clear paths.
func TestClearLeasesReturnedIDsAreAuthoritative(t *testing.T) {
	ctx := context.Background()

	// assertCleared: every id in the slice names a row whose assignee +
	// lease are now nulled. Catches a lying authoritative id set.
	assertCleared := func(t *testing.T, d *DB, ids []string) {
		t.Helper()
		for _, id := range ids {
			got, err := d.GetTask(ctx, id)
			if err != nil {
				t.Fatalf("GetTask(%s): %v", id, err)
			}
			if got.AssigneeSessionID != "" {
				t.Errorf("returned id %s still has assignee %q — not actually cleared", id, got.AssigneeSessionID)
			}
			if got.LeaseExpiresAt != nil {
				t.Errorf("returned id %s still has lease %v — not actually cleared", id, got.LeaseExpiresAt)
			}
		}
	}

	t.Run("expired", func(t *testing.T) {
		d := newMemDB(t)
		wsID := seedWorkspace(t, d, "ws-auth-exp")
		past := time.Now().UTC().Add(-1 * time.Minute)
		future := time.Now().UTC().Add(5 * time.Minute)

		expired := &store.Task{
			WorkspaceID: wsID, Title: "expired", Status: "doing",
			AssigneeSessionID: "alice", LeaseExpiresAt: &past,
		}
		zombie := &store.Task{
			WorkspaceID: wsID, Title: "zombie", Status: "doing",
			AssigneeSessionID: "carol",
		}
		live := &store.Task{
			WorkspaceID: wsID, Title: "live", Status: "doing",
			AssigneeSessionID: "bob", LeaseExpiresAt: &future,
		}
		for _, tk := range []*store.Task{expired, zombie, live} {
			if err := d.CreateTask(ctx, tk); err != nil {
				t.Fatalf("CreateTask %s: %v", tk.Title, err)
			}
		}

		ids, err := d.ClearExpiredTaskLeases(ctx, time.Now().UTC())
		if err != nil {
			t.Fatalf("ClearExpiredTaskLeases: %v", err)
		}
		if len(ids) != 2 {
			t.Fatalf("expected 2 reclaimed (expired+zombie), got %v", ids)
		}
		assertCleared(t, d, ids)

		// Live row must be untouched and must NOT appear in ids.
		for _, id := range ids {
			if id == live.ID {
				t.Fatalf("live row %s wrongly reported cleared", live.ID)
			}
		}
		gotLive, _ := d.GetTask(ctx, live.ID)
		if gotLive.AssigneeSessionID != "bob" || gotLive.LeaseExpiresAt == nil {
			t.Fatalf("live row corrupted: assignee=%q lease=%v", gotLive.AssigneeSessionID, gotLive.LeaseExpiresAt)
		}
	})

	t.Run("session", func(t *testing.T) {
		d := newMemDB(t)
		wsID := seedWorkspace(t, d, "ws-auth-sess")
		future := time.Now().UTC().Add(5 * time.Minute)

		aliceLeased := &store.Task{
			WorkspaceID: wsID, Title: "alice-leased", Status: "doing",
			AssigneeSessionID: "alice", LeaseExpiresAt: &future,
		}
		aliceZombie := &store.Task{
			WorkspaceID: wsID, Title: "alice-zombie", Status: "doing",
			AssigneeSessionID: "alice",
		}
		bobLeased := &store.Task{
			WorkspaceID: wsID, Title: "bob-leased", Status: "doing",
			AssigneeSessionID: "bob", LeaseExpiresAt: &future,
		}
		for _, tk := range []*store.Task{aliceLeased, aliceZombie, bobLeased} {
			if err := d.CreateTask(ctx, tk); err != nil {
				t.Fatalf("CreateTask %s: %v", tk.Title, err)
			}
		}

		ids, err := d.ClearSessionTaskLeases(ctx, "alice")
		if err != nil {
			t.Fatalf("ClearSessionTaskLeases: %v", err)
		}
		if len(ids) != 2 {
			t.Fatalf("expected 2 reclaimed for alice, got %v", ids)
		}
		assertCleared(t, d, ids)

		for _, id := range ids {
			if id == bobLeased.ID {
				t.Fatalf("bob's row %s wrongly reported cleared for session alice", bobLeased.ID)
			}
		}
		gotBob, _ := d.GetTask(ctx, bobLeased.ID)
		if gotBob.AssigneeSessionID != "bob" || gotBob.LeaseExpiresAt == nil {
			t.Fatalf("bob's row corrupted: assignee=%q lease=%v", gotBob.AssigneeSessionID, gotBob.LeaseExpiresAt)
		}
	})
}

func TestTaskOfferDedupesOnEnvelopeNonce(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	offer := &store.TaskOffer{
		RemoteTaskID:      "remote-1",
		FromPeerID:        "peer-A",
		ToPeerID:          "self",
		RemoteWorkspaceID: "ws-A",
		Title:             "Sample",
		EnvelopeNonce:     "nonce-1",
		Direction:         "incoming",
	}
	if err := d.CreateTaskOffer(ctx, offer); err != nil {
		t.Fatalf("CreateTaskOffer first: %v", err)
	}
	// Replay the SAME envelope — should be a no-op, not an error.
	dup := *offer
	dup.ID = ""
	if err := d.CreateTaskOffer(ctx, &dup); err != nil {
		t.Fatalf("CreateTaskOffer replay should be idempotent, got: %v", err)
	}
	rows, _ := d.ListTaskOffers(ctx, store.TaskOfferFilter{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after dedup, got %d", len(rows))
	}
}

func TestRewriteFTS5ErrorMapsKnownPatterns(t *testing.T) {
	cases := []struct {
		name      string
		in        error
		query     string
		wantCode  string
		wantField string
	}{
		{
			name:      "no such column → fts5_reserved_syntax",
			in:        errors.New("SQL logic error: no such column: test (1)"),
			query:     "SSE live-test probe",
			wantCode:  "fts5_reserved_syntax",
			wantField: "q",
		},
		{
			name:      "fts5 syntax error → fts5_syntax_error",
			in:        errors.New(`fts5: syntax error near "OR"`),
			query:     "OR OR OR",
			wantCode:  "fts5_syntax_error",
			wantField: "q",
		},
		{
			name:      "generic SQL error preserved",
			in:        errors.New("database is locked"),
			query:     "anything",
			wantCode:  "", // not a FieldError → falls through
			wantField: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rewriteFTS5Error(c.in, c.query)
			var fe *store.FieldError
			if c.wantCode == "" {
				if errors.As(got, &fe) {
					t.Fatalf("expected fallthrough (no FieldError), got %+v", fe)
				}
				return
			}
			if !errors.As(got, &fe) {
				t.Fatalf("expected *store.FieldError, got %T (%v)", got, got)
			}
			if fe.Code != c.wantCode {
				t.Errorf("code = %q want %q", fe.Code, c.wantCode)
			}
			if fe.Field != c.wantField {
				t.Errorf("field = %q want %q", fe.Field, c.wantField)
			}
			if fe.Value != c.query {
				t.Errorf("value = %q want %q", fe.Value, c.query)
			}
			if fe.Hint == "" {
				t.Error("expected non-empty hint")
			}
			// Unwrap chain must preserve the original cause for debugging.
			if !errors.Is(got, c.in) {
				t.Errorf("expected errors.Is to find original cause via Unwrap chain")
			}
		})
	}
}

func TestRewriteFTS5ErrorTruncatesHugeValue(t *testing.T) {
	huge := strings.Repeat("a", 2000)
	got := rewriteFTS5Error(errors.New("no such column: foo"), huge)
	var fe *store.FieldError
	if !errors.As(got, &fe) {
		t.Fatalf("expected FieldError, got %v", got)
	}
	if len(fe.Value) > 600 {
		t.Errorf("expected truncated value, got %d chars", len(fe.Value))
	}
	if !strings.HasSuffix(fe.Value, "...(truncated)") {
		t.Errorf("expected truncation suffix, got tail %q", fe.Value[len(fe.Value)-20:])
	}
}

func TestEscapeFTS5Query(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"simple", `"simple"`},
		{"two terms", `"two" "terms"`},
		{"SSE live-test probe", `"SSE" "live-test" "probe"`},
		{`task:01KSG... feat/foo`, `"task:01KSG..." "feat/foo"`},
		{`with "quote"`, `"with" """quote"""`},
		{"AND OR NOT NEAR", `"AND" "OR" "NOT" "NEAR"`},
	}
	for _, c := range cases {
		got := escapeFTS5Query(c.in)
		if got != c.want {
			t.Errorf("escapeFTS5Query(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
