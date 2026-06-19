package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// waitForRunningRun polls until a status="running" run row exists for the
// worker and returns its id, so a test can hard-stop a run whose id the
// runner generated internally.
func waitForRunningRun(t *testing.T, db *sqlite.DB, workerID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := db.ListWorkerRuns(context.Background(), workerID, 5)
		if err == nil {
			for _, r := range runs {
				if r.Status == "running" {
					return r.ID
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no running run appeared for worker")
	return ""
}

// TestRunner_LiveCancelInterruptsBlockingAdapter is the headline
// hard-stop case: a run is blocked inside adapter.Send when an operator
// calls Cancel. The per-run execution context is torn down, Send
// unblocks, and the runner finalises the row as the SINGLE WRITER of the
// distinct status=cancelled (carrying the operator reason) — not failure,
// not cap_exceeded.
func TestRunner_LiveCancelInterruptsBlockingAdapter(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	// blockingAdapter (defined in runner_test.go) blocks inside Send until
	// ctx is cancelled — `done` is never closed here, so only the operator
	// hard-stop can unblock it.
	adapter := &blockingAdapter{done: make(chan struct{})}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	var runID string
	var runErr error
	done := make(chan struct{})
	go func() {
		runID, runErr = r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{})
		close(done)
	}()

	// Once the row is observable as 'running', the run is live and either
	// inside Send or about to enter it — both are interruptible by Cancel.
	id := waitForRunningRun(t, db, w.ID)
	if !r.Cancel(id, "stale delegation superseded") {
		t.Fatal("Cancel returned false for a live run")
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithOpts did not return after cancel")
	}
	if runErr != nil {
		t.Fatalf("run err: %v", runErr)
	}

	got, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != runner.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if !strings.Contains(got.Error, "stale delegation superseded") {
		t.Fatalf("error = %q, want operator reason embedded", got.Error)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at not set on cancelled run")
	}
}

// raceProbeStore wraps the real store and records the live-cancel
// registry size at the instant the run row is created, proving the
// registration-window invariant: the cancel handle is registered BEFORE
// the row becomes observable.
type raceProbeStore struct {
	*sqlite.DB
	r              *runner.Runner
	activeAtCreate int
	seen           bool
}

func (s *raceProbeStore) CreateWorkerRun(ctx context.Context, run *store.WorkerRun) error {
	s.activeAtCreate = s.r.ActiveRunCountForTest()
	s.seen = true
	return s.DB.CreateWorkerRun(ctx, run)
}

// TestRunner_CancelHandleRegisteredBeforeRunRowObservable closes the
// registration-window race deterministically: by the time CreateWorkerRun
// makes the row queryable (the first moment a concurrent CancelRun could
// SELECT it), the run's cancel handle is already in the live registry, so
// a hard-stop can never miss an in-flight run.
func TestRunner_CancelHandleRegisteredBeforeRunRowObservable(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	probe := &raceProbeStore{DB: db}
	r := runner.New(runner.Deps{
		Store:      probe,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
	probe.r = r

	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !probe.seen {
		t.Fatal("CreateWorkerRun was never invoked")
	}
	if probe.activeAtCreate < 1 {
		t.Fatalf("cancel handle not registered before run row observable: active=%d, want >=1", probe.activeAtCreate)
	}
	// And the entry is cleaned up once the run finishes.
	if c := r.ActiveRunCountForTest(); c != 0 {
		t.Fatalf("active runs after completion = %d, want 0", c)
	}
}

// TestRunner_CtxCancelCauseMapping pins the cause→status mapping the
// runLoop relies on so operator cancel, wall-clock cap, and parent
// deadline never get conflated.
func TestRunner_CtxCancelCauseMapping(t *testing.T) {
	r := runner.New(runner.Deps{})
	for kind, want := range map[string]string{
		"operator":  runner.StatusCancelled,
		"wallclock": runner.StatusCapExceeded,
		"parent":    runner.StatusFailure,
	} {
		if got := r.MapCancelCauseForTest(kind); got != want {
			t.Errorf("cause %q → %q, want %q", kind, got, want)
		}
	}
}

// TestRunner_HardStopRegistry exercises the live-cancel registry control
// surface directly: Cancel finds and signals a registered run (stamping
// its reason), is idempotent, and returns false for unknown / unregistered
// ids.
func TestRunner_HardStopRegistry(t *testing.T) {
	r := runner.New(runner.Deps{})
	if r.ActiveRunCountForTest() != 0 {
		t.Fatalf("fresh runner active=%d, want 0", r.ActiveRunCountForTest())
	}

	_, cancel := context.WithCancelCause(context.Background())
	r.RegisterActiveRunForTest("run-1", cancel)
	if r.ActiveRunCountForTest() != 1 {
		t.Fatalf("after register active=%d, want 1", r.ActiveRunCountForTest())
	}
	if !r.Cancel("run-1", "operator pulled the plug") {
		t.Fatal("Cancel for live run returned false")
	}
	if got := r.OperatorCancelReasonForTest("run-1"); got != "operator pulled the plug" {
		t.Fatalf("stored reason = %q", got)
	}
	if !r.Cancel("run-1", "second") {
		t.Fatal("second Cancel for still-registered run returned false (want idempotent true)")
	}
	if r.Cancel("no-such-run", "") {
		t.Fatal("Cancel for unknown run returned true")
	}

	r.UnregisterActiveRunForTest("run-1")
	if r.ActiveRunCountForTest() != 0 {
		t.Fatalf("after unregister active=%d, want 0", r.ActiveRunCountForTest())
	}
	if r.Cancel("run-1", "") {
		t.Fatal("Cancel after unregister returned true")
	}
}

// TestAutoPause_CancelledExcludedFromFailureStreak proves an operator
// hard-stop is TRANSPARENT to the consecutive-failure auto-pause streak:
// a cancelled run interleaved between failures neither resets the streak
// nor counts toward it. With the cancelled row excluded, the three most
// recent non-cancelled runs are all failures, so the worker pauses —
// whereas if cancelled counted as "not a failure" it would have reset the
// streak and the worker would stay enabled.
func TestAutoPause_CancelledExcludedFromFailureStreak(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxConsecutiveFailures = 3
	createWorker(t, db, w)

	base := time.Now().UTC().Add(-10 * time.Minute)
	seed := []struct {
		status string
		offset time.Duration
	}{
		{"failure", 0},
		{"failure", time.Minute},
		{"cancelled", 2 * time.Minute}, // operator hard-stop between failures
	}
	for _, s := range seed {
		r := &store.WorkerRun{
			WorkerID:  w.ID,
			Status:    s.status,
			StartedAt: base.Add(s.offset),
		}
		if err := db.CreateWorkerRun(context.Background(), r); err != nil {
			t.Fatalf("seed %s: %v", s.status, err)
		}
	}

	// A third real failure (newest run) drives the streak check.
	adapter := &fakeAdapter{err: errSentinelBoom()}
	rn := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})
	if _, err := rn.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := db.GetWorker(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.Enabled {
		t.Fatal("worker still enabled — cancelled run wrongly reset the failure streak")
	}
	if !strings.Contains(got.AutoPausedReason, "consecutive failures") {
		t.Fatalf("auto_paused_reason = %q", got.AutoPausedReason)
	}
}
