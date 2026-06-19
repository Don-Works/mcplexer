package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ctxAwareExec is a WorkerExecutor whose Run blocks until its context is
// cancelled (or a hard ceiling elapses), then reports back. It exists to
// drive the derived-context-expiry path: pair it with a tiny
// workerRunTimeout and Run will observe ctx.Err() != nil, exactly as a
// real runner that blew past its wall-clock cap would.
type ctxAwareExec struct {
	done    chan struct{}
	sawDone chan struct{} // closed if Run returned because ctx expired
}

func newCtxAwareExec() *ctxAwareExec {
	return &ctxAwareExec{done: make(chan struct{}), sawDone: make(chan struct{})}
}

func (e *ctxAwareExec) Run(ctx context.Context, _ string) (string, error) {
	select {
	case <-ctx.Done():
		close(e.sawDone)
		close(e.done)
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
		// Safety ceiling so a logic bug can't hang the suite.
		close(e.done)
		return "run-ceiling", nil
	}
}

func (e *ctxAwareExec) waitForDone(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-e.done:
	case <-time.After(timeout):
		t.Fatalf("ctxAwareExec.Run never returned")
	}
}

// recordingAuditor captures terminal audit rows so the tests can assert
// the scheduler emits the true outcome and never leaves the audit trail
// stuck at "running" for an async worker fire.
type recordingAuditor struct {
	mu      chan struct{} // 1-slot mutex
	records []store.AuditRecord
}

func newRecordingAuditor() *recordingAuditor {
	a := &recordingAuditor{mu: make(chan struct{}, 1)}
	a.mu <- struct{}{}
	return a
}

func (a *recordingAuditor) Record(_ context.Context, r *store.AuditRecord) error {
	<-a.mu
	a.records = append(a.records, *r)
	a.mu <- struct{}{}
	return nil
}

func (a *recordingAuditor) snapshot() []store.AuditRecord {
	<-a.mu
	out := append([]store.AuditRecord{}, a.records...)
	a.mu <- struct{}{}
	return out
}

// TestPersistTerminalRecoversFromExpiredContext is the regression guard
// for issue #1: when the worker goroutine's derived context expires
// (runner exceeded its wall-clock cap), persistTerminal MUST still write
// a terminal row instead of bailing — fire() already wrote
// LastStatus="running" optimistically and skips its own post-run write
// for the async path, so an early return leaves the row stuck at
// "running" forever with a nil NextRunAt.
func TestPersistTerminalRecoversFromExpiredContext(t *testing.T) {
	cases := []struct {
		name string
		spec string // re-arm spec on the seeded job
	}{
		{name: "cron spec re-arms", spec: "* * * * *"},
		{name: "interval spec re-arms", spec: "5m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newMemStore()
			wexec := newCtxAwareExec()
			wstore := newFakeWorkerStore()
			wstore.workers["wkr-exp"] = &store.Worker{ID: "wkr-exp", Enabled: true}
			s := newWorkerTestScheduler(t, st, wexec, wstore)
			// Tiny outer cap so Run's ctx expires almost immediately.
			s.mu.Lock()
			s.workerRunTimeout = 20 * time.Millisecond
			s.mu.Unlock()

			at := s.clock.Now().Add(time.Second)
			if err := st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
				ID: "j-exp", Name: "j-exp", Kind: KindWorker, Spec: c.spec,
				WorkerID: "wkr-exp", Enabled: true, NextRunAt: &at,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if err := s.RunOnce(context.Background(), "j-exp"); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			wexec.waitForDone(t, 2*time.Second)

			// The row must NOT remain stuck at "running": it settles to a
			// terminal failure that explains the abort.
			got := waitForRowStatus(t, st, "j-exp", statusFailure, 2*time.Second)
			if got.LastError == "" {
				t.Error("last_error should describe the timeout/abort")
			}
			// And the heap/DB stay consistent: NextRunAt is re-armed.
			if got.NextRunAt == nil {
				t.Fatal("NextRunAt must be re-armed after an expired-context finalise, got nil")
			}
			want, err := NextRun(KindWorker, c.spec, s.clock.Now())
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.NextRunAt.Equal(want) {
				t.Errorf("NextRunAt = %v, want %v", got.NextRunAt, want)
			}
		})
	}
}

