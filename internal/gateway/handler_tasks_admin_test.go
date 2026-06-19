// handler_tasks_admin_test.go — coverage of Phase 5 admin tools:
//   - task__consolidate_statuses          (dry-run by default)
//   - task__apply_status_consolidation    (writes vocab + rewrites tasks)
//   - task__rebind_peer                   (re-pair recovery)
//
// The CWD-gate is exercised at the FilterAdminTools / IsAdminTool layer
// (see TestTaskAdminGated below). End-to-end dispatch goes through a
// real Service backed by an in-memory sqlite DB so heuristics + writes
// are tested in one pass.
package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newTasksHandler returns a barebones handler wired to a fresh
// in-memory sqlite DB + tasks.Service, with no mesh / approval /
// adapters. Suitable for exercising the admin handlers directly.
func newTasksHandler(t *testing.T) (*handler, *sqlite.DB, string) {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{
		Name:     "ws-admin",
		RootPath: "/tmp/ws-admin",
		Tags:     json.RawMessage("[]"),
	}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	h := &handler{
		store:    db,
		tasksSvc: tasks.New(db),
	}
	// Bind a session so resolveAdminWorkspaceID has a default workspace
	// and the audit + history paths fire with a known session id.
	sess := &store.Session{ID: "sess-task-admin", WorkspaceID: &ws.ID}
	if err := db.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("create session: %v", err)
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

func TestTaskAdmin_ConsolidateStatusesProposesHeuristicMerges(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandler(t)

	// Seed tasks with a mix of canonical + alias statuses.
	for _, s := range []string{"open", "open", "in-progress", "wip", "finished", "done"} {
		if err := db.CreateTask(ctx, &store.Task{
			WorkspaceID: wsID, Title: "t", Status: s,
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	raw, err := json.Marshal(map[string]any{"workspace": wsID})
	if err != nil {
		t.Fatal(err)
	}
	resp, rpcErr := h.handleTaskConsolidateStatuses(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if got["workspace_id"] != wsID {
		t.Errorf("workspace_id = %v, want %s", got["workspace_id"], wsID)
	}
	counts, _ := got["counts"].(map[string]any)
	if int(counts["open"].(float64)) != 2 {
		t.Errorf("counts[open] = %v, want 2", counts["open"])
	}
	merges, _ := got["merges"].([]any)
	// Expect: in-progress -> doing, wip -> doing, finished -> done.
	found := map[string]string{}
	for _, m := range merges {
		mm := m.(map[string]any)
		found[mm["from"].(string)] = mm["to"].(string)
	}
	if found["in-progress"] != "doing" {
		t.Errorf("expected in-progress -> doing, got %v", found)
	}
	if found["wip"] != "doing" {
		t.Errorf("expected wip -> doing, got %v", found)
	}
	if found["finished"] != "done" {
		t.Errorf("expected finished -> done, got %v", found)
	}
}

func TestTaskAdmin_ApplyStatusConsolidationRewritesTasks(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandler(t)

	t1 := &store.Task{WorkspaceID: wsID, Title: "a", Status: "in-progress"}
	t2 := &store.Task{WorkspaceID: wsID, Title: "b", Status: "wip"}
	t3 := &store.Task{WorkspaceID: wsID, Title: "c", Status: "doing"}
	for _, x := range []*store.Task{t1, t2, t3} {
		if err := db.CreateTask(ctx, x); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	plan := map[string]any{
		"workspace": wsID,
		"plan": map[string]any{
			"merges": []map[string]any{
				{"from": "in-progress", "to": "doing", "terminal": false},
				{"from": "wip", "to": "doing", "terminal": false},
			},
		},
	}
	raw, _ := json.Marshal(plan)
	resp, rpcErr := h.handleTaskApplyStatusConsolidation(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	updated, _ := got["tasks_updated"].(map[string]any)
	if int(updated["doing"].(float64)) != 2 {
		t.Errorf("tasks_updated[doing] = %v, want 2", updated["doing"])
	}

	for _, id := range []string{t1.ID, t2.ID} {
		got, err := db.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		if got.Status != "doing" {
			t.Errorf("task %s status = %q, want doing", id, got.Status)
		}
	}
}

func TestTaskAdmin_ApplyRejectsEmptyPlan(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandler(t)
	raw, _ := json.Marshal(map[string]any{
		"workspace": wsID,
		"plan":      map[string]any{"merges": []any{}},
	})
	resp, _ := h.handleTaskApplyStatusConsolidation(ctx, raw)
	if resp == nil {
		t.Fatal("expected an error result, got nil")
	}
	if !isErrResult(resp) {
		t.Errorf("expected isError=true result, got %s", string(resp))
	}
}

func TestTaskAdmin_RebindPeerRewritesAcrossTables(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandler(t)

	// Seed one task assigned to peer-A and a binding for peer-A.
	if err := db.CreateTask(ctx, &store.Task{
		WorkspaceID: wsID, Title: "x",
		AssigneeOriginKind: store.TaskAssigneePeer, AssigneePeerID: "peer-A",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := db.UpsertWorkspacePeerBinding(ctx, &store.WorkspacePeerBinding{
		PeerID: "peer-A", RemoteWorkspaceID: "rw", LocalWorkspaceID: wsID,
	}); err != nil {
		t.Fatalf("UpsertWorkspacePeerBinding: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"old_peer_id": "peer-A",
		"new_peer_id": "peer-B",
	})
	resp, rpcErr := h.handleTaskRebindPeer(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	got := unwrapResult(t, resp)
	if int(got["total"].(float64)) < 2 {
		t.Errorf("expected ≥2 rows rebound, got %v", got["total"])
	}
}

func TestTaskAdmin_RebindPeerRequiresBothIDs(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandler(t)
	for _, args := range []map[string]any{
		{},
		{"old_peer_id": "a"},
		{"new_peer_id": "b"},
		{"old_peer_id": "same", "new_peer_id": "same"},
	} {
		raw, _ := json.Marshal(args)
		_, rpcErr := h.handleTaskRebindPeer(ctx, raw)
		if rpcErr == nil {
			t.Errorf("expected rpc error for %v", args)
		}
	}
}

// TestTaskAdminGated checks the AdminCWDGate hides every Phase 5 task
// admin tool when the agent's CWD is outside the data directory. The
// gate is the primary defence (tools/list filter) AND the secondary
// per-call check (see handler_tools.go:305-).
func TestTaskAdminGated(t *testing.T) {
	tools := []Tool{
		{Name: "task__create"},                     // universal
		{Name: "task__consolidate_statuses"},       // admin
		{Name: "task__apply_status_consolidation"}, // admin
		{Name: "task__rebind_peer"},                // admin
	}

	g := NewAdminCWDGate("/data")

	t.Run("admin CWD sees every task tool", func(t *testing.T) {
		out := g.FilterAdminTools(tools, "/data/sub", nil)
		if len(out) != len(tools) {
			t.Errorf("expected all %d tools visible from admin CWD, got %d", len(tools), len(out))
		}
	})

	t.Run("non-admin CWD hides only admin tools", func(t *testing.T) {
		out := g.FilterAdminTools(tools, "/home/me/project", nil)
		names := map[string]bool{}
		for _, x := range out {
			names[x.Name] = true
		}
		if !names["task__create"] {
			t.Error("task__create (universal) must stay visible")
		}
		for _, gated := range []string{
			"task__consolidate_statuses",
			"task__apply_status_consolidation",
			"task__rebind_peer",
		} {
			if names[gated] {
				t.Errorf("%s must be hidden outside ~/.mcplexer", gated)
			}
			if !IsAdminTool(gated) {
				t.Errorf("IsAdminTool(%q) = false, want true", gated)
			}
		}
	})
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

// unwrapResult parses the MCP CallToolResult shape produced by
// marshalJSONResult — Content[0].Text is JSON-encoded; the outer
// envelope marks isError=false.
func unwrapResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if env.IsError {
		t.Fatalf("expected isError=false, got error result: %s", string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content envelope: %s", string(raw))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(env.Content[0].Text), &out); err != nil {
		t.Fatalf("unwrap content[0]: %v (text=%s)", err, env.Content[0].Text)
	}
	return out
}

func isErrResult(raw json.RawMessage) bool {
	var env struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.IsError
}
