package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Fakes + test helpers live in exec_worker_fakes_test.go so this file
// stays focused on the actual test cases.

func TestSchedulerWorkerKindDispatchesToRunner(t *testing.T) {
	st := newMemStore()
	wexec := newFakeExec("run-1", nil)
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-1"] = &store.Worker{ID: "wkr-1", Enabled: true, ConcurrencyPolicy: "skip"}
	wstore.runs["run-1"] = &store.WorkerRun{ID: "run-1", WorkerID: "wkr-1", Status: "success"}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-worker", "wkr-1", s.clock.Now().Add(time.Second))

	if err := s.RunOnce(context.Background(), "j-worker"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wexec.waitForDone(t, 2*time.Second)
	if wexec.count() != 1 {
		t.Errorf("worker executor called %d times, want 1", wexec.count())
	}
	got := waitForRowStatus(t, st, "j-worker", "success", 2*time.Second)
	if got.LastError != "" {
		t.Errorf("last_error = %q, want empty", got.LastError)
	}
}

func TestSchedulerWorkerKindSkippedWhenWorkerDisabled(t *testing.T) {
	st := newMemStore()
	wexec := &fakeWorkerExec{}
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-disabled"] = &store.Worker{ID: "wkr-disabled", Enabled: false}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-d", "wkr-disabled", s.clock.Now().Add(time.Second))

	if err := s.RunOnce(context.Background(), "j-d"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if wexec.count() != 0 {
		t.Errorf("worker executor must not be called on disabled worker: %d", wexec.count())
	}
	got, _ := st.GetScheduledJob(context.Background(), "j-d")
	if got.LastStatus != "skipped" {
		t.Errorf("last_status = %q, want skipped", got.LastStatus)
	}
	if got.LastError == "" {
		t.Error("last_error should explain why we skipped")
	}
}

func TestSchedulerWorkerKindDeletedScheduleRowDoesNotDispatch(t *testing.T) {
	st := newMemStore()
	wexec := &fakeWorkerExec{}
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-stale"] = &store.Worker{ID: "wkr-stale", Enabled: true}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	at := s.clock.Now().Add(time.Second)
	stale := store.ScheduledJob{
		ID: "j-stale", Name: "j-stale", Kind: KindWorker, Spec: "1h",
		WorkerID: "wkr-stale", Enabled: true, NextRunAt: &at,
	}
	if err := st.CreateScheduledJob(context.Background(), &stale); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.DeleteScheduledJob(context.Background(), stale.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	s.fire(context.Background(), stale)

	if wexec.count() != 0 {
		t.Fatalf("stale deleted schedule row dispatched worker %d times, want 0", wexec.count())
	}
}

func TestSchedulerWorkerKindDisabledScheduleRowDoesNotDispatch(t *testing.T) {
	st := newMemStore()
	wexec := &fakeWorkerExec{}
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-enabled"] = &store.Worker{ID: "wkr-enabled", Enabled: true}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	at := s.clock.Now().Add(time.Second)
	stale := store.ScheduledJob{
		ID: "j-disabled", Name: "j-disabled", Kind: KindWorker, Spec: "1h",
		WorkerID: "wkr-enabled", Enabled: true, NextRunAt: &at,
	}
	if err := st.CreateScheduledJob(context.Background(), &stale); err != nil {
		t.Fatalf("seed: %v", err)
	}
	current := stale
	current.Enabled = false
	if err := st.UpdateScheduledJob(context.Background(), &current); err != nil {
		t.Fatalf("disable schedule row: %v", err)
	}

	s.fire(context.Background(), stale)

	if wexec.count() != 0 {
		t.Fatalf("disabled schedule row dispatched worker %d times, want 0", wexec.count())
	}
	got, err := st.GetScheduledJob(context.Background(), stale.ID)
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if got.LastStatus != "disabled" {
		t.Fatalf("last_status = %q, want disabled", got.LastStatus)
	}
}

func TestSchedulerWorkerKindSkippedWhenConcurrencyBlocks(t *testing.T) {
	st := newMemStore()
	wexec := &fakeWorkerExec{}
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-c"] = &store.Worker{ID: "wkr-c", Enabled: true, ConcurrencyPolicy: "skip"}
	wstore.running["wkr-c"] = 1
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-c", "wkr-c", s.clock.Now().Add(time.Second))

	if err := s.RunOnce(context.Background(), "j-c"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if wexec.count() != 0 {
		t.Errorf("executor must not run while concurrency=skip + count>0: %d", wexec.count())
	}
	got, _ := st.GetScheduledJob(context.Background(), "j-c")
	if got.LastStatus != "skipped" {
		t.Errorf("last_status = %q, want skipped", got.LastStatus)
	}
}

func TestSchedulerWorkerKindMissingWorkerIDFails(t *testing.T) {
	st := newMemStore()
	wexec := &fakeWorkerExec{}
	wstore := newFakeWorkerStore()
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	at := s.clock.Now().Add(time.Second)
	// WorkerID intentionally empty.
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID: "j-noid", Name: "j-noid", Kind: KindWorker, Spec: "1h",
		Enabled: true, NextRunAt: &at,
	})
	if err := s.RunOnce(context.Background(), "j-noid"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if wexec.count() != 0 {
		t.Errorf("executor must not be called when WorkerID empty: %d", wexec.count())
	}
	got, _ := st.GetScheduledJob(context.Background(), "j-noid")
	if got.LastStatus != "skipped" {
		t.Errorf("last_status = %q, want skipped", got.LastStatus)
	}
	if got.LastError == "" {
		t.Error("last_error should mention worker_id missing")
	}
}

func TestSchedulerWorkerKindNoExecutorFails(t *testing.T) {
	st := newMemStore()
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-ok"] = &store.Worker{ID: "wkr-ok", Enabled: true}
	s := newWorkerTestScheduler(t, st, nil, wstore)
	seedWorkerJob(t, st, "j-noexec", "wkr-ok", s.clock.Now().Add(time.Second))
	if err := s.RunOnce(context.Background(), "j-noexec"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, _ := st.GetScheduledJob(context.Background(), "j-noexec")
	if got.LastStatus != "failure" {
		t.Errorf("last_status = %q, want failure", got.LastStatus)
	}
	if got.LastError == "" {
		t.Error("last_error should describe missing executor")
	}
}

func TestSchedulerWorkerKindRunnerErrorMarksFailure(t *testing.T) {
	st := newMemStore()
	wexec := newFakeExec("", errors.New("model dead"))
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-e"] = &store.Worker{ID: "wkr-e", Enabled: true}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-e", "wkr-e", s.clock.Now().Add(time.Second))
	if err := s.RunOnce(context.Background(), "j-e"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wexec.waitForDone(t, 2*time.Second)
	got := waitForRowStatus(t, st, "j-e", "failure", 2*time.Second)
	if got.LastError == "" {
		t.Error("last_error should surface runner error")
	}
}

func TestSchedulerWorkerKindRunFailureMirrors(t *testing.T) {
	// Runner completes but WorkerRun terminated in "failure" status —
	// the scheduler must reflect that onto LastStatus + LastError.
	st := newMemStore()
	wexec := newFakeExec("run-f", nil)
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-rf"] = &store.Worker{ID: "wkr-rf", Enabled: true}
	wstore.runs["run-f"] = &store.WorkerRun{
		ID: "run-f", WorkerID: "wkr-rf", Status: "failure", Error: "tool denied",
	}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-rf", "wkr-rf", s.clock.Now().Add(time.Second))
	if err := s.RunOnce(context.Background(), "j-rf"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wexec.waitForDone(t, 2*time.Second)
	got := waitForRowStatus(t, st, "j-rf", "failure", 2*time.Second)
	if got.LastError == "" {
		t.Error("last_error should mirror WorkerRun.Error")
	}
}

// TestSchedulerWorkerKindAdvancesBeforeRunnerCompletes proves the
// async dispatch contract: when the runner blocks for an extended
// period (e.g. an LLM call), RunOnce — and by extension fire() —
// MUST return before the runner has finished. Without this, a single
// slow worker would stall every other scheduled job on the heap.
//
// Test mechanic: the fake executor blocks on a channel until the test
// closes it. RunOnce returns; we then assert the ScheduledJob row is
// in "running" status (set by fire()) and not yet "success" (which
// would mean the goroutine finalised). After we unblock the runner,
// we wait for the terminal status.
func TestSchedulerWorkerKindAdvancesBeforeRunnerCompletes(t *testing.T) {
	st := newMemStore()
	blockCh := make(chan struct{})
	startCh := make(chan struct{})
	wexec := &fakeWorkerExec{
		runID:   "run-slow",
		done:    make(chan struct{}),
		blockCh: blockCh,
		startCh: startCh,
	}
	wstore := newFakeWorkerStore()
	wstore.workers["wkr-slow"] = &store.Worker{ID: "wkr-slow", Enabled: true}
	wstore.runs["run-slow"] = &store.WorkerRun{ID: "run-slow", WorkerID: "wkr-slow", Status: "success"}
	s := newWorkerTestScheduler(t, st, wexec, wstore)
	seedWorkerJob(t, st, "j-slow", "wkr-slow", s.clock.Now().Add(time.Second))

	runOnceReturned := make(chan struct{})
	go func() {
		_ = s.RunOnce(context.Background(), "j-slow")
		close(runOnceReturned)
	}()

	// RunOnce must return within a short window even though the
	// runner is blocked. A 1s timeout is generous — async dispatch
	// should be effectively instant.
	select {
	case <-runOnceReturned:
	case <-time.After(time.Second):
		t.Fatalf("RunOnce did not return — scheduler is blocking on the runner")
	}

	// Wait for the goroutine to actually enter Run before asserting
	// the row state (otherwise we race the goroutine reaching dispatch).
	select {
	case <-startCh:
	case <-time.After(time.Second):
		t.Fatalf("runner goroutine never reached Run")
	}

	got, err := st.GetScheduledJob(context.Background(), "j-slow")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if got.LastStatus != "running" {
		t.Fatalf("expected LastStatus=running before runner returned, got %q", got.LastStatus)
	}

	// Unblock the fake runner; assert the row eventually settles.
	close(blockCh)
	wexec.waitForDone(t, 2*time.Second)
	waitForRowStatus(t, st, "j-slow", "success", 2*time.Second)
}
