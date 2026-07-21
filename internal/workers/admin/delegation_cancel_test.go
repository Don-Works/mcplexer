package admin_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	admin "github.com/don-works/mcplexer/internal/workers/admin"
)

func delegationWorker(provider, modelID, status string, cost float64) admin.DelegationWorkerContext {
	return admin.DelegationWorkerContext{
		Worker: &store.Worker{ModelProvider: provider, ModelID: modelID},
		LatestRun: &store.WorkerRun{
			ModelProvider: provider,
			ModelID:       modelID,
			Status:        status,
			CostUSD:       cost,
			DurationMS:    1000,
		},
	}
}

// TestAggregateDelegation_CancelledNotAFailure proves an operator
// hard-stop is excluded from the delegation Failure count and never
// flips a delegation into failure / needs_review.
func TestAggregateDelegation_CancelledNotAFailure(t *testing.T) {
	t.Run("all cancelled → terminal cancelled, zero failures", func(t *testing.T) {
		d := admin.DelegationContext{
			ReviewRequired: true, // would force needs_review if cancelled counted as failure
			Workers: []admin.DelegationWorkerContext{
				delegationWorker("openrouter", "x", "cancelled", 0.01),
			},
		}
		agg, status := admin.AggregateDelegationForTest(d)
		if agg.Failure != 0 {
			t.Fatalf("Failure = %d, want 0 (cancelled is not a failure)", agg.Failure)
		}
		if agg.Cancelled != 1 {
			t.Fatalf("Cancelled = %d, want 1", agg.Cancelled)
		}
		if status != "cancelled" {
			t.Fatalf("status = %q, want cancelled", status)
		}
	})

	t.Run("success + cancelled → partial, not failure", func(t *testing.T) {
		d := admin.DelegationContext{
			Workers: []admin.DelegationWorkerContext{
				delegationWorker("anthropic", "a", "success", 0.02),
				delegationWorker("openrouter", "b", "cancelled", 0.01),
			},
		}
		agg, status := admin.AggregateDelegationForTest(d)
		if agg.Failure != 0 {
			t.Fatalf("Failure = %d, want 0", agg.Failure)
		}
		if status != "partial" {
			t.Fatalf("status = %q, want partial", status)
		}
	})
}

// TestModelStats_CancelledExcludedFromRank proves a cancelled run does
// not perturb a model's rank stats: it counts toward neither Runs,
// Success, nor Failure, so an operator cancel can't penalise (or flatter)
// the model the ranker prefers.
func TestModelStats_CancelledExcludedFromRank(t *testing.T) {
	d := admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{
			delegationWorker("openrouter", "m", "cancelled", 0.05),
		},
	}
	stats := admin.ModelStatsForDelegationForTest(d)
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.Failure != 0 || s.Runs != 0 || s.Success != 0 {
		t.Fatalf("cancelled run leaked into rank stats: runs=%d success=%d failure=%d", s.Runs, s.Success, s.Failure)
	}
	if s.Cancelled != 1 {
		t.Fatalf("Cancelled = %d, want 1", s.Cancelled)
	}
}

// TestAggregateDelegation_InterruptedNotAFailure proves daemon restart
// casualties are not treated as model failures or review-gated worker
// output.
func TestAggregateDelegation_InterruptedNotAFailure(t *testing.T) {
	d := admin.DelegationContext{
		ReviewRequired: true,
		Workers: []admin.DelegationWorkerContext{
			delegationWorker("opencode_cli", "minimax/MiniMax-M3", "interrupted", 0),
		},
	}
	agg, status := admin.AggregateDelegationForTest(d)
	if agg.Failure != 0 {
		t.Fatalf("Failure = %d, want 0 (interrupted is not a model failure)", agg.Failure)
	}
	if agg.Interrupted != 1 {
		t.Fatalf("Interrupted = %d, want 1", agg.Interrupted)
	}
	if status != "interrupted" {
		t.Fatalf("status = %q, want interrupted", status)
	}
}

func TestModelStats_InterruptedExcludedFromRank(t *testing.T) {
	d := admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{
			delegationWorker("opencode_cli", "minimax/MiniMax-M3", "interrupted", 0),
		},
	}
	stats := admin.ModelStatsForDelegationForTest(d)
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.Failure != 0 || s.Runs != 0 || s.Success != 0 {
		t.Fatalf("interrupted run leaked into rank stats: runs=%d success=%d failure=%d", s.Runs, s.Success, s.Failure)
	}
	if s.Interrupted != 1 {
		t.Fatalf("Interrupted = %d, want 1", s.Interrupted)
	}
}
