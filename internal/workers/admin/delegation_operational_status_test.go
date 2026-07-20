package admin_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// operationalRun is the launch-crash signature isOperationalFailure
// recognises: status=failure, zero tokens both ways, and the runner's
// canonical "adapter send: " error prefix. The model never produced a turn.
func operationalRun() *store.WorkerRun {
	return &store.WorkerRun{
		Status:       "failure",
		Error:        "adapter send: dial tcp 127.0.0.1:1234: connect: connection refused",
		InputTokens:  0,
		OutputTokens: 0,
	}
}

// genuineFailureRun died AFTER the model produced output — non-zero tokens
// mean there is real work for a parent to score.
func genuineFailureRun() *store.WorkerRun {
	return &store.WorkerRun{
		Status:       "failure",
		Error:        "tool loop exceeded max_tool_calls",
		InputTokens:  4200,
		OutputTokens: 900,
	}
}

func successRunCtx() *store.WorkerRun {
	return &store.WorkerRun{Status: "success", InputTokens: 3000, OutputTokens: 800}
}

// TestAggregateDelegation_OperationalFailureDoesNotGateNeedsReview covers the
// status gate at the failure branch. An all-operational delegation produced
// nothing for a parent to review — parking it in needs_review demands a
// quality score for a model that never ran a single turn, which is exactly
// what delegationIsOperationalOnly already prevents for model ranking.
func TestAggregateDelegation_OperationalFailureDoesNotGateNeedsReview(t *testing.T) {
	cases := []struct {
		name           string
		reviewRequired bool
		workers        []admin.DelegationWorkerContext
		want           string
	}{
		{
			// (a) The bug: every worker died at the adapter before the model
			// ran, so there is no quality event to review.
			name:           "all operational failures with review_required terminate as failure",
			reviewRequired: true,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun()},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: operationalRun()},
			},
			want: "failure",
		},
		{
			// (b) One worker genuinely failed after producing output — the
			// review gate still applies to that worker's result.
			name:           "mixed operational and genuine failure still honours the review gate",
			reviewRequired: true,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun()},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: genuineFailureRun()},
			},
			want: "needs_review",
		},
		{
			// (c) Unchanged behaviour: tokens > 0 means the model ran.
			name:           "genuine failure with review_required is unchanged",
			reviewRequired: true,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: genuineFailureRun()},
			},
			want: "needs_review",
		},
		{
			// (d) Unchanged behaviour: no gate to bypass in the first place.
			name:           "all operational failures without review_required is unchanged",
			reviewRequired: false,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun()},
			},
			want: "failure",
		},
		{
			// A success alongside an operational failure IS reviewable: the
			// model ran on the success worker.
			name:           "success plus operational failure honours the review gate",
			reviewRequired: true,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun()},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: successRunCtx()},
			},
			want: "needs_review",
		},
		{
			// Same shape as (a) but the operator's DispatchFailed flag is the
			// "never ran" signal instead of a run row. hasRun is false here,
			// so this already terminated as failure — locked in so the fix
			// cannot regress it.
			name:           "dispatch-failed workers terminate as failure",
			reviewRequired: true,
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun(), DispatchFailed: true},
			},
			want: "failure",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := admin.DelegationContext{
				ID:             "del-test",
				ReviewRequired: c.reviewRequired,
				Workers:        c.workers,
			}
			_, status := admin.AggregateDelegationForTest(d)
			if status != c.want {
				t.Fatalf("status = %q, want %q", status, c.want)
			}
		})
	}
}

// TestAggregateDelegation_ReviewedOperationalFailureStaysFailure guards the
// interaction with an already-recorded review: once reviewed, the gate is
// satisfied anyway, and an all-operational delegation must still read as a
// failure rather than flipping to some reviewed-success state.
func TestAggregateDelegation_ReviewedOperationalFailureStaysFailure(t *testing.T) {
	d := admin.DelegationContext{
		ID:             "del-test",
		ReviewRequired: true,
		Review:         admin.DelegationReview{Reviewed: true, Score: 1},
		Workers: []admin.DelegationWorkerContext{
			{Worker: &store.Worker{ID: "w1"}, LatestRun: operationalRun()},
		},
	}
	if _, status := admin.AggregateDelegationForTest(d); status != "failure" {
		t.Fatalf("status = %q, want %q", status, "failure")
	}
}
