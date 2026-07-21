package admin_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// dispatchOrphanRunner is a runner whose RunWithOpts succeeds but
// deliberately creates NO run row — exactly the state a worker is left in
// when its process dies between DISPATCH and CreateWorkerRun. (The nil-runner
// stub in newTestService, by contrast, DOES insert a placeholder run row, so
// it can't reproduce the orphan.) runCalls is atomic so the sweep's
// "no runner call" invariant can be asserted without racing the dispatch
// goroutine.
type dispatchOrphanRunner struct {
	runCalls atomic.Int64
}

func (r *dispatchOrphanRunner) RunWithOpts(_ context.Context, _ string, _ runner.RunOpts) (string, error) {
	r.runCalls.Add(1)
	return "run-orphan-never-persisted", nil
}
func (r *dispatchOrphanRunner) RefreshRunCaps(string, *store.Worker) bool { return true }
func (r *dispatchOrphanRunner) Cancel(string, string) bool                { return false }

func delegateOrphan(t *testing.T, svc *admin.Service, wsID, scopeID string) admin.DelegationOutput {
	t.Helper()
	out, err := svc.Delegate(context.Background(), admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Dispatched but never ran.",
		TaskKind:            "coding",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	return out
}

// waitForRunnerCall blocks until the detached dispatch goroutine has hit
// the runner at least once, so the "no run row" state is settled before we
// snapshot the runner call count.
func waitForRunnerCall(t *testing.T, r *dispatchOrphanRunner) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.runCalls.Load() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dispatch goroutine never reached the runner")
}

// TestSweepOrphanedDispatches_NoRunRowPastGrace_ReapedAsOperationalFailure
// is the core fix: a worker created + enabled with NO run row, past the
// grace window, is reaped to a terminal dispatch FAILURE classified
// operational (0 tokens, adapter/launch shape) and attributed.
func TestSweepOrphanedDispatches_NoRunRowPastGrace_ReapedAsOperationalFailure(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	r := &dispatchOrphanRunner{}
	svc.SetRunnerForTest(r)
	ctx := context.Background()

	out := delegateOrphan(t, svc, wsID, scopeID)
	waitForRunnerCall(t, r) // settle the detached dispatch goroutine

	// Aggressive grace so the just-created orphan is past cutoff.
	n, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("SweepOrphanedDispatches: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)

	if got.Status != "failure" {
		t.Fatalf("status = %q, want failure (dispatched-but-never-ran must resolve terminal)", got.Status)
	}
	if got.Aggregate.Failure != 1 {
		t.Fatalf("aggregate failure = %d, want 1", got.Aggregate.Failure)
	}
	if got.Aggregate.Dispatched != 0 {
		t.Fatalf("aggregate dispatched = %d, want 0 (no longer stuck dispatched)", got.Aggregate.Dispatched)
	}
	// 0 tokens / 0 cost — the model never ran.
	if got.Aggregate.TotalTokens != 0 || got.Aggregate.CostUSD != 0 {
		t.Fatalf("tokens/cost = %d/%.4f, want 0/0", got.Aggregate.TotalTokens, got.Aggregate.CostUSD)
	}
	if len(got.Workers) != 1 || !got.Workers[0].DispatchFailed {
		t.Fatalf("worker DispatchFailed not set: %+v", got.Workers)
	}
	if !strings.Contains(got.Workers[0].DispatchError, "dispatched but never ran") {
		t.Fatalf("dispatch error = %q, want cause attribution", got.Workers[0].DispatchError)
	}

	// Operational classification: the reaped orphan is an OperationalFailure /
	// DispatchFailure, NOT a model-quality failure — so it cannot corrupt the
	// per-model quality average / capacity ranking.
	if len(got.ModelStats) != 1 {
		t.Fatalf("model stats = %d, want 1", len(got.ModelStats))
	}
	st := got.ModelStats[0]
	if st.OperationalFailures != 1 || st.DispatchFailures != 1 {
		t.Fatalf("operational/dispatch failures = %d/%d, want 1/1", st.OperationalFailures, st.DispatchFailures)
	}
	if st.QualityFailure != 0 {
		t.Fatalf("quality failures = %d, want 0 (launch failure is not a model-quality event)", st.QualityFailure)
	}
}

// TestSweepOrphanedDispatches_NoRunRowWithinGrace_NotReaped proves a
// slow-but-legitimate dispatch (no run row yet, but recently dispatched)
// is never falsely reaped — the grace window protects it.
func TestSweepOrphanedDispatches_NoRunRowWithinGrace_NotReaped(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	r := &dispatchOrphanRunner{}
	svc.SetRunnerForTest(r)
	ctx := context.Background()

	out := delegateOrphan(t, svc, wsID, scopeID)
	waitForRunnerCall(t, r) // settle the detached dispatch goroutine

	n, err := svc.SweepOrphanedDispatches(ctx, time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanedDispatches: %v", err)
	}
	if n != 0 {
		t.Fatalf("reaped = %d, want 0 (within grace must not be reaped)", n)
	}
	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Status != "dispatched" {
		t.Fatalf("status = %q, want dispatched (still pending within grace)", got.Status)
	}
	if got.Workers[0].DispatchFailed {
		t.Fatal("worker must not be flagged DispatchFailed within grace")
	}
}

// TestSweepOrphanedDispatches_RunningWorker_NotDoubleReaped confirms a
// worker that DID reach the runner (has a run row) is left to the existing
// ResumeOrphanedDelegations path — never touched by this sweep, even with
// an aggressive grace.
func TestSweepOrphanedDispatches_RunningWorker_NotDoubleReaped(t *testing.T) {
	// No runner set → newTestService's stub path inserts a status="running"
	// run row, modelling a worker that reached the runner.
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out := delegateOrphan(t, svc, wsID, scopeID)
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if run.Status != "running" {
		t.Fatalf("seed run status = %q, want running", run.Status)
	}

	n, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("SweepOrphanedDispatches: %v", err)
	}
	if n != 0 {
		t.Fatalf("reaped = %d, want 0 (running worker owned by existing path)", n)
	}

	after, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkerRun: %v", err)
	}
	if after.Status != "running" {
		t.Fatalf("run status after sweep = %q, want running (untouched)", after.Status)
	}
	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Workers[0].DispatchFailed {
		t.Fatal("running worker must not be flagged DispatchFailed")
	}
}

