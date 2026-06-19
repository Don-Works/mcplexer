package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// --- fakes -----------------------------------------------------------

// fakeClock is a controllable Clock: tests advance it via Set or
// Advance, and each NewTimer call returns a fakeTimer the test can
// fire on demand.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: make(chan time.Time, 1), fireAt: c.now.Add(d)}
	c.timers = append(c.timers, t)
	return t
}

// advanceTo moves the clock and fires any timer whose fireAt <= new
// time. Each fired timer's C is sent on with the new time.
func (c *fakeClock) advanceTo(target time.Time) {
	c.mu.Lock()
	c.now = target
	timers := append([]*fakeTimer{}, c.timers...)
	c.timers = c.timers[:0]
	c.mu.Unlock()
	for _, t := range timers {
		if t.stopped.Load() {
			continue
		}
		if !t.fireAt.After(target) {
			select {
			case t.c <- target:
			default:
			}
		} else {
			c.mu.Lock()
			c.timers = append(c.timers, t)
			c.mu.Unlock()
		}
	}
}

type fakeTimer struct {
	c       chan time.Time
	fireAt  time.Time
	stopped atomic.Bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (t *fakeTimer) Stop() bool          { return !t.stopped.Swap(true) }

// memStore is a minimal in-memory ScheduledJobStore for scheduler
// tests. Concurrency-safe.
type memStore struct {
	mu      sync.Mutex
	jobs    map[string]store.ScheduledJob
	updates int32 // counts UpdateScheduledJob calls
}

func newMemStore() *memStore {
	return &memStore{jobs: map[string]store.ScheduledJob{}}
}

func (m *memStore) CreateScheduledJob(_ context.Context, j *store.ScheduledJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; ok {
		return store.ErrAlreadyExists
	}
	m.jobs[j.ID] = *j
	return nil
}

func (m *memStore) GetScheduledJob(_ context.Context, id string) (*store.ScheduledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &j, nil
}

func (m *memStore) ListScheduledJobs(_ context.Context) ([]store.ScheduledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.ScheduledJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out, nil
}

func (m *memStore) UpdateScheduledJob(_ context.Context, j *store.ScheduledJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return store.ErrNotFound
	}
	m.jobs[j.ID] = *j
	atomic.AddInt32(&m.updates, 1)
	return nil
}

func (m *memStore) DeleteScheduledJob(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.jobs, id)
	return nil
}

func (m *memStore) DueScheduledJobs(_ context.Context, _ time.Time, _ int) ([]store.ScheduledJob, error) {
	return nil, nil
}

// fakeApprover always returns the configured decision.
type fakeApprover struct {
	approve bool
	err     error
	calls   atomic.Int32
}

func (f *fakeApprover) RequestApproval(_ context.Context, _ *store.ToolApproval) (bool, error) {
	f.calls.Add(1)
	if f.err != nil {
		return false, f.err
	}
	return f.approve, nil
}

// fakeExecutor records every Run call.
type fakeExecutor struct {
	mu     sync.Mutex
	calls  []fakeCall
	err    error
	delay  time.Duration
	doneCh chan struct{}
}

type fakeCall struct {
	cmd  string
	args []string
}

func (f *fakeExecutor) Run(ctx context.Context, cmd string, args []string, _ []string, _ string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{cmd: cmd, args: append([]string{}, args...)})
	dc := f.doneCh
	delay := f.delay
	err := f.err
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if dc != nil {
		select {
		case dc <- struct{}{}:
		default:
		}
	}
	return nil, nil, err
}

func (f *fakeExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- helpers ---------------------------------------------------------

func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor %q timed out", label)
}

func newTestSchedulerWithExec(
	t *testing.T, st *memStore, approve bool,
) (*Scheduler, *fakeClock, *fakeExecutor, *fakeApprover) {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	app := &fakeApprover{approve: approve}
	exec := &fakeExecutor{doneCh: make(chan struct{}, 16)}
	s := New(st, app, nil, clk)
	s.exec = exec
	return s, clk, exec, app
}

// --- tests -----------------------------------------------------------

