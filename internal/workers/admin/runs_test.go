package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// stubRunner records RunWithOpts calls and returns the canned id.
type stubRunner struct {
	gotID string
	opts  runner.RunOpts
	runID string
	err   error
	// cancelCalls records (runID, reason) passed to Cancel; cancelReturn
	// is the canned response (true = live run owned by the runner).
	cancelCalls  [][2]string
	cancelReturn bool
}

func (s *stubRunner) RunWithOpts(_ context.Context, workerID string, opts runner.RunOpts) (string, error) {
	s.gotID = workerID
	s.opts = opts
	return s.runID, s.err
}

func (s *stubRunner) Cancel(runID, reason string) bool {
	s.cancelCalls = append(s.cancelCalls, [2]string{runID, reason})
	return s.cancelReturn
}

func TestServiceRunNowDelegatesToRunner(t *testing.T) {
	_, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	stub := &stubRunner{runID: "run-stub-1"}
	svcWithRunner := admin.New(db, admin.Options{
		Workspaces: db,
		Runner:     stub,
	})
	w, err := svcWithRunner.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := svcWithRunner.RunNow(ctx, w.ID)
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out.RunID != "run-stub-1" || out.Status != "running" {
		t.Errorf("out = %+v, want {RunID:run-stub-1 Status:running}", out)
	}
	if stub.gotID != w.ID {
		t.Errorf("runner saw id %q, want %q", stub.gotID, w.ID)
	}
	if stub.opts.TriggerKind != "manual" {
		t.Errorf("RunNow trigger_kind = %q, want manual", stub.opts.TriggerKind)
	}
}

func TestServiceRunNowStubsWhenRunnerNil(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := svc.RunNow(ctx, w.ID)
	if err != nil {
		t.Fatalf("run_now (stub): %v", err)
	}
	if out.RunID == "" || out.Status != "running" {
		t.Errorf("stub out = %+v, want non-empty run_id + running", out)
	}
	// Stub path must persist a run row so the agent can poll it.
	got, err := svc.GetRun(ctx, out.RunID)
	if err != nil {
		t.Fatalf("get_run: %v", err)
	}
	if got.WorkerID != w.ID {
		t.Errorf("stub run worker_id = %q, want %q", got.WorkerID, w.ID)
	}
	if got.TriggerKind != "manual" {
		t.Errorf("stub run trigger_kind = %q, want manual", got.TriggerKind)
	}
}