// TestSweepOrphanedDispatches_Idempotent reaps a fresh orphan, then re-runs
// the sweep and asserts the second pass is a no-op (already terminal).
func TestSweepOrphanedDispatches_Idempotent(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	r := &dispatchOrphanRunner{}
	svc.SetRunnerForTest(r)
	ctx := context.Background()

	delegateOrphan(t, svc, wsID, scopeID)
	waitForRunnerCall(t, r) // settle the detached dispatch goroutine

	first, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if first != 1 {
		t.Fatalf("first reaped = %d, want 1", first)
	}
	second, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second != 0 {
		t.Fatalf("second reaped = %d, want 0 (idempotent — already dispatch-failed)", second)
	}
}

// TestSweepOrphanedDispatches_IgnoresNonDelegateWorkers guards the blast
// radius: a plain scheduled worker with no run row is never reaped.
func TestSweepOrphanedDispatches_IgnoresNonDelegateWorkers(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, admin.CreateInput{
		Name:           "not-a-delegate",
		ModelProvider:  "anthropic",
		ModelID:        "claude-sonnet-4-5",
		SecretScopeID:  scopeID,
		PromptTemplate: "Do work.",
		ScheduleSpec:   "manual",
		WorkspaceID:    wsID,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	n, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("SweepOrphanedDispatches: %v", err)
	}
	if n != 0 {
		t.Fatalf("reaped = %d, want 0 (non-delegate worker must be ignored)", n)
	}
}

// TestSweepOrphanedDispatches_NoModelCallInPath proves the reap path is
// deterministic — it never invokes the runner (no model turn). The runner
// call count is unchanged across the sweep.
func TestSweepOrphanedDispatches_NoModelCallInPath(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	r := &dispatchOrphanRunner{}
	svc.SetRunnerForTest(r)
	ctx := context.Background()

	delegateOrphan(t, svc, wsID, scopeID)
	waitForRunnerCall(t, r) // let the dispatch goroutine settle
	before := r.runCalls.Load()

	if _, err := svc.SweepOrphanedDispatches(ctx, time.Nanosecond); err != nil {
		t.Fatalf("SweepOrphanedDispatches: %v", err)
	}
	if after := r.runCalls.Load(); after != before {
		t.Fatalf("runner calls = %d after sweep, want %d (sweep must not invoke the model)", after, before)
	}
}
