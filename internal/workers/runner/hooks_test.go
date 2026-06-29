package runner_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// hookVerdictSentinel mirrors the unexported runner constant — it is a wire
// contract between the injected abort() helper and the runner's parser.
const hookVerdictSentinel = "@@MCPLEXER_HOOK_VERDICT@@"

// hookEnvelope builds the MCP tools/call result envelope an mcpx__execute_code
// dispatch returns, so a fakeDispatcher can stand in for a real hook run.
func hookEnvelope(t *testing.T, printOut string, isErr bool) string {
	t.Helper()
	content := make([]map[string]string, 0, 2)
	if printOut != "" {
		content = append(content, map[string]string{"type": "text", "text": printOut})
	}
	if isErr {
		content = append(content, map[string]string{"type": "text", "text": "Error: thrown by script"})
	}
	b, err := json.Marshal(map[string]any{"content": content, "isError": isErr})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return string(b)
}

func abortVerdict(reason string) string {
	return hookVerdictSentinel + `{"action":"abort","reason":"` + reason + `"}`
}

func countExecuteCode(reqs []runner.ToolCallRequest) int {
	n := 0
	for _, r := range reqs {
		if r.Name == "mcpx__execute_code" {
			n++
		}
	}
	return n
}

func TestPreExecuteHook_BlocksRunBeforeModelSpend(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PreExecuteScript = `abort("gate closed")`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "should never run", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"mcpx__execute_code": {OutputJSON: hookEnvelope(t, abortVerdict("gate closed"), true), IsError: true},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != runner.StatusBlocked {
		t.Fatalf("status = %q, want blocked", run.Status)
	}
	if !strings.Contains(run.Error, "gate closed") {
		t.Fatalf("error = %q, want it to mention the abort reason", run.Error)
	}
	adapter.mu.Lock()
	requests := len(adapter.requests)
	adapter.mu.Unlock()
	if requests != 0 {
		t.Fatalf("adapter received %d requests, want 0 (gate must block before any model spend)", requests)
	}
	if got := countExecuteCode(disp.dispatched); got != 1 {
		t.Fatalf("execute_code dispatches = %d, want 1 (the pre-hook)", got)
	}
}

func TestPreExecuteHook_ProceedsOnCleanRun(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PreExecuteScript = `print("checks passed")`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "did the work", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"mcpx__execute_code": {OutputJSON: hookEnvelope(t, "checks passed", false)},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if run.OutputText != "did the work" {
		t.Fatalf("output = %q, want the model output", run.OutputText)
	}
	adapter.mu.Lock()
	requests := len(adapter.requests)
	adapter.mu.Unlock()
	if requests != 1 {
		t.Fatalf("adapter requests = %d, want 1 (run proceeds after clean gate)", requests)
	}
}

func TestPreExecuteHook_BlocksFailClosedOnDispatchError(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PreExecuteScript = `print("never observed — dispatch fails")`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "should never run", StopReason: models.StopEndTurn},
	}}
	// A dispatcher transport error (e.g. sandbox wiring down) must fail
	// CLOSED — a gate we cannot evaluate blocks the run.
	disp := &fakeDispatcher{err: context.DeadlineExceeded}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusBlocked {
		t.Fatalf("status = %q, want blocked (fail-closed)", run.Status)
	}
	adapter.mu.Lock()
	requests := len(adapter.requests)
	adapter.mu.Unlock()
	if requests != 0 {
		t.Fatalf("adapter requests = %d, want 0", requests)
	}
}

func TestPreExecuteHook_EmptyScriptSkipsHook(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID) // no PreExecuteScript / PostExecuteScript
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "ran normally", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if got := countExecuteCode(disp.dispatched); got != 0 {
		t.Fatalf("execute_code dispatches = %d, want 0 (no hooks configured)", got)
	}
}

func TestPostExecuteHook_RejectsSuccessfulOutput(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PostExecuteScript = `abort("output failed validation")`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "draft output", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"mcpx__execute_code": {OutputJSON: hookEnvelope(t, abortVerdict("output failed validation"), true), IsError: true},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusBlocked {
		t.Fatalf("status = %q, want blocked (post-hook rejected output)", run.Status)
	}
	if !strings.Contains(run.Error, "output failed validation") {
		t.Fatalf("error = %q, want the rejection reason", run.Error)
	}
	// The model DID run (post-hook is after the loop).
	adapter.mu.Lock()
	requests := len(adapter.requests)
	adapter.mu.Unlock()
	if requests != 1 {
		t.Fatalf("adapter requests = %d, want 1", requests)
	}
}

func TestPostExecuteHook_CleanRunStaysSuccess(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PostExecuteScript = `print("output looks good")`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "final answer", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"mcpx__execute_code": {OutputJSON: hookEnvelope(t, "output looks good", false)},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if run.OutputText != "final answer" {
		t.Fatalf("output = %q", run.OutputText)
	}
}

func TestPostExecuteHook_DoesNotUpgradeFailure(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PostExecuteScript = `abort("never mind")`
	createWorker(t, db, w)

	// Model send fails → the run is a failure before finalize. A post-hook
	// block must NOT relabel a failure as "blocked" — failures keep their
	// (more specific) terminal status; the hook only annotates.
	adapter := &fakeAdapter{err: context.Canceled}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"mcpx__execute_code": {OutputJSON: hookEnvelope(t, abortVerdict("never mind"), true), IsError: true},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status == runner.StatusBlocked {
		t.Fatalf("status = blocked, want the original failure status preserved")
	}
}
