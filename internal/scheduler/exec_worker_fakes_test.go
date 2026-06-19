package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeWorkerExec records every Run call and lets the test seed a
// (runID, err) tuple per worker. Concurrency-safe so the scheduler
// goroutine can poke it without races.
//
// done is closed on every Run return so tests can wait for the
// async dispatch to settle without sleep-and-pray.
type fakeWorkerExec struct {
	mu      sync.Mutex
	calls   []string
	runID   string
	err     error
	done    chan struct{}
	blockCh chan struct{} // when non-nil, Run blocks on it before returning
	startCh chan struct{} // when non-nil, Run closes it on entry
}

func (f *fakeWorkerExec) Run(_ context.Context, workerID string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, workerID)
	startCh, blockCh := f.startCh, f.blockCh
	f.mu.Unlock()
	if startCh != nil {
		// Signal the test that the goroutine reached Run before we
		// start blocking on the gate.
		select {
		case <-startCh:
		default:
			close(startCh)
		}
	}
	if blockCh != nil {
		<-blockCh
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done != nil {
		select {
		case <-f.done:
		default:
			close(f.done)
		}
	}
	return f.runID, f.err
}

func (f *fakeWorkerExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// waitForDone blocks until the async worker goroutine has finished
// executing the runner shim, or fails the test after timeout. Used in
// every assert-on-LastStatus test after the async refactor.
func (f *fakeWorkerExec) waitForDone(t *testing.T, timeout time.Duration) {
	t.Helper()
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	if done == nil {
		t.Fatalf("fakeWorkerExec.done channel not initialised")
	}
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for async worker dispatch")
	}
}

// waitForRowStatus polls the ScheduledJob row until LastStatus
// stabilises to want or the deadline elapses. The async finalise runs
// in a different goroutine than waitForDone, so the channel signal
// alone isn't enough — we also need the store write to land before
// asserting on LastStatus.
func waitForRowStatus(t *testing.T, st *memStore, jobID, want string, timeout time.Duration) *store.ScheduledJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := st.GetScheduledJob(context.Background(), jobID)
		if err == nil && got.LastStatus == want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := st.GetScheduledJob(context.Background(), jobID)
	if got != nil {
		t.Fatalf("row never reached %q: last_status=%q last_error=%q",
			want, got.LastStatus, got.LastError)
	}
	t.Fatalf("row never reached %q (and could not be read)", want)
	return nil
}

// newFakeExec constructs a fakeWorkerExec with the done-channel ready
// for waitForDone.
func newFakeExec(runID string, err error) *fakeWorkerExec {
	return &fakeWorkerExec{runID: runID, err: err, done: make(chan struct{})}
}

// fakeWorkerStore is a minimal WorkerLookup for the dispatch tests.
type fakeWorkerStore struct {
	mu        sync.Mutex
	workers   map[string]*store.Worker
	runs      map[string]*store.WorkerRun
	running   map[string]int
	getErr    error
	countErr  error
	runGetErr error
	gets      atomic.Int32
}

func newFakeWorkerStore() *fakeWorkerStore {
	return &fakeWorkerStore{
		workers: map[string]*store.Worker{},
		runs:    map[string]*store.WorkerRun{},
		running: map[string]int{},
	}
}

func (s *fakeWorkerStore) GetWorker(_ context.Context, id string) (*store.Worker, error) {
	s.gets.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	w, ok := s.workers[id]
	if !ok {
		return nil, store.ErrWorkerNotFound
	}
	return w, nil
}

func (s *fakeWorkerStore) GetWorkerRun(_ context.Context, id string) (*store.WorkerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runGetErr != nil {
		return nil, s.runGetErr
	}
	r, ok := s.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return r, nil
}

func (s *fakeWorkerStore) CountRunningWorkerRuns(_ context.Context, workerID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.running[workerID], nil
}

// newWorkerTestScheduler returns a scheduler ready to dispatch a
// kind=worker job via RunOnce. The scheduler is NOT started — we use
// the synchronous RunOnce path to avoid timer-driven flakes.
func newWorkerTestScheduler(
	t *testing.T,
	st *memStore,
	wexec WorkerExecutor,
	wstore WorkerLookup,
) *Scheduler {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, nil, clk)
	if wexec != nil {
		s.SetWorkerExecutor(wexec)
	}
	if wstore != nil {
		s.SetWorkerStore(wstore)
	}
	return s
}

// seedWorkerJob inserts a kind=worker ScheduledJob due at `at`.
func seedWorkerJob(t *testing.T, st *memStore, id, workerID string, at time.Time) {
	t.Helper()
	j := store.ScheduledJob{
		ID: id, Name: id, Kind: KindWorker, Spec: "1h",
		WorkerID: workerID, Enabled: true, NextRunAt: &at,
	}
	if err := st.CreateScheduledJob(context.Background(), &j); err != nil {
		t.Fatalf("seed scheduled job: %v", err)
	}
}
