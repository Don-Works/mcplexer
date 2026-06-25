package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// chanRunner signals each RunWithOpts call on a buffered channel so the
// async resume dispatch can be observed race-free.
type chanRunner struct {
	calls chan runner.RunOpts
}

func (c *chanRunner) RunWithOpts(_ context.Context, _ string, opts runner.RunOpts) (string, error) {
	c.calls <- opts
	return "run-resumed", nil
}

func (c *chanRunner) Cancel(_, _ string) bool { return false }

func (c *chanRunner) RefreshRunCaps(_ string, _ *store.Worker) bool { return false }

func seedDelegationRun(
	t *testing.T, db *sqlite.DB, wsID, scopeID, name, triggerKind string, enabled bool,
) string {
	t.Helper()
	ctx := context.Background()
	w := &store.Worker{
		Name:           name,
		ModelProvider:  "opencode_cli",
		ModelID:        "zai-coding-plan/glm-5.1",
		SecretScopeID:  scopeID,
		PromptTemplate: "x",
		ScheduleSpec:   "manual",
		WorkspaceID:    wsID,
		Enabled:        enabled,
	}
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker %q: %v", name, err)
	}
	run := &store.WorkerRun{WorkerID: w.ID, Status: "running", TriggerKind: triggerKind}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run for %q: %v", name, err)
	}
	return run.ID
}

// TestResumeOrphanedDelegations exercises the three boot-resume decisions
// in a single pass: an enabled delegation run with a non-resume trigger is
// re-dispatched (TriggerKind="resume"); an already-resumed run is guarded
// (1-generation crash-loop cap); a disabled worker is skipped. Every
// orphaned run is finalised to "interrupted" regardless of the decision.
func TestResumeOrphanedDelegations(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	cr := &chanRunner{calls: make(chan runner.RunOpts, 8)}
	svc.SetRunnerForTest(cr)

	manualRun := seedDelegationRun(t, db, wsID, scopeID, "delegate-manual", "manual", true)
	resumeRun := seedDelegationRun(t, db, wsID, scopeID, "delegate-resumed", "resume", true)
	disabledRun := seedDelegationRun(t, db, wsID, scopeID, "delegate-disabled", "manual", false)

	n, err := svc.ResumeOrphanedDelegations(context.Background())
	if err != nil {
		t.Fatalf("ResumeOrphanedDelegations: %v", err)
	}
	// Only the enabled, non-resume run is re-dispatched.
	if n != 1 {
		t.Fatalf("resumed = %d, want 1 (guard must skip the already-resumed and disabled runs)", n)
	}

	// Every orphaned run is finalised, whatever the dispatch decision.
	for _, id := range []string{manualRun, resumeRun, disabledRun} {
		r, gerr := db.GetWorkerRun(context.Background(), id)
		if gerr != nil {
			t.Fatalf("get run %s: %v", id, gerr)
		}
		if r.Status != "interrupted" {
			t.Errorf("run %s status = %q, want interrupted (orphan must be finalised)", id, r.Status)
		}
	}

	// The single dispatch must carry TriggerKind="resume" so a second
	// restart hits the crash-loop guard.
	select {
	case opts := <-cr.calls:
		if opts.TriggerKind != "resume" {
			t.Errorf("dispatched TriggerKind = %q, want resume", opts.TriggerKind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected one re-dispatch within 3s, got none")
	}

	// No further dispatch should arrive (the return count already proves
	// only one, but confirm the guard/disabled paths stayed silent).
	select {
	case opts := <-cr.calls:
		t.Errorf("unexpected extra dispatch: %+v", opts)
	case <-time.After(200 * time.Millisecond):
	}
}
