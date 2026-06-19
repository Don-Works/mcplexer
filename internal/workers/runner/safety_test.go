package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// panicAdapter panics on every Send call. Verifies the runner's
// defer/recover guard finalises the WorkerRun row as failure instead
// of leaking it in status='running'.
type panicAdapter struct {
	message string
}

func (p *panicAdapter) Send(_ context.Context, _ models.SendRequest) (*models.SendResponse, error) {
	panic(p.message)
}

func TestRun_PanicInAdapterFinalisesAsFailure(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &panicAdapter{message: "synthetic adapter panic for test"}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if runID == "" {
		t.Fatal("expected runID even on panic")
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != runner.StatusFailure {
		t.Fatalf("status = %q, want failure (panic should finalise as failure)", run.Status)
	}
	if !strings.Contains(run.Error, "runner panic") {
		t.Errorf("error = %q, want runner-panic marker", run.Error)
	}
	if !strings.Contains(run.Error, "synthetic adapter panic") {
		t.Errorf("error = %q, want panic message included", run.Error)
	}
	count, _ := db.CountRunningWorkerRuns(context.Background(), w.ID)
	if count != 0 {
		t.Errorf("CountRunningWorkerRuns = %d after panic, want 0 (no orphan)", count)
	}
}

// ctxRecordingAdapter captures the ctx it was called with so a test
// can assert the runner derived a per-call deadline before invoking
// the adapter.
type ctxRecordingAdapter struct {
	deadlineSeen bool
	deadlineAt   time.Time
}

func (a *ctxRecordingAdapter) Send(ctx context.Context, _ models.SendRequest) (*models.SendResponse, error) {
	if d, ok := ctx.Deadline(); ok {
		a.deadlineSeen = true
		a.deadlineAt = d
	}
	return &models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}, nil
}

func TestRun_PerCallContextHasDeadline(t *testing.T) {
	// Regression for the stuck-runs class of bug. The adapter Send call
	// must receive a ctx with a deadline so a hung subprocess gets
	// killed instead of waiting forever for the loop's top-of-iteration
	// wall-clock check (which never runs while Send is blocked).
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &ctxRecordingAdapter{}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !adapter.deadlineSeen {
		t.Fatal("adapter ctx had no deadline — per-call timeout not applied")
	}
	if d := time.Until(adapter.deadlineAt); d <= 0 || d > 10*time.Minute {
		t.Errorf("ctx deadline = %v from now, want a sensible positive duration", d)
	}
}

// blockingCancelAdapter blocks until ctx is cancelled, returning ctx.Err().
// Used to verify the runner can recover from a wedged Send.
type blockingCancelAdapter struct{}

func (blockingCancelAdapter) Send(ctx context.Context, _ models.SendRequest) (*models.SendResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRun_OuterCtxCancelTerminatesBlockedSend(t *testing.T) {
	// Even with an unbounded outer ctx, a caller (RunWithOpts) that
	// cancels the ctx must unblock the adapter and finalise the row.
	// This is the recovery contract the cancel_worker_run path
	// ultimately leans on (cancel doesn't interrupt the goroutine, but
	// the runner's own ctx-cancel paths still work).
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	r := makeRunner(t, db, blockingCancelAdapter{}, &fakeDispatcher{}, &fakeMesh{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	runID, err := r.Run(ctx, w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v after ctx cancel, expected prompt return", elapsed)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run == nil {
		t.Fatal("run row missing")
	}
	if run.Status != runner.StatusFailure {
		t.Errorf("status = %q, want failure", run.Status)
	}
	count, _ := db.CountRunningWorkerRuns(context.Background(), w.ID)
	if count != 0 {
		t.Errorf("running count = %d after ctx cancel, want 0", count)
	}
}

func TestReconcileOrphanedRuns_FlipsStuckRunningRows(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	now := time.Now().UTC().Truncate(time.Second)
	oldRun := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: now.Add(-2 * time.Hour),
		Status:    "running",
	}
	freshRun := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: now.Add(-30 * time.Second),
		Status:    "running",
	}
	doneRun := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: now.Add(-3 * time.Hour),
		Status:    "success",
	}
	for _, r := range []*store.WorkerRun{oldRun, freshRun, doneRun} {
		if err := db.CreateWorkerRun(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.Status, err)
		}
	}

	n, err := db.ReconcileOrphanedRuns(ctx,
		now.Add(-1*time.Hour), now, "test sweep")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled = %d, want 1 (only the 2h-old run)", n)
	}

	got, _ := db.GetWorkerRun(ctx, oldRun.ID)
	if got.Status != runner.StatusFailure {
		t.Errorf("old run status = %q, want failure", got.Status)
	}
	if !strings.Contains(got.Error, "test sweep") {
		t.Errorf("old run error = %q, want reason carried", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("old run finished_at not set after reconcile")
	}
	if got.DurationMS <= 0 {
		t.Errorf("old run duration_ms = %d, want positive", got.DurationMS)
	}

	gotFresh, _ := db.GetWorkerRun(ctx, freshRun.ID)
	if gotFresh.Status != "running" {
		t.Errorf("fresh run flipped — status = %q, want running", gotFresh.Status)
	}
	gotDone, _ := db.GetWorkerRun(ctx, doneRun.ID)
	if gotDone.Status != "success" {
		t.Errorf("done run touched — status = %q, want success unchanged", gotDone.Status)
	}

	// Idempotent: re-running returns 0 because the row is no longer
	// in 'running' state.
	n2, err := db.ReconcileOrphanedRuns(ctx,
		now.Add(-1*time.Hour), now, "test sweep again")
	if err != nil {
		t.Fatalf("reconcile-again: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second reconcile reconciled = %d, want 0 (idempotent)", n2)
	}
}

func TestCancelRun_FlipsRunningToCancelled(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	r := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: time.Now().UTC().Add(-5 * time.Minute),
		Status:    "running",
	}
	if err := db.CreateWorkerRun(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := db.CancelRun(ctx, r.ID, time.Now().UTC(), "operator cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ := db.GetWorkerRun(ctx, r.ID)
	if got.Status != runner.StatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	if !strings.Contains(got.Error, "operator cancel") {
		t.Errorf("error = %q, want reason included", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at not set")
	}
}

func TestCancelRun_AlreadyTerminalReturnsNotCancellable(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	r := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: time.Now().UTC(),
		Status:    "success",
	}
	if err := db.CreateWorkerRun(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}
	finishedAt := time.Now().UTC()
	if err := db.UpdateWorkerRunStatus(ctx, r.ID, store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: finishedAt,
		OutputText: "all good",
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	err := db.CancelRun(ctx, r.ID, time.Now().UTC(), "operator cancel")
	if !errors.Is(err, store.ErrRunNotCancellable) {
		t.Fatalf("err = %v, want ErrRunNotCancellable", err)
	}

	// Row must remain unchanged.
	got, _ := db.GetWorkerRun(ctx, r.ID)
	if got.Status != "success" {
		t.Errorf("status flipped to %q, must stay success", got.Status)
	}
	if got.OutputText != "all good" {
		t.Errorf("output_text changed to %q, must stay intact", got.OutputText)
	}
}

func TestCancelRun_MissingRunReturnsNotFound(t *testing.T) {
	db := newTestStore(t)
	err := db.CancelRun(context.Background(), "no-such-id", time.Now().UTC(), "x")
	if !errors.Is(err, store.ErrWorkerRunNotFound) {
		t.Fatalf("err = %v, want ErrWorkerRunNotFound", err)
	}
}
