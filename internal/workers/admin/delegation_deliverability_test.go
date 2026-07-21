package admin_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestDelegationWorkerPreservesUserPostHookWithoutInjectedSandboxGate(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	const userHook = `if (!String(hook.run.output || "").includes("STATUS:")) abort("missing status")`

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Return an auditable report.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		PostExecuteScript:   userHook,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if script := got.Worker.PostExecuteScript; script != userHook {
		t.Fatalf("delegation post hook = %q, want caller hook unchanged; empty-report validation is native", script)
	}
}

func TestDelegationAggregateTreatsBlockedAsTerminal(t *testing.T) {
	t.Run("all blocked", func(t *testing.T) {
		d := admin.DelegationContext{Workers: []admin.DelegationWorkerContext{
			delegationWorker("anthropic", "a", "blocked", 0),
		}}
		agg, status := admin.AggregateDelegationForTest(d)
		if agg.Blocked != 1 || agg.Failure != 0 {
			t.Fatalf("aggregate = %+v, want one blocked and no failure", agg)
		}
		if status != "blocked" {
			t.Fatalf("status = %q, want blocked", status)
		}
	})

	t.Run("success and blocked", func(t *testing.T) {
		d := admin.DelegationContext{Workers: []admin.DelegationWorkerContext{
			delegationWorker("anthropic", "a", "success", 0),
			delegationWorker("anthropic", "b", "blocked", 0),
		}}
		_, status := admin.AggregateDelegationForTest(d)
		if status != "partial" {
			t.Fatalf("status = %q, want partial", status)
		}
	})

	t.Run("review-required success and blocked", func(t *testing.T) {
		d := admin.DelegationContext{
			ReviewRequired: true,
			Workers: []admin.DelegationWorkerContext{
				delegationWorker("anthropic", "a", "success", 0),
				delegationWorker("anthropic", "b", "blocked", 0),
			},
		}
		_, status := admin.AggregateDelegationForTest(d)
		if status != "needs_review" {
			t.Fatalf("status = %q, want needs_review", status)
		}
	})

	blockedWith := func(errText string) admin.DelegationWorkerContext {
		w := delegationWorker("anthropic", "blocked", "blocked", 0)
		w.LatestRun.Error = errText
		return w
	}
	t.Run("review-required post-execute block", func(t *testing.T) {
		d := admin.DelegationContext{
			ReviewRequired: true,
			Workers: []admin.DelegationWorkerContext{
				blockedWith("post-execute deliverability gate blocked the run: empty final report"),
			},
		}
		_, status := admin.AggregateDelegationForTest(d)
		if status != "needs_review" {
			t.Fatalf("status = %q, want needs_review for attributable post-execute block", status)
		}
	})

	t.Run("review-required pre-execute blocks stay blocked", func(t *testing.T) {
		d := admin.DelegationContext{
			ReviewRequired: true,
			Workers: []admin.DelegationWorkerContext{
				blockedWith("pre-execute hook blocked the run: policy gate"),
				blockedWith("legacy policy block without phase provenance"),
			},
		}
		_, status := admin.AggregateDelegationForTest(d)
		if status != "blocked" {
			t.Fatalf("status = %q, want blocked when no model ran", status)
		}
	})

	t.Run("review-required mixed pre and post blocks need review", func(t *testing.T) {
		d := admin.DelegationContext{
			ReviewRequired: true,
			Workers: []admin.DelegationWorkerContext{
				blockedWith("pre-execute hook blocked the run: policy gate"),
				blockedWith("post-execute deliverability gate blocked the run: empty final report"),
			},
		}
		_, status := admin.AggregateDelegationForTest(d)
		if status != "needs_review" {
			t.Fatalf("status = %q, want needs_review when any model reached post-execute", status)
		}
	})
}

func TestDelegationWaitTreatsBlockedAndInterruptedAsTerminal(t *testing.T) {
	for _, status := range []string{"blocked", "interrupted"} {
		if !admin.DelegationStatusTerminalForTest(status) {
			t.Fatalf("status %q must terminate wait_for_delegation polling", status)
		}
	}
}

func TestDelegationAggregateDispatchFailureOverridesStaleRun(t *testing.T) {
	d := admin.DelegationContext{Workers: []admin.DelegationWorkerContext{{
		Worker:         delegationWorker("grok_cli", "stale", "success", 0).Worker,
		LatestRun:      &store.WorkerRun{Status: "success", InputTokens: 100, OutputTokens: 20},
		DispatchFailed: true,
	}}}
	agg, status := admin.AggregateDelegationForTest(d)
	if agg.Failure != 1 || agg.Success != 0 || agg.TotalTokens != 0 {
		t.Fatalf("aggregate = %+v, want authoritative dispatch failure with no stale-run usage", agg)
	}
	if status != "failure" {
		t.Fatalf("status = %q, want failure", status)
	}
}

func TestDelegationAggregateDoesNotFinishWhileSiblingIsPending(t *testing.T) {
	pending := admin.DelegationWorkerContext{Worker: &store.Worker{ModelProvider: "anthropic", ModelID: "pending"}}
	cases := []struct {
		name  string
		first admin.DelegationWorkerContext
	}{
		{name: "blocked and pending", first: delegationWorker("anthropic", "blocked", "blocked", 0)},
		{name: "dispatch failed and pending", first: admin.DelegationWorkerContext{
			Worker:         &store.Worker{ModelProvider: "grok_cli", ModelID: "failed-dispatch"},
			DispatchFailed: true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg, status := admin.AggregateDelegationForTest(admin.DelegationContext{
				Workers: []admin.DelegationWorkerContext{tc.first, pending},
			})
			if agg.Dispatched != 1 {
				t.Fatalf("dispatched = %d, want 1", agg.Dispatched)
			}
			if status != "dispatched" {
				t.Fatalf("status = %q, want nonterminal dispatched", status)
			}
		})
	}
}
