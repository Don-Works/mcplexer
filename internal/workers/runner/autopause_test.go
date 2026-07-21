package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// TestAutoPause_MonthlyCostCapTriggers — when a successful run pushes
// total monthly cost over MaxMonthlyCostUSD, the runner pauses the
// worker and emits a critical mesh alert. Cost calc uses the model
// estimator (anthropic + claude-sonnet-4-6 → known per-token rates).
func TestAutoPause_MonthlyCostCapTriggers(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxMonthlyCostUSD = 0.0001 // microscopic cap → trips on first run
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			Text:         "ok",
			InputTokens:  1000,
			OutputTokens: 500,
			StopReason:   models.StopEndTurn,
		},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, mesh)

	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Worker should now be paused with auto_paused_reason set.
	got, err := db.GetWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.Enabled {
		t.Fatalf("worker still enabled after auto-pause")
	}
	if !strings.Contains(got.AutoPausedReason, "monthly budget exceeded") {
		t.Fatalf("auto_paused_reason = %q", got.AutoPausedReason)
	}

	// Mesh alert MUST have fired with critical priority.
	if !meshSawAlertContaining(mesh, "monthly budget exceeded", "critical") {
		t.Fatalf("expected critical mesh alert; got %v", mesh.sent)
	}
}

// TestAutoPause_ConsecutiveFailuresTriggers — three consecutive
// failure-status runs (the threshold) should pause the worker and emit
// a high-priority mesh alert. We pre-seed two failure rows, then drive
// a third failing run.
func TestAutoPause_ConsecutiveFailuresTriggers(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxConsecutiveFailures = 3
	createWorker(t, db, w)

	// Two pre-existing failures.
	for range 2 {
		r := &store.WorkerRun{
			WorkerID: w.ID,
			Status:   "failure",
		}
		if err := db.CreateWorkerRun(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		// Mark each as terminal so they count toward LastFailureStatuses.
		// (The default CreateWorkerRun sets Status to "running" if empty —
		// we already explicitly set it.)
	}

	// Adapter returns an error so the new run terminates as failure.
	adapter := &fakeAdapter{
		// Send returns err on first call → run logs failure.
	}
	adapter.err = errSentinelBoom()
	mesh := &fakeMesh{}
	rn := makeRunner(t, db, adapter, &fakeDispatcher{}, mesh)

	if _, err := rn.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := db.GetWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.Enabled {
		t.Fatalf("worker still enabled after streak auto-pause")
	}
	if !strings.Contains(got.AutoPausedReason, "consecutive failures") {
		t.Fatalf("auto_paused_reason = %q", got.AutoPausedReason)
	}
	if !meshSawAlertContaining(mesh, "consecutive failures", "high") {
		t.Fatalf("expected high mesh alert; got %v", mesh.sent)
	}
}

// TestAutoPause_NoCapIsNoOp — worker without any caps set runs a
// failure to completion and stays enabled. Sanity check that the
// auto-pause hook doesn't false-positive on the default config.
func TestAutoPause_NoCapIsNoOp(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{err: errSentinelBoom()}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := db.GetWorker(context.Background(), w.ID)
	if !got.Enabled {
		t.Fatalf("worker unexpectedly paused: reason=%q", got.AutoPausedReason)
	}
}

// TestAutoPause_PendingApprovalPersisted — propose-mode worker that
// hits a write tool persists a WorkerApproval row and fires a high-
// priority mesh alert. Confirms the M1 propose-first persistence.
func TestAutoPause_PendingApprovalPersisted(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			ToolCalls: []models.ToolCall{
				{ID: "t1", Name: "post_message", Input: map[string]any{"text": "hi"}},
			},
			StopReason: models.StopToolUse,
		},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"post_message": {OutputJSON: `{"would_post":true}`, WriteClass: true},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}

	pending, err := db.ListWorkerApprovals(context.Background(), "pending", 10)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending list = %d, want 1", len(pending))
	}
	p := pending[0]
	if p.ToolName != "post_message" || p.WorkerID != w.ID {
		t.Fatalf("approval row mismatch: %+v", p)
	}
	if !strings.Contains(p.ToolInput, "hi") {
		t.Fatalf("tool_input not persisted: %q", p.ToolInput)
	}
	if !meshSawAlertContaining(mesh, "needs approval", "high") {
		t.Fatalf("expected approval mesh alert; got %v", mesh.sent)
	}
}

// errSentinelBoom returns a fresh error for failure-path tests.
func errSentinelBoom() error { return &boomErr{} }

type boomErr struct{}

func (b *boomErr) Error() string { return "boom" }

// meshSawAlertContaining returns true when the mesh fake recorded an
// alert with the given content fragment + priority.
func meshSawAlertContaining(m *fakeMesh, contentFragment, priority string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.sent {
		if msg.Kind != "alert" {
			continue
		}
		if msg.Priority != priority {
			continue
		}
		if strings.Contains(msg.Content, contentFragment) {
			return true
		}
	}
	return false
}
