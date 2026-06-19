package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// fakeRunner records every RunWithOpts invocation so we can assert
// that ApproveAndResume actually fires a new run with the expected
// PreApprovedTools payload.
type fakeRunner struct {
	calls []runner.RunOpts
	runID string
	err   error
	// cancelCalls records (runID, reason) pairs passed to Cancel.
	// cancelReturn is what Cancel reports — true models a live run the
	// runner owns (single-writer path), false models no live entry.
	cancelCalls  [][2]string
	cancelReturn bool
}

func (f *fakeRunner) Cancel(runID, reason string) bool {
	f.cancelCalls = append(f.cancelCalls, [2]string{runID, reason})
	return f.cancelReturn
}

func (f *fakeRunner) RunWithOpts(_ context.Context, _ string, opts runner.RunOpts) (string, error) {
	f.calls = append(f.calls, opts)
	if f.err != nil {
		return "", f.err
	}
	if f.runID == "" {
		return "run-fake", nil
	}
	return f.runID, nil
}

// seedPendingApproval is the test fixture: create a worker, persist a
// pending WorkerApproval row directly, return the IDs.
func seedPendingApproval(t *testing.T, db interface {
	CreateWorker(context.Context, *store.Worker) error
	CreateWorkerRun(context.Context, *store.WorkerRun) error
	CreateWorkerApproval(context.Context, *store.WorkerApproval) error
}, wsID, scopeID string) (workerID, runID, appID string) {
	t.Helper()
	ctx := context.Background()
	w := &store.Worker{
		Name:           "approval-worker",
		ModelProvider:  "anthropic",
		ModelID:        "claude-opus-4-7",
		SecretScopeID:  scopeID,
		PromptTemplate: "x",
		ScheduleSpec:   "0 * * * *",
		WorkspaceID:    wsID,
		Enabled:        true,
	}
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	run := &store.WorkerRun{WorkerID: w.ID, Status: "awaiting_approval"}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	app := &store.WorkerApproval{
		WorkerID:  w.ID,
		RunID:     run.ID,
		ToolName:  "post_message",
		ToolInput: `{"text":"hi"}`,
		Reason:    "write-class tool, propose-mode",
	}
	if err := db.CreateWorkerApproval(ctx, app); err != nil {
		t.Fatalf("create approval: %v", err)
	}
	return w.ID, run.ID, app.ID
}

func TestService_ListApprovals_FiltersByStatus(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	_, _, _ = seedPendingApproval(t, db, wsID, scopeID)
	ctx := context.Background()

	pending, err := svc.ListApprovals(ctx, admin.ListApprovalsInput{Status: "pending"})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}

	all, err := svc.ListApprovals(ctx, admin.ListApprovalsInput{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("all count = %d, want 1", len(all))
	}

	rejected, _ := svc.ListApprovals(ctx, admin.ListApprovalsInput{Status: "rejected"})
	if len(rejected) != 0 {
		t.Fatalf("rejected count = %d, want 0", len(rejected))
	}
}

// TestService_ApproveAndResume_FiresNewRun is the critical M1.4 test:
// approving an approval must spawn a NEW runner.RunWithOpts call with
// PreApprovedTools = the approved tool name. Without the runner wired,
// the call succeeds but ResumedRunID stays empty.
func TestService_ApproveAndResume_FiresNewRun(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	_, _, appID := seedPendingApproval(t, db, wsID, scopeID)
	fr := &fakeRunner{runID: "run-resumed-1"}
	svc.SetRunnerForTest(fr)

	out, err := svc.ApproveAndResume(context.Background(), appID, "ada")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if out.Status != "approved" {
		t.Fatalf("status = %q, want approved", out.Status)
	}
	if out.ResumedRunID != "run-resumed-1" {
		t.Fatalf("resumed_run_id = %q", out.ResumedRunID)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(fr.calls))
	}
	if len(fr.calls[0].PreApprovedTools) != 1 ||
		fr.calls[0].PreApprovedTools[0] != "post_message" {
		t.Fatalf("PreApprovedTools = %v, want [post_message]",
			fr.calls[0].PreApprovedTools)
	}

	// Approval row should be marked approved + decided_by recorded.
	app, err := db.GetWorkerApproval(context.Background(), appID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if app.Status != "approved" || app.DecidedBy != "ada" {
		t.Fatalf("approval post-state: %+v", app)
	}
	if app.ResumedRunID != "run-resumed-1" {
		t.Fatalf("resumed_run_id on row: %q", app.ResumedRunID)
	}
}

func TestService_ApproveAndResume_RejectsAlreadyDecided(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	_, _, appID := seedPendingApproval(t, db, wsID, scopeID)
	svc.SetRunnerForTest(&fakeRunner{runID: "run-1"})
	if _, err := svc.ApproveAndResume(context.Background(), appID, "ada"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	_, err := svc.ApproveAndResume(context.Background(), appID, "bob")
	if err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("second approve: want already-decided error, got %v", err)
	}
}

func TestService_Reject(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	_, runID, appID := seedPendingApproval(t, db, wsID, scopeID)

	out, err := svc.Reject(context.Background(), appID, "carol")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if out.Status != "rejected" {
		t.Fatalf("status = %q", out.Status)
	}
	if out.OriginalRunID != runID {
		t.Fatalf("original_run_id = %q, want %q", out.OriginalRunID, runID)
	}
	app, _ := db.GetWorkerApproval(context.Background(), appID)
	if app.Status != "rejected" || app.DecidedBy != "carol" {
		t.Fatalf("approval after reject: %+v", app)
	}
	// Run row best-effort stamped rejected.
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != "rejected" {
		t.Fatalf("run not stamped rejected: %q", run.Status)
	}
}

func TestService_Reject_NotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.Reject(context.Background(), "wapp-no-such", "x")
	if !errors.Is(err, store.ErrWorkerApprovalNotFound) {
		t.Fatalf("err = %v, want ErrWorkerApprovalNotFound", err)
	}
}

// TestService_Create_AcceptsCaps verifies the new M1 cap fields make
// it from CreateInput through validation onto the persisted Worker
// row. Negative caps must be rejected.
func TestService_Create_AcceptsCaps(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	in := baseCreate(wsID, scopeID)
	in.MaxInputTokens = 8000
	in.MaxOutputTokens = 2048
	in.MaxToolCalls = 30
	in.MaxWallClockSeconds = 120
	in.MaxMonthlyCostUSD = 5.25
	in.MaxConsecutiveFailures = 3

	w, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.MaxInputTokens != 8000 || w.MaxOutputTokens != 2048 {
		t.Fatalf("tokens: %d/%d", w.MaxInputTokens, w.MaxOutputTokens)
	}
	if w.MaxToolCalls != 30 || w.MaxWallClockSeconds != 120 {
		t.Fatalf("calls/wall: %d/%d", w.MaxToolCalls, w.MaxWallClockSeconds)
	}
	if w.MaxMonthlyCostUSD != 5.25 || w.MaxConsecutiveFailures != 3 {
		t.Fatalf("cost/streak: %v/%d", w.MaxMonthlyCostUSD, w.MaxConsecutiveFailures)
	}
}

func TestService_Create_RejectsNegativeCap(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	in := baseCreate(wsID, scopeID)
	in.MaxToolCalls = -1
	if _, err := svc.Create(context.Background(), in); err == nil {
		t.Fatal("expected error for negative cap")
	}
}
