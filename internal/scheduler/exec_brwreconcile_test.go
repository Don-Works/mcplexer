package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeBrwExec records every Reconcile call and returns the configured
// result/err so dispatch wiring can be asserted without a real brwctl or db.
type fakeBrwExec struct {
	calls  atomic.Int32
	result BrwReconcileResult
	err    error
}

func (f *fakeBrwExec) Reconcile(_ context.Context, _ time.Time) (BrwReconcileResult, error) {
	f.calls.Add(1)
	if f.err != nil {
		return BrwReconcileResult{}, f.err
	}
	return f.result, nil
}

// The interval-fallback job (kind=interval) carrying the brw sentinel command
// must route to the wired executor, NOT exec the command, and emit a trailing
// brw.reconciled audit row.
func TestSchedulerBrwReconcile_IntervalDispatches(t *testing.T) {
	st := newMemStore()
	bexec := &fakeBrwExec{result: BrwReconcileResult{Daemons: 2, Created: 4}}
	aud := &fakeAuditor{}
	clk := newFakeClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, aud, clk)
	s.SetBrwReconcileExecutor(bexec)

	next := clk.Now().Add(time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "brw-interval",
		Name:      "brw_reconcile_interval",
		Kind:      KindInterval,
		Spec:      "5m",
		Command:   BrwReconcileCommand,
		Surface:   "schedule",
		Enabled:   true,
		NextRunAt: &next,
	})

	if err := s.RunOnce(context.Background(), "brw-interval"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if bexec.calls.Load() != 1 {
		t.Fatalf("reconcile calls = %d, want 1", bexec.calls.Load())
	}
	got, _ := st.GetScheduledJob(context.Background(), "brw-interval")
	if got.LastStatus != "success" {
		t.Errorf("last_status = %q, want success", got.LastStatus)
	}
	if got.NextRunAt == nil {
		t.Errorf("next_run_at not advanced after fire")
	}
	var found bool
	for _, r := range aud.records() {
		if r.ToolName == "brw.reconciled" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a brw.reconciled audit row")
	}
}

// The file_watch job (kind=file_watch, spec=policy path) carrying the same
// sentinel command must also route to the executor — this is the path the
// FileWatcher fires via RunOnce when the policy file changes.
func TestSchedulerBrwReconcile_FileWatchDispatches(t *testing.T) {
	st := newMemStore()
	bexec := &fakeBrwExec{}
	clk := newFakeClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, nil, clk)
	s.SetBrwReconcileExecutor(bexec)

	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:      "brw-watch",
		Name:    "brw_reconcile_watch",
		Kind:    KindFileWatch,
		Spec:    "/tmp/brw/browser-profiles.json",
		Command: BrwReconcileCommand,
		Surface: "schedule",
		Enabled: true,
		// file_watch jobs have no NextRunAt — fired by the FileWatcher.
	})

	if err := s.RunOnce(context.Background(), "brw-watch"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if bexec.calls.Load() != 1 {
		t.Fatalf("reconcile calls = %d, want 1", bexec.calls.Load())
	}
	got, _ := st.GetScheduledJob(context.Background(), "brw-watch")
	if got.LastStatus != "success" {
		t.Errorf("last_status = %q, want success", got.LastStatus)
	}
}

// A sentinel-command job with no executor wired surfaces a failure status —
// it must never silently exec the magic command string.
func TestSchedulerBrwReconcile_ExecutorUnwired(t *testing.T) {
	st := newMemStore()
	clk := newFakeClock(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, nil, clk)
	// No SetBrwReconcileExecutor.

	next := clk.Now().Add(time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "brw-noexec",
		Name:      "brw_reconcile_interval",
		Kind:      KindInterval,
		Spec:      "5m",
		Command:   BrwReconcileCommand,
		Surface:   "schedule",
		Enabled:   true,
		NextRunAt: &next,
	})

	if err := s.RunOnce(context.Background(), "brw-noexec"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, _ := st.GetScheduledJob(context.Background(), "brw-noexec")
	if got.LastStatus != statusFailure {
		t.Errorf("last_status = %q, want %q", got.LastStatus, statusFailure)
	}
}
