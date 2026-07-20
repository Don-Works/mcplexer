package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// stubCLICounter reports n tool calls from a single, cleanly-disconnected
// child session — the unambiguous shape, so these tests stay focused on the
// cap/outcome logic rather than on attribution (which
// cli_cap_attribution_test.go covers against the real store).
type stubCLICounter struct {
	n int
}

func (s *stubCLICounter) CountChildCLIToolCalls(
	_ context.Context, _ string, _, _ time.Time, _ []string,
) (int, error) {
	return s.n, nil
}

func (s *stubCLICounter) CountChildCLIToolCallsBySession(
	_ context.Context, _ string, start, _ time.Time, _ []string,
) ([]store.ChildCLISessionCount, error) {
	disconnected := start.Add(2 * time.Second)
	return []store.ChildCLISessionCount{{
		SessionID:      "child-session",
		ClientType:     "grok",
		ConnectedAt:    start.Add(time.Second),
		DisconnectedAt: &disconnected,
		Count:          s.n,
	}}, nil
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
	outcome := loopOutcome{status: StatusSuccess, outputText: "bounded partial report"}
	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)
	if outcome.status != StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", outcome.status)
	}
	if outcome.outputText != "bounded partial report" {
		t.Fatalf("output = %q, want partial evidence preserved", outcome.outputText)
	}
	if outcome.errorText == "" {
		t.Fatal("cap_exceeded outcome must explain the breached cap")
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

func TestApplyCLIToolCallCap_PreservesEarlierRootCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		err    string
	}{
		{name: "adapter failure", status: StatusFailure, err: "adapter send: model unavailable"},
		{name: "deliverability block", status: StatusBlocked, err: "post-execute deliverability gate blocked the run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Deps{CLIToolCounter: &stubCLICounter{n: 95}})
			worker := &store.Worker{ModelProvider: "grok_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
			run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: time.Now().UTC().Add(-time.Minute)}
			state := &loopState{}
			outcome := loopOutcome{status: tc.status, errorText: tc.err, outputText: "partial evidence"}

			r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)

			if outcome.status != tc.status {
				t.Fatalf("status = %q, want original %q", outcome.status, tc.status)
			}
			if !strings.Contains(outcome.errorText, tc.err) || !strings.Contains(outcome.errorText, "max tool calls") {
				t.Fatalf("error = %q, want original cause plus cap annotation", outcome.errorText)
			}
			if outcome.outputText != "partial evidence" {
				t.Fatalf("output = %q, want evidence preserved", outcome.outputText)
			}
		})
	}
}

func TestApplyCLIToolCallCap_BoundsPreservedRootCause(t *testing.T) {
	r := New(Deps{CLIToolCounter: &stubCLICounter{n: 95}})
	worker := &store.Worker{ModelProvider: "grok_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: time.Now().UTC().Add(-time.Minute)}
	state := &loopState{}
	root := "adapter send: " + strings.Repeat("provider detail ", 2000)
	outcome := loopOutcome{status: StatusFailure, errorText: root}

	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)
	// Re-applying the post-run check must remain bounded and must retain one
	// canonical cap annotation even when the original cause was oversized.
	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)

	if outcome.status != StatusFailure {
		t.Fatalf("status = %q, want original failure", outcome.status)
	}
	if !strings.HasPrefix(outcome.errorText, "adapter send:") || !strings.Contains(outcome.errorText, "max tool calls") {
		t.Fatalf("bounded error lost root cause or cap annotation: %q", outcome.errorText)
	}
	if len(outcome.errorText) > maxCLICapOutcomeErrorBytes {
		t.Fatalf("bounded error length = %d, want <= %d", len(outcome.errorText), maxCLICapOutcomeErrorBytes)
	}
	if strings.Count(outcome.errorText, "max tool calls") != 1 {
		t.Fatalf("cap annotation count = %d, want 1: %q", strings.Count(outcome.errorText, "max tool calls"), outcome.errorText)
	}
}
