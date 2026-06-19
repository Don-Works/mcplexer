package runner

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type stubCLICounter struct {
	n int
}

func (s *stubCLICounter) CountChildCLIToolCalls(
	_ context.Context, _ string, _, _ time.Time, _ []string,
) (int, error) {
	return s.n, nil
}

func TestApplyCLIToolCallCap_Exceeds(t *testing.T) {
	r := New(Deps{CLIToolCounter: &stubCLICounter{n: 95}})
	worker := &store.Worker{
		ModelProvider: "grok_cli",
		MaxToolCalls:  80,
		WorkspaceID:   "ws-1",
	}
	run := &store.WorkerRun{
		WorkspaceID: "ws-1",
		StartedAt:   time.Now().UTC().Add(-time.Minute),
	}
	state := &loopState{}
	outcome := loopOutcome{status: StatusSuccess}
	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)
	if outcome.status != StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", outcome.status)
	}
	if state.toolCallCount != 95 {
		t.Fatalf("toolCallCount = %d, want 95", state.toolCallCount)
	}
}

func TestApplyCLIToolCallCap_SkipsAPIProvider(t *testing.T) {
	r := New(Deps{CLIToolCounter: &stubCLICounter{n: 999}})
	worker := &store.Worker{ModelProvider: "anthropic", MaxToolCalls: 10}
	run := &store.WorkerRun{StartedAt: time.Now().UTC()}
	state := &loopState{toolCallCount: 2}
	outcome := loopOutcome{status: StatusSuccess}
	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)
	if outcome.status != StatusSuccess {
		t.Fatalf("status changed to %q, want success", outcome.status)
	}
	if state.toolCallCount != 2 {
		t.Fatalf("toolCallCount clobbered to %d", state.toolCallCount)
	}
}
