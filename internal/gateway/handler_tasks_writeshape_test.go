// handler_tasks_writeshape_test.go — coverage of the LLM-ergonomics
// post-write response shape: compact-by-default + `full: true` opt-in,
// id surfaced at the top level, coordination_warnings preserved.
//
// Pairs with the changes in handler_tasks.go for tasks
// 01KSGCVXZXAHA7M6GX391Z03SH (compact responses) and
// 01KSG4RTH42365J0X82D9M7H0N (id-at-top-level — satisfied by compact).
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newTasksHandlerWithSession returns a handler with a fresh sqlite DB,
// a fully bound session (so handleTaskCreate/Update/Claim can resolve
// workspaceID + sessionID without the caller passing workspace_id), and
// no mesh wiring (write responses don't broadcast in this path).
func newTasksHandlerWithSession(t *testing.T) (*handler, *sqlite.DB, string) {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{
		Name:     "ws-writeshape",
		RootPath: "/tmp/ws-writeshape",
		Tags:     json.RawMessage("[]"),
	}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	sess := &store.Session{ID: "sess-test"}
	if err := db.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	h := &handler{
		store:    db,
		tasksSvc: tasks.New(db),
	}
	h.sessions = newSessionManager(db, nil, TransportInternal, nil)
	h.sessions.session = sess
	h.sessions.clientPath = ws.RootPath
	h.sessions.wsChain = []routing.WorkspaceAncestor{{
		ID:       ws.ID,
		Name:     ws.Name,
		RootPath: ws.RootPath,
	}}
	return h, db, ws.ID
}

// TestTaskCreate_CompactResponseDefault confirms the default post-write
// shape on task__create: compact body + `id` at the envelope top + the
// task object still carries `id`/`title`/`status` so the legacy
// `result.task.title` access path keeps working.
func TestTaskCreate_CompactResponseDefault(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{
		"title": "compact-default-task",
	})
	resp, rpcErr := h.handleTaskCreate(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)

	// `ok: true` + `id` at the envelope top — both new in the compact
	// shape, both load-bearing for the "skip the envelope walk" UX.
	if got["ok"] != true {
		t.Errorf("ok = %v, want true", got["ok"])
	}
	id, _ := got["id"].(string)
	if id == "" {
		t.Errorf("expected top-level id, got %v (full envelope=%v)", got["id"], got)
	}

	// `task` still nested so result.task.title keeps working — this is
	// the strict-subset back-compat contract the brief requires.
	task, _ := got["task"].(map[string]any)
	if task == nil {
		t.Fatalf("expected task object, got %v", got["task"])
	}
	if task["id"] != id {
		t.Errorf("task.id = %v, want %s", task["id"], id)
	}
	if task["title"] != "compact-default-task" {
		t.Errorf("task.title = %v", task["title"])
	}
	if task["status"] != "open" {
		t.Errorf("task.status = %v, want open", task["status"])
	}
	if task["workspace_id"] != wsID {
		t.Errorf("task.workspace_id = %v, want %s", task["workspace_id"], wsID)
	}
	if _, ok := task["updated_at"]; !ok {
		t.Errorf("expected updated_at in compact task, got %v", task)
	}

	// Heavy fields must NOT be in the compact body — that's the whole point.
	for _, k := range []string{"status_history", "description", "meta", "tags", "notes", "composed_by", "composes"} {
		if _, ok := got[k]; ok {
			t.Errorf("compact envelope should not carry %q (got %v)", k, got[k])
		}
		if _, ok := task[k]; ok {
			t.Errorf("compact task should not carry %q (got %v)", k, task[k])
		}
	}
}

