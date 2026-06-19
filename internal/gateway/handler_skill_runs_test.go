// handler_skill_runs_test.go — coverage of the W2 skill telemetry
// tools (skill__run_start / __phase / __run_complete). Exercises both
// the bare run path (no task epic) and the auto-epic path triggered
// when `phases` is supplied to skill__run_start.
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

func newSkillRunsHandler(t *testing.T) (*handler, *sqlite.DB, string) {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{
		Name:     "ws-skill-runs",
		RootPath: "/tmp/ws-skill-runs",
		Tags:     json.RawMessage("[]"),
	}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	h := &handler{
		store:    db,
		tasksSvc: tasks.New(db),
	}
	h.sessions = newSessionManager(db, nil, TransportInternal, nil)
	// Plant the workspace chain directly — the test bypasses the
	// session-bind path that would normally resolve a chain from
	// clientPath. Without this, workspaceID() returns "" and
	// skill__run_start refuses to record.
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: ws.ID, Name: ws.Name, RootPath: ws.RootPath},
	}
	return h, db, ws.ID
}

func TestSkillRunStart_BareRun(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newSkillRunsHandler(t)

	raw, _ := json.Marshal(map[string]any{
		"skill":    "mcplexer-features",
		"version":  3,
		"metadata": map[string]any{"agent_name": "test"},
	})
	resp, rpcErr := h.handleSkillRunStart(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc: %v", rpcErr)
	}
	got := unwrapToolText(t, resp)
	runID, _ := got["run_id"].(string)
	if runID == "" {
		t.Fatalf("expected run_id, got %+v", got)
	}
	if _, ok := got["task_epic_id"]; ok {
		t.Fatalf("did not expect task_epic_id without phases, got %+v", got)
	}

	row, err := db.GetSkillRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRun: %v", err)
	}
	if row.WorkspaceID != wsID {
		t.Errorf("workspace_id = %s, want %s", row.WorkspaceID, wsID)
	}
	if row.SkillName != "mcplexer-features" || row.SkillVersion != 3 {
		t.Errorf("skill metadata mismatch: %+v", row)
	}
	if row.Outcome != store.SkillRunOutcomeRunning {
		t.Errorf("outcome = %s, want running", row.Outcome)
	}
}

func TestSkillRunStart_WithPhases_AutoCreatesEpic(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newSkillRunsHandler(t)

	raw, _ := json.Marshal(map[string]any{
		"skill":   "extract",
		"version": 1,
		"phases":  []string{"discover", "validate", "publish"},
	})
	resp, rpcErr := h.handleSkillRunStart(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc: %v", rpcErr)
	}
	got := unwrapToolText(t, resp)
	runID, _ := got["run_id"].(string)
	epicID, _ := got["task_epic_id"].(string)
	if runID == "" || epicID == "" {
		t.Fatalf("expected run_id+task_epic_id, got %+v", got)
	}
	children, _ := got["child_task_ids"].([]any)
	if len(children) != 3 {
		t.Fatalf("expected 3 child task ids, got %d (%v)", len(children), children)
	}

	row, err := db.GetSkillRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRun: %v", err)
	}
	if row.TaskEpicID != epicID {
		t.Errorf("task_epic_id not persisted: row=%s resp=%s", row.TaskEpicID, epicID)
	}

	// Confirm the epic was created in tasks.
	epic, err := h.tasksSvc.Get(ctx, wsID, epicID)
	if err != nil {
		t.Fatalf("Get epic: %v", err)
	}
	if epic.Title == "" {
		t.Errorf("epic missing title")
	}
}

func TestSkillPhase_AppendsAndMirrorsToTask(t *testing.T) {
	ctx := context.Background()
	h, db, _ := newSkillRunsHandler(t)

	startRaw, _ := json.Marshal(map[string]any{
		"skill":  "extract",
		"phases": []string{"discover", "publish"},
	})
	startResp, _ := h.handleSkillRunStart(ctx, startRaw)
	start := unwrapToolText(t, startResp)
	runID := start["run_id"].(string)

	// Start the discover phase.
	phaseRaw, _ := json.Marshal(map[string]any{
		"run_id": runID, "phase": "discover", "event": "started",
	})
	if _, rpcErr := h.handleSkillPhase(ctx, phaseRaw); rpcErr != nil {
		t.Fatalf("phase started rpc: %v", rpcErr)
	}
	// Complete it.
	phaseRaw2, _ := json.Marshal(map[string]any{
		"run_id": runID, "phase": "discover", "event": "completed",
		"note": "extracted 14 fields",
	})
	if _, rpcErr := h.handleSkillPhase(ctx, phaseRaw2); rpcErr != nil {
		t.Fatalf("phase completed rpc: %v", rpcErr)
	}

	row, err := db.GetSkillRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRun: %v", err)
	}
	var events []store.SkillRunPhaseEvent
	_ = json.Unmarshal(row.PhasesJSON, &events)
	if len(events) != 2 {
		t.Fatalf("expected 2 phase events, got %d (%+v)", len(events), events)
	}
	if events[0].Phase != "discover" || events[0].Event != "started" {
		t.Errorf("first event mismatch: %+v", events[0])
	}
	if events[1].Event != "completed" {
		t.Errorf("second event mismatch: %+v", events[1])
	}
}

func TestSkillRunComplete_StampsOutcomeAndClosesEpic(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newSkillRunsHandler(t)

	startRaw, _ := json.Marshal(map[string]any{
		"skill":  "extract",
		"phases": []string{"a", "b"},
	})
	startResp, _ := h.handleSkillRunStart(ctx, startRaw)
	start := unwrapToolText(t, startResp)
	runID := start["run_id"].(string)
	epicID := start["task_epic_id"].(string)

	completeRaw, _ := json.Marshal(map[string]any{
		"run_id":  runID,
		"outcome": "success",
		"summary": "all good",
	})
	if _, rpcErr := h.handleSkillRunComplete(ctx, completeRaw); rpcErr != nil {
		t.Fatalf("complete rpc: %v", rpcErr)
	}

	row, err := db.GetSkillRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetSkillRun: %v", err)
	}
	if row.Outcome != store.SkillRunOutcomeSuccess {
		t.Errorf("outcome = %s, want success", row.Outcome)
	}
	if row.CompletedAt == nil {
		t.Errorf("completed_at not stamped")
	}

	epic, err := h.tasksSvc.Get(ctx, wsID, epicID)
	if err != nil {
		t.Fatalf("Get epic: %v", err)
	}
	if epic.Status != "done" {
		t.Errorf("epic status = %s, want done", epic.Status)
	}
	if epic.ClosedAt == nil {
		t.Errorf("epic closed_at not stamped")
	}
}

func TestSkillRunStart_NoWorkspaceFails(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newSkillRunsHandler(t)
	// Wipe the wsChain so workspaceID() returns "".
	h.sessions.wsChain = nil

	raw, _ := json.Marshal(map[string]any{"skill": "x"})
	resp, rpcErr := h.handleSkillRunStart(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("expected friendly error result, got rpc err: %v", rpcErr)
	}
	if !isErrResult(resp) {
		t.Errorf("expected isError envelope, got %s", string(resp))
	}
}

// unwrapToolText parses a marshalToolResult envelope where the Content[0].Text
// is a JSON object string. Mirrors unwrapResult but tolerates the case where
// the text itself is plain (no JSON) — returns an empty map then.
func unwrapToolText(t *testing.T, raw json.RawMessage) map[string]any {
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
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal([]byte(env.Content[0].Text), &out)
	return out
}