// TestWorkerFireEmitsTerminalAudit is the regression guard for issue #2:
// a scheduled worker fire must emit a TERMINAL audit row carrying the
// true outcome. Previously the only audit row for an async worker fire
// was the optimistic status="running" one written by fire(); the
// goroutine never re-recorded the real result, so the audit trail lied.
func TestWorkerFireEmitsTerminalAudit(t *testing.T) {
	cases := []struct {
		name       string
		runID      string
		runErr     error
		run        *store.WorkerRun
		wantStatus string
	}{
		{
			name:       "success run records success audit",
			runID:      "run-ok",
			run:        &store.WorkerRun{ID: "run-ok", WorkerID: "wkr-a", Status: "success"},
			wantStatus: "success",
		},
		{
			name:       "failed run records failure audit",
			runID:      "run-bad",
			run:        &store.WorkerRun{ID: "run-bad", WorkerID: "wkr-a", Status: "failure", Error: "tool denied"},
			wantStatus: "failure",
		},
		{
			name:       "runner error records failure audit",
			runErr:     errors.New("model dead"),
			wantStatus: statusFailure,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newMemStore()
			wexec := newFakeExec(c.runID, c.runErr)
			wstore := newFakeWorkerStore()
			wstore.workers["wkr-a"] = &store.Worker{ID: "wkr-a", Enabled: true}
			if c.run != nil {
				wstore.runs[c.runID] = c.run
			}
			aud := newRecordingAuditor()
			clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
			s := New(st, nil, aud, clk)
			s.SetWorkerExecutor(wexec)
			s.SetWorkerStore(wstore)
			seedWorkerJob(t, st, "j-aud", "wkr-a", clk.Now().Add(time.Second))

			if err := s.RunOnce(context.Background(), "j-aud"); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			wexec.waitForDone(t, 2*time.Second)
			waitForRowStatus(t, st, "j-aud", c.wantStatus, 2*time.Second)

			// Poll the audit log until a terminal row appears — the
			// terminal audit is emitted by the async goroutine after the
			// row writeback, so it races RunOnce's return.
			var terminal *store.AuditRecord
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && terminal == nil {
				recs := aud.snapshot()
				for i := range recs {
					r := recs[i]
					if r.ToolName == "schedule:j-aud" && r.Status == c.wantStatus {
						terminal = &r
						break
					}
				}
				if terminal == nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if terminal == nil {
				t.Fatalf("no terminal audit row with status %q; got %+v",
					c.wantStatus, auditStatuses(aud.snapshot()))
			}
			if c.wantStatus == "failure" || c.wantStatus == statusFailure {
				if terminal.ErrorMessage == "" {
					t.Error("terminal failure audit must carry an error message")
				}
			}
		})
	}
}

// TestWorkerFireReArmsNextRunAt is the regression guard for issue #3:
// the headline branch fix (NextRun resolving KindWorker) must survive
// the full fire()/persistTerminal write path — not just the pure NextRun
// unit. A worker fire must leave NextRunAt non-nil and equal to
// NextRun(kind, spec, now) so the cron re-arms instead of firing once.
func TestWorkerFireReArmsNextRunAt(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{name: "cron worker re-arms", spec: "*/5 * * * *"},
		{name: "interval worker re-arms", spec: "10m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newMemStore()
			wexec := newFakeExec("run-z", nil)
			wstore := newFakeWorkerStore()
			wstore.workers["wkr-z"] = &store.Worker{ID: "wkr-z", Enabled: true}
			wstore.runs["run-z"] = &store.WorkerRun{ID: "run-z", WorkerID: "wkr-z", Status: "success"}
			s := newWorkerTestScheduler(t, st, wexec, wstore)

			at := s.clock.Now().Add(time.Second)
			if err := st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
				ID: "j-z", Name: "j-z", Kind: KindWorker, Spec: c.spec,
				WorkerID: "wkr-z", Enabled: true, NextRunAt: &at,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if err := s.RunOnce(context.Background(), "j-z"); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			wexec.waitForDone(t, 2*time.Second)
			got := waitForRowStatus(t, st, "j-z", "success", 2*time.Second)

			if got.NextRunAt == nil {
				t.Fatal("NextRunAt nil after worker fire — cron would fire exactly once")
			}
			want, err := NextRun(KindWorker, c.spec, s.clock.Now())
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.NextRunAt.Equal(want) {
				t.Errorf("NextRunAt = %v, want %v", got.NextRunAt, want)
			}
		})
	}
}

func auditStatuses(recs []store.AuditRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ToolName+"="+r.Status)
	}
	return out
}