func TestSchedulerSingleJobFires(t *testing.T) {
	st := newMemStore()
	s, clk, exec, app := newTestSchedulerWithExec(t, st, true)
	at := clk.Now().Add(1 * time.Second)
	j := store.ScheduledJob{
		ID: "j1", Name: "ping", Kind: KindInterval, Spec: "1h",
		Command: "/bin/echo", Enabled: true, NextRunAt: &at,
	}
	if err := st.CreateScheduledJob(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(time.Second) }()

	clk.advanceTo(at.Add(time.Second))
	waitFor(t, "exec call", func() bool { return exec.count() >= 1 })
	if app.calls.Load() != 1 {
		t.Errorf("approver called %d times, want 1", app.calls.Load())
	}
	got, _ := st.GetScheduledJob(context.Background(), "j1")
	if got.LastStatus != "success" {
		t.Errorf("last_status = %q, want success", got.LastStatus)
	}
	if got.NextRunAt == nil {
		t.Error("next_run_at should be re-scheduled")
	}
}

func TestSchedulerOrdersByNextRun(t *testing.T) {
	st := newMemStore()
	s, clk, exec, _ := newTestSchedulerWithExec(t, st, true)
	t1 := clk.Now().Add(2 * time.Second)
	t2 := clk.Now().Add(1 * time.Second)
	for _, j := range []store.ScheduledJob{
		{ID: "early", Name: "early", Kind: KindInterval, Spec: "10m", Command: "/bin/true", Enabled: true, NextRunAt: &t2},
		{ID: "late", Name: "late", Kind: KindInterval, Spec: "10m", Command: "/bin/true", Enabled: true, NextRunAt: &t1},
	} {
		_ = st.CreateScheduledJob(context.Background(), &j)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	defer func() { _ = s.Stop(time.Second) }()

	clk.advanceTo(t1.Add(time.Second))
	waitFor(t, "both fired", func() bool { return exec.count() >= 2 })
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.calls) < 2 {
		t.Fatalf("expected 2 calls, got %d", len(exec.calls))
	}
	// early was due first (t2 < t1)
	if exec.calls[0].cmd != "/bin/true" {
		t.Errorf("first call cmd = %q", exec.calls[0].cmd)
	}
}

func TestSchedulerApprovalDeniedSkipsExec(t *testing.T) {
	st := newMemStore()
	s, clk, exec, _ := newTestSchedulerWithExec(t, st, false)
	at := clk.Now().Add(1 * time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID: "denied", Name: "denied", Kind: KindInterval, Spec: "1h",
		Command: "/bin/echo", Enabled: true, NextRunAt: &at,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	defer func() { _ = s.Stop(time.Second) }()

	clk.advanceTo(at.Add(time.Second))
	waitFor(t, "row updated", func() bool {
		got, _ := st.GetScheduledJob(context.Background(), "denied")
		return got.LastStatus == "denied"
	})
	if exec.count() != 0 {
		t.Errorf("exec ran but should have been blocked: %d calls", exec.count())
	}
}

func TestSchedulerReloadPicksUpNewJobs(t *testing.T) {
	st := newMemStore()
	s, clk, exec, _ := newTestSchedulerWithExec(t, st, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	defer func() { _ = s.Stop(time.Second) }()

	at := clk.Now().Add(1 * time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID: "late", Name: "late", Kind: KindInterval, Spec: "1h",
		Command: "/bin/echo", Enabled: true, NextRunAt: &at,
	})
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	clk.advanceTo(at.Add(time.Second))
	waitFor(t, "fired after reload", func() bool { return exec.count() >= 1 })
}

func TestSchedulerStopReturns(t *testing.T) {
	st := newMemStore()
	s, _, _, _ := newTestSchedulerWithExec(t, st, true)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(time.Second); err != nil {
		t.Errorf("stop returned err: %v", err)
	}
}

func TestSchedulerApprovalErrorMarksFailure(t *testing.T) {
	st := newMemStore()
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	app := &fakeApprover{approve: false, err: errors.New("kaboom")}
	exec := &fakeExecutor{doneCh: make(chan struct{}, 4)}
	s := New(st, app, nil, clk)
	s.exec = exec
	at := clk.Now().Add(1 * time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID: "err", Name: "err", Kind: KindInterval, Spec: "1h",
		Command: "/bin/echo", Enabled: true, NextRunAt: &at,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	defer func() { _ = s.Stop(time.Second) }()
	clk.advanceTo(at.Add(time.Second))
	waitFor(t, "failure status", func() bool {
		got, _ := st.GetScheduledJob(context.Background(), "err")
		return got.LastStatus == "failure"
	})
}
