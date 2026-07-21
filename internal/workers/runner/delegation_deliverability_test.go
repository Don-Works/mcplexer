package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestDelegationDeliverabilityGateIsNative(t *testing.T) {
	for _, provider := range []string{"anthropic", "grok_cli", "openai_compat"} {
		t.Run(provider, func(t *testing.T) {
			delegationWorker := &store.Worker{
				ModelProvider:  provider,
				ParametersJSON: `{"_mcplexer_delegation":{"id":"del-test","kind":"token_preserving_delegation"}}`,
			}
			r := &Runner{} // no dispatcher: native validation must not depend on code-mode health.
			outcome := r.runPostExecuteHook(
				context.Background(),
				delegationWorker,
				&store.WorkerRun{ID: "run-1"},
				&loopState{},
				loopOutcome{status: StatusSuccess, outputText: " \n\t "},
			)
			if outcome.status != StatusBlocked {
				t.Fatalf("status = %q, want blocked for empty delegation report", outcome.status)
			}
			if !strings.Contains(outcome.errorText, "empty final report") {
				t.Fatalf("error = %q, want deliverability explanation", outcome.errorText)
			}
		})
	}
}

func TestDelegationDeliverabilityGatePreservesValidAndUnrelatedOutcomes(t *testing.T) {
	delegationWorker := &store.Worker{
		ParametersJSON: `{"_mcplexer_delegation":{"id":"del-test","kind":"token_preserving_delegation"}}`,
	}
	for _, tc := range []struct {
		name    string
		worker  *store.Worker
		outcome loopOutcome
		want    string
	}{
		{
			name:    "non-empty delegation report",
			worker:  delegationWorker,
			outcome: loopOutcome{status: StatusSuccess, outputText: "STATUS: success"},
			want:    StatusSuccess,
		},
		{
			name:    "ordinary worker may use another delivery mechanism",
			worker:  &store.Worker{ParametersJSON: `{}`},
			outcome: loopOutcome{status: StatusSuccess},
			want:    StatusSuccess,
		},
		{
			name:    "adapter failure remains root cause",
			worker:  delegationWorker,
			outcome: loopOutcome{status: StatusFailure, errorText: "adapter send: unavailable"},
			want:    StatusFailure,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := applyDelegationDeliverabilityGate(tc.worker, tc.outcome)
			if got.status != tc.want {
				t.Fatalf("status = %q, want %q", got.status, tc.want)
			}
		})
	}
}
