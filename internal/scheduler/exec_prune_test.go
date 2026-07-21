package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakePruneExec records every Prune call and returns the configured
// counts. err is non-nil to drive the failure-path test.
type fakePruneExec struct {
	calls     atomic.Int32
	auditN    int64
	runsN     int64
	err       error
	gotPolicy PrunePolicy
	gotNow    time.Time
}

func (f *fakePruneExec) Prune(_ context.Context, p PrunePolicy, now time.Time) (int64, int64, error) {
	f.calls.Add(1)
	f.gotPolicy = p
	f.gotNow = now
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.auditN, f.runsN, nil
}

// fakeAuditor records every AuditRecord written. Drives the
// "audit.pruned" emission assertion.
type fakeAuditor struct {
	mu    atomic.Pointer[[]store.AuditRecord]
	calls atomic.Int32
}

func (a *fakeAuditor) Record(_ context.Context, r *store.AuditRecord) error {
	a.calls.Add(1)
	cur := a.mu.Load()
	var next []store.AuditRecord
	if cur != nil {
		next = append([]store.AuditRecord{}, *cur...)
	}
	next = append(next, *r)
	a.mu.Store(&next)
	return nil
}

func (a *fakeAuditor) records() []store.AuditRecord {
	cur := a.mu.Load()
	if cur == nil {
		return nil
	}
	return *cur
}

// TestNextRunAuditPruneUsesCron confirms the new KindAuditPrune uses
// cron-style spec parsing — i.e. the daemon-seeded "0 3 * * *" spec
// rolls forward to 03:00 UTC the next day.
func TestNextRunAuditPruneUsesCron(t *testing.T) {
	// 12:00 UTC on a Monday → next 0 3 * * * is 03:00 UTC the next day.
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	next, err := NextRun(KindAuditPrune, "0 3 * * *", base)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	want := time.Date(2026, 5, 21, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

// TestSchedulerAuditPruneKindDispatches confirms the audit_prune kind
// routes to the wired PruneExecutor, threads the policy through, and
// emits a trailing audit.pruned record.
func TestSchedulerAuditPruneKindDispatches(t *testing.T) {
	st := newMemStore()
	pexec := &fakePruneExec{auditN: 17, runsN: 5}
	aud := &fakeAuditor{}
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, aud, clk)
	s.SetPruneExecutor(pexec)
	pol := PrunePolicy{
		AuditRetentionDays:     90,
		WorkerRunKeepPerWorker: 1000,
		WorkerRunCapDays:       180,
	}
	s.SetPrunePolicy(&pol)

	next := clk.Now().Add(time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "audit-prune-test",
		Name:      "audit_prune",
		Kind:      KindAuditPrune,
		Spec:      "0 3 * * *",
		Surface:   "schedule",
		Enabled:   true,
		NextRunAt: &next,
	})

	if err := s.RunOnce(context.Background(), "audit-prune-test"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pexec.calls.Load() != 1 {
		t.Fatalf("prune executor calls = %d, want 1", pexec.calls.Load())
	}
	if pexec.gotPolicy.AuditRetentionDays != 90 {
		t.Errorf("policy AuditRetentionDays = %d, want 90", pexec.gotPolicy.AuditRetentionDays)
	}
	if pexec.gotPolicy.WorkerRunKeepPerWorker != 1000 {
		t.Errorf("policy WorkerRunKeepPerWorker = %d, want 1000", pexec.gotPolicy.WorkerRunKeepPerWorker)
	}

	got, _ := st.GetScheduledJob(context.Background(), "audit-prune-test")
	if got.LastStatus != "success" {
		t.Errorf("last_status = %q, want success", got.LastStatus)
	}
	if got.NextRunAt == nil {
		t.Errorf("next_run_at not advanced after fire")
	}

	// The fire() path also records its own generic schedule:<name>
	// audit; we expect one of the recorded rows to be the dedicated
	// audit.pruned trace with our counts.
	var found bool
	for _, r := range aud.records() {
		if r.ToolName == "audit.pruned" {
			found = true
			if r.Status != "success" {
				t.Errorf("audit.pruned status = %q", r.Status)
			}
			if len(r.ParamsRedacted) == 0 {
				t.Errorf("audit.pruned params payload missing")
			}
			// L6: actor fields must be populated so the 053 actor
			// index catches "what did the scheduler do last week?"
			// queries. Without these the row is invisible to
			// actor_kind=scheduler filters.
			if r.ActorKind != "scheduler" {
				t.Errorf("audit.pruned ActorKind = %q, want scheduler", r.ActorKind)
			}
			if r.ActorID != "audit-prune-test" {
				t.Errorf("audit.pruned ActorID = %q, want audit-prune-test", r.ActorID)
			}
			// CorrelationID must be a fresh ULID per prune run so
			// the slog lines + this audit row can be joined.
			if r.CorrelationID == "" {
				t.Errorf("audit.pruned CorrelationID is empty; want a per-run ULID")
			}
			if len(r.CorrelationID) != 26 {
				t.Errorf("audit.pruned CorrelationID = %q (len=%d); want a 26-char ULID",
					r.CorrelationID, len(r.CorrelationID))
			}
		}
	}
	if !found {
		t.Errorf("audit.pruned record not emitted; got rows: %d", aud.calls.Load())
	}
}

// TestSchedulerAuditPruneNoExecutorFails confirms a missing wiring
// surfaces as last_status="failure" rather than crashing the
// scheduler loop.
func TestSchedulerAuditPruneNoExecutorFails(t *testing.T) {
	st := newMemStore()
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, nil, clk)
	// No SetPruneExecutor.

	next := clk.Now().Add(time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "no-exec",
		Name:      "audit_prune",
		Kind:      KindAuditPrune,
		Spec:      "0 3 * * *",
		Enabled:   true,
		NextRunAt: &next,
	})
	if err := s.RunOnce(context.Background(), "no-exec"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, _ := st.GetScheduledJob(context.Background(), "no-exec")
	if got.LastStatus != statusFailure {
		t.Errorf("last_status = %q, want failure", got.LastStatus)
	}
	if got.LastError == "" {
		t.Errorf("last_error should describe missing executor")
	}
}