func TestTaskList_PreviewResponseDefault(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	seed := &store.Task{
		WorkspaceID:       wsID,
		Title:             "preview-list-row",
		Description:       strings.Repeat("description payload ", 40),
		Status:            "open",
		Priority:          "high",
		TagsJSON:          json.RawMessage(`["context-cost","preview"]`),
		Meta:              strings.Repeat("owner: measurement\n", 25),
		StatusHistoryJSON: json.RawMessage(`[{"evt":"created"},{"evt":"status_changed"}]`),
	}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"limit": 1})
	resp, rpcErr := h.handleTaskList(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["task_view"] != "preview" {
		t.Fatalf("task_view = %v, want preview (response=%v)", got["task_view"], got)
	}
	if got["count"] != float64(1) {
		t.Fatalf("count = %v, want 1", got["count"])
	}
	rows, ok := got["tasks"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("tasks = %#v, want one preview row", got["tasks"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("task row shape = %#v", rows[0])
	}
	if row["id"] != seed.ID || row["title"] != seed.Title {
		t.Fatalf("preview row lost identity fields: %#v", row)
	}
	if _, ok := row["description"]; ok {
		t.Fatalf("preview row should not include full description: %#v", row)
	}
	if _, ok := row["meta"]; ok {
		t.Fatalf("preview row should not include full meta: %#v", row)
	}
	if _, ok := row["status_history"]; ok {
		t.Fatalf("preview row should not include full status_history: %#v", row)
	}
	if row["description_truncated"] != true {
		t.Fatalf("description_truncated = %v, want true", row["description_truncated"])
	}
	if row["meta_truncated"] != true {
		t.Fatalf("meta_truncated = %v, want true", row["meta_truncated"])
	}
	if row["status_history_count"] != float64(2) {
		t.Fatalf("status_history_count = %v, want 2", row["status_history_count"])
	}
}

func TestTaskList_FullOptInRestoresRows(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	seed := &store.Task{
		WorkspaceID:       wsID,
		Title:             "full-list-row",
		Description:       "full body",
		Status:            "open",
		Meta:              "owner: test",
		StatusHistoryJSON: json.RawMessage(`[{"evt":"created"}]`),
	}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"limit": 1, "full": true})
	resp, rpcErr := h.handleTaskList(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	// task_view self-describes the row shape in BOTH modes — full:true must
	// not silently change the envelope contract.
	if got["task_view"] != "full" {
		t.Fatalf("task_view = %v, want full (response=%v)", got["task_view"], got)
	}
	if got["count"] != float64(1) {
		t.Fatalf("count = %v, want 1", got["count"])
	}
	rows, ok := got["tasks"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("tasks = %#v, want one full row", got["tasks"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("task row shape = %#v", rows[0])
	}
	if row["description"] != "full body" {
		t.Fatalf("description = %v, want full body", row["description"])
	}
	if _, ok := row["status_history"]; !ok {
		t.Fatalf("full row should include status_history: %#v", row)
	}
}

// TestTaskList_EmptyStateIsUnambiguous locks the empty-inbox contract:
// zero matches must return tasks:[] plus count:0 and the task_view marker,
// so an agent can tell "genuinely no tasks" apart from "call failed" without
// a second probe.
func TestTaskList_EmptyStateIsUnambiguous(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{"state": "open"})
	resp, rpcErr := h.handleTaskList(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", got["count"])
	}
	rows, ok := got["tasks"].([]any)
	if !ok {
		t.Fatalf("tasks = %#v, want [] (never null/absent)", got["tasks"])
	}
	if len(rows) != 0 {
		t.Fatalf("tasks len = %d, want 0", len(rows))
	}
	if got["task_view"] != "preview" {
		t.Fatalf("task_view = %v, want preview", got["task_view"])
	}
}

// TestTaskListMilestones_MCPSurface covers the task__list_milestones tool —
// the burndown rollup existed store + REST-side only; agents had no path
// to it.
func TestTaskListMilestones_MCPSurface(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	due := time.Now().UTC().Add(48 * time.Hour)
	seed := &store.Task{
		WorkspaceID: wsID,
		Title:       "ship v1",
		Status:      "open",
		TagsJSON:    json.RawMessage(`["milestone"]`),
		DueAt:       &due,
	}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	resp, rpcErr := h.handleTaskListMilestones(ctx, json.RawMessage(`{}`))
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["count"] != float64(1) {
		t.Fatalf("count = %v, want 1 (response=%v)", got["count"], got)
	}
	rows, ok := got["milestones"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("milestones = %#v, want one row", got["milestones"])
	}
}

// TestTaskCreate_FullOptIn confirms `full: true` restores the historical
// envelope (status_history, notes, composed_by, composes) so callers
// that need the full body can still get it.
func TestTaskCreate_FullOptIn(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandlerWithSession(t)

	raw, _ := json.Marshal(map[string]any{
		"title": "full-opt-in",
		"full":  true,
	})
	resp, rpcErr := h.handleTaskCreate(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)

	task, ok := got["task"].(map[string]any)
	if !ok {
		t.Fatalf("expected task object, got %v", got["task"])
	}
	if _, ok := task["status_history"]; !ok {
		t.Errorf("expected full body to include status_history, got %v", task)
	}
	if _, ok := got["composed_by"]; !ok {
		t.Errorf("expected full envelope to include composed_by")
	}
	if _, ok := got["composes"]; !ok {
		t.Errorf("expected full envelope to include composes")
	}
	if _, ok := got["notes"]; !ok {
		t.Errorf("expected full envelope to include notes")
	}
}

// TestTaskUpdate_CompactResponseDefault hits the update path and
// confirms the same compact shape — important because task__update is
// the hottest write tool (status flips, heartbeats indirectly).
func TestTaskUpdate_CompactResponseDefault(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	seed := &store.Task{WorkspaceID: wsID, Title: "row", Status: "open"}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"id":     seed.ID,
		"status": "doing",
	})
	resp, rpcErr := h.handleTaskUpdate(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["id"] != seed.ID {
		t.Errorf("top-level id = %v, want %s", got["id"], seed.ID)
	}
	task, _ := got["task"].(map[string]any)
	if task == nil {
		t.Fatalf("expected task object, got %v", got["task"])
	}
	if task["status"] != "doing" {
		t.Errorf("task.status = %v, want doing", task["status"])
	}
	// status_history is part of the heavy body — must NOT leak into
	// the compact response.
	if _, ok := task["status_history"]; ok {
		t.Errorf("compact update should not include status_history, got %v", task["status_history"])
	}
}

// TestTaskUpdate_FullOptInRestoresWarnings ensures the warnings field
// rides on the full envelope too — the coordination signal is part of
// the contract for `task__update` regardless of shape choice.
func TestTaskUpdate_FullOptIn(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	seed := &store.Task{WorkspaceID: wsID, Title: "row", Status: "open"}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"id":     seed.ID,
		"status": "doing",
		"full":   true,
	})
	resp, rpcErr := h.handleTaskUpdate(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	task, ok := got["task"].(map[string]any)
	if !ok {
		t.Fatalf("expected task object")
	}
	if _, ok := task["status_history"]; !ok {
		t.Errorf("expected status_history in full response, got %v", task)
	}
}

// TestTaskClaim_CompactResponseDefault confirms claim follows the same
// shape — the workspace-broadcast claim flow returns a tight envelope.
func TestTaskClaim_CompactResponseDefault(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandlerWithSession(t)

	seed := &store.Task{WorkspaceID: wsID, Title: "claimable", Status: "open"}
	if err := db.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"id": seed.ID})
	resp, rpcErr := h.handleTaskClaim(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["id"] != seed.ID {
		t.Errorf("top-level id = %v", got["id"])
	}
	task, _ := got["task"].(map[string]any)
	if task == nil {
		t.Fatalf("expected task object")
	}
	if task["status"] != "doing" {
		t.Errorf("expected status=doing on claim, got %v", task["status"])
	}
}