func TestServiceRunNowDisabledWorkerDoesNotDispatch(t *testing.T) {
	_, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	stub := &stubRunner{runID: "run-should-not-start"}
	svc := admin.New(db, admin.Options{
		Workspaces: db,
		Runner:     stub,
	})
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Pause(ctx, w.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	out, err := svc.RunNow(ctx, w.ID)
	if !errors.Is(err, runner.ErrWorkerDisabled) {
		t.Fatalf("err = %v, want ErrWorkerDisabled", err)
	}
	if out.RunID != "" {
		t.Fatalf("runID = %q, want empty", out.RunID)
	}
	if stub.gotID != "" {
		t.Fatalf("runner was called with %q, want no dispatch", stub.gotID)
	}
	runs, err := db.ListWorkerRuns(ctx, w.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("created %d run rows for disabled worker, want 0", len(runs))
	}
}

func TestServiceListRunsStatusFilter(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Two runs — one running, one finalised as success.
	r1 := &store.WorkerRun{WorkerID: w.ID, Status: "running", StartedAt: time.Now().UTC()}
	if err := db.CreateWorkerRun(ctx, r1); err != nil {
		t.Fatalf("seed run 1: %v", err)
	}
	r2 := &store.WorkerRun{WorkerID: w.ID, Status: "running", StartedAt: time.Now().UTC().Add(time.Second)}
	if err := db.CreateWorkerRun(ctx, r2); err != nil {
		t.Fatalf("seed run 2: %v", err)
	}
	if err := db.UpdateWorkerRunStatus(ctx, r2.ID, store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: time.Now().UTC().Add(2 * time.Second),
		OutputText: "ok",
	}); err != nil {
		t.Fatalf("finalize run 2: %v", err)
	}
	runs, err := svc.ListRuns(ctx, admin.ListRunsInput{WorkerID: w.ID, Status: "success"})
	if err != nil {
		t.Fatalf("list runs filtered: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != r2.ID {
		t.Errorf("filtered runs = %+v, want one r2", runs)
	}
	all, _ := svc.ListRuns(ctx, admin.ListRunsInput{WorkerID: w.ID})
	if len(all) != 2 {
		t.Errorf("all runs len = %d, want 2", len(all))
	}
}

func TestServiceListRunsReturnsPreviews(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	fullPrompt := strings.Repeat("p", 3000)
	fullOutput := strings.Repeat("o", 6000)
	run := &store.WorkerRun{
		WorkerID:       w.ID,
		Status:         "running",
		StartedAt:      time.Now().UTC(),
		PromptRendered: fullPrompt,
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: time.Now().UTC().Add(time.Second),
		OutputText: fullOutput,
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	listed, err := svc.ListRuns(ctx, admin.ListRunsInput{WorkerID: w.ID})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed runs = %d, want 1", len(listed))
	}
	if len(listed[0].PromptRendered) >= len(fullPrompt) {
		t.Fatalf("list prompt was not truncated")
	}
	if len(listed[0].OutputText) >= len(fullOutput) {
		t.Fatalf("list output was not truncated")
	}
	if !strings.Contains(listed[0].OutputText, "use get_worker_run for full text") {
		t.Fatalf("list output missing preview marker")
	}

	got, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.PromptRendered != fullPrompt {
		t.Fatalf("get prompt len = %d, want full %d", len(got.PromptRendered), len(fullPrompt))
	}
	if got.OutputText != fullOutput {
		t.Fatalf("get output len = %d, want full %d", len(got.OutputText), len(fullOutput))
	}
}

func TestServiceCancelRun(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    "running",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	out, err := svc.CancelRun(ctx, admin.CancelRunInput{
		RunID:  run.ID,
		Reason: "operator saw stale pid",
	})
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if out.RunID != run.ID || out.Status != "cancelled" {
		t.Fatalf("cancel output = %+v", out)
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != "cancelled" || got.Error != "cancelled by operator: operator saw stale pid" {
		t.Fatalf("cancelled row = %+v", got)
	}
	if _, err := svc.CancelRun(ctx, admin.CancelRunInput{RunID: run.ID}); !errors.Is(err, store.ErrRunNotCancellable) {
		t.Fatalf("second cancel err = %v, want ErrRunNotCancellable", err)
	}
}

// TestServiceCancelRun_LiveRunnerOwnsFinalize — when a LIVE runner entry
// exists, CancelRun signals the runner (single writer) and must NOT
// direct-flip the DB row. The row stays 'running' here because the
// (stub) runner is the one that would finalise it.
func TestServiceCancelRun_LiveRunnerOwnsFinalize(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stub := &stubRunner{cancelReturn: true} // models a live run the runner owns
	svc.SetRunnerForTest(stub)

	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    "running",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	out, err := svc.CancelRun(ctx, admin.CancelRunInput{RunID: run.ID, Reason: "superseded"})
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if out.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", out.Status)
	}
	if len(stub.cancelCalls) != 1 || stub.cancelCalls[0][0] != run.ID {
		t.Fatalf("runner.Cancel calls = %v, want one for %s", stub.cancelCalls, run.ID)
	}
	if stub.cancelCalls[0][1] != "cancelled by operator: superseded" {
		t.Fatalf("runner.Cancel reason = %q", stub.cancelCalls[0][1])
	}
	// CRITICAL single-writer property: the service did NOT direct-flip
	// the DB — the row is still 'running', awaiting the runner's own
	// terminal write.
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("row status = %q, want still running (runner owns finalize)", got.Status)
	}
}

// TestServiceCancelRun_OrphanFallback — when NO live runner entry exists
// (orphan/stub running row whose runner died), CancelRun direct-flips the
// DB to the distinct 'cancelled' status. The runner.Cancel attempt is
// still made (and reports false) before falling back.
func TestServiceCancelRun_OrphanFallback(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stub := &stubRunner{cancelReturn: false} // no live entry
	svc.SetRunnerForTest(stub)

	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    "running",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	out, err := svc.CancelRun(ctx, admin.CancelRunInput{RunID: run.ID})
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if out.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", out.Status)
	}
	if len(stub.cancelCalls) != 1 {
		t.Fatalf("expected one runner.Cancel attempt, got %v", stub.cancelCalls)
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != "cancelled" {
		t.Fatalf("row status = %q, want cancelled (orphan direct-flip)", got.Status)
	}
}
