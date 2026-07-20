package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeAuditCounter is a hand-rolled stub for the AuditCounter interface
// — keeps the test free of the sqlite driver while still exercising the
// derive-at-read-time wiring end-to-end. result + err are returned by
// every call; lastArgs records the most recent invocation so assertions
// can verify the workspace_id + time window + client_type list reach
// the counter unmodified.
type fakeAuditCounter struct {
	result int
	err    error

	lastArgs struct {
		workspaceID string
		start       time.Time
		end         time.Time
		clientTypes []string
		called      bool
	}
}

func (f *fakeAuditCounter) CountChildCLIToolCalls(
	_ context.Context, workspaceID string, start, end time.Time, clientTypes []string,
) (int, error) {
	f.lastArgs.workspaceID = workspaceID
	f.lastArgs.start = start
	f.lastArgs.end = end
	f.lastArgs.clientTypes = clientTypes
	f.lastArgs.called = true
	return f.result, f.err
}

// CountChildCLIToolCallsBySession reports result as a single, cleanly
// disconnected child session — the unambiguous shape, so these tests stay
// about the derive wiring. Attribution across overlapping sessions is
// covered against the real store in workers/runner.
func (f *fakeAuditCounter) CountChildCLIToolCallsBySession(
	ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string,
) ([]store.ChildCLISessionCount, error) {
	n, err := f.CountChildCLIToolCalls(ctx, workspaceID, start, end, clientTypes)
	if err != nil {
		return nil, err
	}
	disconnected := start.Add(time.Second)
	return []store.ChildCLISessionCount{{
		SessionID:      "child-session",
		ClientType:     clientTypes[0],
		ConnectedAt:    start,
		DisconnectedAt: &disconnected,
		Count:          n,
	}}, nil
}

// TestAnnotateToolCallsSource exercises every branch of the derive
// fallback: native adapter (anthropic), CLI adapter with the counter
// reporting non-zero (the typical case the fix targets), CLI adapter
// with a counter outage (logs + swallows), and a CLI adapter whose
// own ToolCallsCount is already non-zero (a future stream-json patch
// — native count wins).
func TestAnnotateToolCallsSource(t *testing.T) {
	started := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	finishedT := started.Add(45 * time.Second)
	finished := &finishedT

	t.Run("native adapter stamps 'native' and does not query counter", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 999}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-1",
			ModelProvider:  "anthropic",
			ToolCallsCount: 3,
			WorkspaceID:    "ws-1",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceNative {
			t.Fatalf("source = %q, want native", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 3 {
			t.Fatalf("count clobbered: %d, want 3", run.ToolCallsCount)
		}
		if ac.lastArgs.called {
			t.Fatalf("counter unexpectedly called for native adapter")
		}
	})

	t.Run("claude_cli with empty count derives from audit_records", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 7}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-2",
			ModelProvider:  "claude_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 7 {
			t.Fatalf("count = %d, want 7 from counter", run.ToolCallsCount)
		}
		if !ac.lastArgs.called {
			t.Fatalf("counter was not called for CLI adapter")
		}
		if ac.lastArgs.workspaceID != "ws-cli" {
			t.Fatalf("counter workspace_id = %q, want ws-cli", ac.lastArgs.workspaceID)
		}
		if !ac.lastArgs.start.Equal(started) {
			t.Fatalf("counter start = %v, want %v", ac.lastArgs.start, started)
		}
		if !ac.lastArgs.end.Equal(*finished) {
			t.Fatalf("counter end = %v, want %v", ac.lastArgs.end, *finished)
		}
		// Only THIS provider's child client_types reach the counter. The
		// query used to pass the flat union of every CLI family, which is
		// how a concurrent grok_cli or pi_cli run's audit rows ended up in
		// a claude_cli run's total. Order doesn't matter; membership does.
		want := map[string]bool{
			"claude_cli":  true,
			"claude_code": true,
			"claude-code": true,
		}
		for _, ct := range ac.lastArgs.clientTypes {
			if !want[ct] {
				t.Fatalf("counter called with foreign client_type %q — a claude_cli run must not match other families", ct)
			}
			delete(want, ct)
		}
		if len(want) > 0 {
			t.Fatalf("missing child client types from counter call: %v", want)
		}
	})

	t.Run("opencode_cli with empty count derives", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 2}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-3",
			ModelProvider:  "opencode_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 2 {
			t.Fatalf("count = %d, want 2", run.ToolCallsCount)
		}
	})

	t.Run("grok_cli with empty count derives", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 3}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-grok",
			ModelProvider:  "grok_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 3 {
			t.Fatalf("count = %d, want 3", run.ToolCallsCount)
		}
	})

	t.Run("mimo_cli with empty count derives", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 4}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-mimo",
			ModelProvider:  "mimo_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 4 {
			t.Fatalf("count = %d, want 4", run.ToolCallsCount)
		}
	})

	t.Run("counter outage logs + swallows, stays at 0 derived", func(t *testing.T) {
		ac := &fakeAuditCounter{err: errors.New("boom")}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-4",
			ModelProvider:  "claude_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 0 {
			t.Fatalf("count = %d, want 0 on counter error", run.ToolCallsCount)
		}
	})

	t.Run("CLI adapter with non-zero ToolCallsCount keeps native count", func(t *testing.T) {
		// Future-proofs the fix: when the stream-json follow-up lands and
		// the adapter starts reporting its own count, we don't want to
		// override with a (probably noisier) audit-derived number.
		ac := &fakeAuditCounter{result: 999}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-5",
			ModelProvider:  "claude_cli",
			ToolCallsCount: 4,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived (adapter family hint stays)", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 4 {
			t.Fatalf("count = %d, want 4 (native wins when adapter populates it)", run.ToolCallsCount)
		}
		if ac.lastArgs.called {
			t.Fatalf("counter called when adapter already reported a non-zero count")
		}
	})

	t.Run("no counter wired falls back to native semantics for CLI runs", func(t *testing.T) {
		svc := &Service{auditCounter: nil}
		run := &store.WorkerRun{
			ID:             "run-6",
			ModelProvider:  "opencode_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		// Source stays "derived" so the UI still flags unreliability,
		// but ToolCallsCount stays 0 because we never queried.
		if run.ToolCallsCountSource != toolCallsSourceDerived {
			t.Fatalf("source = %q, want derived", run.ToolCallsCountSource)
		}
		if run.ToolCallsCount != 0 {
			t.Fatalf("count = %d, want 0 (no counter wired)", run.ToolCallsCount)
		}
	})

	t.Run("running run (no FinishedAt) uses now() as window end", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 1}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-7",
			ModelProvider:  "claude_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "ws-cli",
			StartedAt:      started,
			FinishedAt:     nil,
		}
		before := time.Now().UTC()
		svc.annotateToolCallsSource(context.Background(), run)
		after := time.Now().UTC()
		if !ac.lastArgs.called {
			t.Fatalf("counter was not called")
		}
		if ac.lastArgs.end.Before(before) || ac.lastArgs.end.After(after) {
			t.Fatalf("end = %v, expected between %v and %v", ac.lastArgs.end, before, after)
		}
	})

	t.Run("empty workspace_id skips counter query", func(t *testing.T) {
		ac := &fakeAuditCounter{result: 99}
		svc := &Service{auditCounter: ac}
		run := &store.WorkerRun{
			ID:             "run-8",
			ModelProvider:  "claude_cli",
			ToolCallsCount: 0,
			WorkspaceID:    "", // pre-denormalisation legacy row
			StartedAt:      started,
			FinishedAt:     finished,
		}
		svc.annotateToolCallsSource(context.Background(), run)
		if run.ToolCallsCount != 0 {
			t.Fatalf("count = %d, want 0 (no workspace)", run.ToolCallsCount)
		}
		if ac.lastArgs.called {
			t.Fatalf("counter unexpectedly called with empty workspace_id")
		}
	})
}

