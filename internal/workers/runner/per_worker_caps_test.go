package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// TestRun_PerWorkerMaxOutputTokens — worker.MaxOutputTokens=64 should
// trip the lifetime-output-token cap once cumulative output reaches it.
// We feed the runner a single 128-token response so the cap fires on
// the second iteration (before the next adapter call). The package
// default ceiling stays per-turn so multi-turn fakes don't accidentally
// hit it.
func TestRun_PerWorkerMaxOutputTokens(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxOutputTokens = 64
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		// Turn 1: large tool-call reply (lifetime tokens > cap on next loop iter).
		{
			OutputTokens: 128,
			ToolCalls:    []models.ToolCall{{ID: "t1", Name: "noop"}},
			StopReason:   models.StopToolUse,
		},
		// Turn 2 (should never run — cap trips first).
		{Text: "should not be reached", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "output tokens") {
		t.Fatalf("error = %q, want mention of output tokens", run.Error)
	}
}

// TestRun_PerWorkerMaxToolCalls — worker.MaxToolCalls=1 should trip
// the cap after exactly one tool dispatch. Confirms the per-worker
// override beats the package default (50).
func TestRun_PerWorkerMaxToolCalls(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxToolCalls = 1
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "search"}}, StopReason: models.StopToolUse},
		{ToolCalls: []models.ToolCall{{ID: "t2", Name: "search"}}, StopReason: models.StopToolUse},
		{Text: "never reached", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "tool calls") {
		t.Fatalf("error = %q, want mention of tool calls", run.Error)
	}
}

// TestRun_PerWorkerMaxWallClockSeconds — worker.MaxWallClockSeconds=2
// should trip the wall-clock cap. We drive a fakeClock that ticks 5s
// per call so the second iteration sees elapsed >= 2s and aborts.
func TestRun_PerWorkerMaxWallClockSeconds(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxWallClockSeconds = 2
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t", Name: "noop"}}, StopReason: models.StopToolUse},
		{Text: "never reached", StopReason: models.StopEndTurn},
	}}
	clock := &fakeClock{current: time.Now(), step: 5 * time.Second}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Clock:      clock,
	})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "wall-clock") {
		t.Fatalf("error = %q, want mention of wall-clock", run.Error)
	}
}

// TestRun_PerWorkerMaxInputTokens — worker.MaxInputTokens=10 should
// trip the cap on the second iteration after cumulative input crosses
// 10. A positive worker value overrides the package default.
func TestRun_PerWorkerMaxInputTokens(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxInputTokens = 10
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			InputTokens: 50,
			ToolCalls:   []models.ToolCall{{ID: "t1", Name: "noop"}},
			StopReason:  models.StopToolUse,
		},
		{Text: "never reached", StopReason: models.StopEndTurn},
	}}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	runID, _ := r.Run(context.Background(), w.ID)
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "input tokens") {
		t.Fatalf("error = %q, want mention of input tokens", run.Error)
	}
}

func TestRun_PreSendInputEstimateStopsOversizedPrompt(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PromptTemplate = strings.Repeat("x", 1000)
	w.ParametersJSON = `{}`
	w.MaxInputTokens = 10
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "never reached", StopReason: models.StopEndTurn},
	}}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	runID, _ := r.Run(context.Background(), w.ID)
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "estimated input tokens") {
		t.Fatalf("error = %q, want mention of estimated input tokens", run.Error)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
	}
}

func TestRun_TruncatesLargeToolResultBeforeNextModelSend(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxInputTokens = 1_000_000
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			InputTokens: 10,
			ToolCalls:   []models.ToolCall{{ID: "t1", Name: "large_result"}},
			StopReason:  models.StopToolUse,
		},
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"large_result": {OutputJSON: strings.Repeat("x", 64*1024)},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, _ := r.Run(context.Background(), w.ID)
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success (error=%q)", run.Status, run.Error)
	}
	if len(adapter.requests) < 2 {
		t.Fatalf("adapter requests = %d, want at least 2", len(adapter.requests))
	}
	var toolResult string
	for _, msg := range adapter.requests[1].Messages {
		if msg.Role == models.RoleTool {
			toolResult = msg.ToolResult
			break
		}
	}
	if toolResult == "" {
		t.Fatal("second model request did not include a tool result")
	}
	if len(toolResult) > 32*1024 {
		t.Fatalf("tool result len = %d, want <= 32768", len(toolResult))
	}
	if !strings.Contains(toolResult, "tool result truncated") {
		t.Fatalf("tool result missing truncation marker")
	}
}

// TestRun_PreApprovedToolsBypassPropose — write-class tool dispatch in
// propose mode normally short-circuits to awaiting_approval. With the
// tool name in RunOpts.PreApprovedTools we expect the run to proceed
// normally and terminate as success.
func TestRun_PreApprovedToolsBypassPropose(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
		{Text: "Done.", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"post_message": {OutputJSON: `{"posted":true}`, WriteClass: true},
	}}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{
		PreApprovedTools: []string{"post_message"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success (pre-approved bypass)", run.Status)
	}
	if len(disp.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(disp.dispatched))
	}
}