// TestSchedulerAuditPruneExecutorErrorSurfaces wires an executor that
// returns an error and checks the scheduler row reflects failure
// without losing the underlying message.
func TestSchedulerAuditPruneExecutorErrorSurfaces(t *testing.T) {
	st := newMemStore()
	pexec := &fakePruneExec{err: errors.New("disk full")}
	clk := newFakeClock(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))
	s := New(st, nil, nil, clk)
	s.SetPruneExecutor(pexec)

	next := clk.Now().Add(time.Second)
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "fail",
		Name:      "audit_prune",
		Kind:      KindAuditPrune,
		Spec:      "0 3 * * *",
		Enabled:   true,
		NextRunAt: &next,
	})
	if err := s.RunOnce(context.Background(), "fail"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, _ := st.GetScheduledJob(context.Background(), "fail")
	if got.LastStatus != statusFailure {
		t.Errorf("last_status = %q, want failure", got.LastStatus)
	}
	if got.LastError == "" || !contains(got.LastError, "disk full") {
		t.Errorf("last_error = %q, want to mention 'disk full'", got.LastError)
	}
}

// TestDefaultPrunePolicy locks in the documented defaults so a
// well-meaning constant tweak doesn't silently change retention.
func TestDefaultPrunePolicy(t *testing.T) {
	p := DefaultPrunePolicy()
	if p.AuditRetentionDays != 90 {
		t.Errorf("AuditRetentionDays = %d, want 90", p.AuditRetentionDays)
	}
	if p.WorkerRunKeepPerWorker != 1000 {
		t.Errorf("WorkerRunKeepPerWorker = %d, want 1000", p.WorkerRunKeepPerWorker)
	}
	if p.WorkerRunCapDays != 180 {
		t.Errorf("WorkerRunCapDays = %d, want 180", p.WorkerRunCapDays)
	}
}

// contains is a tiny strings.Contains shim — pulling the real package
// in just for this trivial check isn't worth the import noise.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