// TestAnnotateRunsToolCallsSource verifies the slice-batch wrapper fans
// the derive across every row.
func TestAnnotateRunsToolCallsSource(t *testing.T) {
	started := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	finishedT := started.Add(time.Minute)
	finished := &finishedT
	ac := &fakeAuditCounter{result: 5}
	svc := &Service{auditCounter: ac}

	runs := []*store.WorkerRun{
		{ID: "r1", ModelProvider: "anthropic", ToolCallsCount: 1, WorkspaceID: "ws", StartedAt: started, FinishedAt: finished},
		{ID: "r2", ModelProvider: "claude_cli", ToolCallsCount: 0, WorkspaceID: "ws", StartedAt: started, FinishedAt: finished},
		{ID: "r3", ModelProvider: "opencode_cli", ToolCallsCount: 0, WorkspaceID: "ws", StartedAt: started, FinishedAt: finished},
		{ID: "r4", ModelProvider: "grok_cli", ToolCallsCount: 0, WorkspaceID: "ws", StartedAt: started, FinishedAt: finished},
		{ID: "r5", ModelProvider: "mimo_cli", ToolCallsCount: 0, WorkspaceID: "ws", StartedAt: started, FinishedAt: finished},
	}
	svc.annotateRunsToolCallsSource(context.Background(), runs)

	if runs[0].ToolCallsCountSource != toolCallsSourceNative {
		t.Errorf("r1 source = %q, want native", runs[0].ToolCallsCountSource)
	}
	if runs[1].ToolCallsCountSource != toolCallsSourceDerived || runs[1].ToolCallsCount != 5 {
		t.Errorf("r2 = %+v, want derived/5", runs[1])
	}
	if runs[2].ToolCallsCountSource != toolCallsSourceDerived || runs[2].ToolCallsCount != 5 {
		t.Errorf("r3 = %+v, want derived/5", runs[2])
	}
	if runs[3].ToolCallsCountSource != toolCallsSourceDerived || runs[3].ToolCallsCount != 5 {
		t.Errorf("r4 = %+v, want derived/5", runs[3])
	}
	if runs[4].ToolCallsCountSource != toolCallsSourceDerived || runs[4].ToolCallsCount != 5 {
		t.Errorf("r5 = %+v, want derived/5", runs[4])
	}
}

// TestIsCLIAdapter is a guard against typo-drift on the adapter family
// set — a misspelled constant would silently disable the derive for
// every CLI run.
func TestIsCLIAdapter(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{"claude_cli", true},
		{"opencode_cli", true},
		{"grok_cli", true},
		{"mimo_cli", true},
		{"gemini_cli", true},
		{"codex_cli", true},
		{"pi_cli", true},
		{"anthropic", false},
		{"openai", false},
		{"openai_compat", false},
		{"", false},
		{"claude", false}, // no underscore — not the CLI provider name
	} {
		if got := isCLIAdapter(tc.provider); got != tc.want {
			t.Errorf("isCLIAdapter(%q) = %v, want %v", tc.provider, got, tc.want)
		}
	}
}
