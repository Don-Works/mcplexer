// Package admin — tool_calls_derive.go owns the derive-at-read-time
// fallback for WorkerRun.tool_calls_count, used by the CLI adapter
// families.
//
// Background. Both subprocess adapters return Text + token counts but
// never populate ToolCalls — the comment in internal/models/claude_cli.go
// flags this as "follow-up via stream-json parsing". In practice the
// CLI child spawned by these adapters opens its OWN stdio MCP connection
// back to the gateway and dispatches its tools from there. The dispatch
// rows land in audit_records with client_type identifying the child
// harness — they just aren't joined back to the WorkerRun row.
//
// Until the stream-json parsing fix lands, GetRun/ListRuns derive a
// reasonable tool_calls_count by counting matching audit_records inside
// the run's window. The annotated source field ("derived" vs "native")
// is surfaced to the UI so operators don't see a misleading 0 and
// conclude their healthy CLI worker is hallucinating tool calls.
package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

const (
	toolCallsSourceNative  = "native"
	toolCallsSourceDerived = "derived"
)

// childCLISessionCounter is the session-attributed audit counter. The
// Service's configured AuditCounter is asserted to it at use time rather
// than being required by the AuditCounter interface itself: a counter that
// predates session attribution simply yields no attribution, which lands on
// the safe side (count stays 0, still annotated "derived") instead of
// forcing every implementation to be updated in lockstep.
type childCLISessionCounter interface {
	CountChildCLIToolCallsBySession(
		ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string,
	) ([]store.ChildCLISessionCount, error)
}

// isCLIAdapter reports whether the WorkerRun's model_provider is one
// whose ToolCalls slice is structurally empty (so the derive fallback
// is justified).
func isCLIAdapter(modelProvider string) bool {
	return models.IsCLIProvider(modelProvider)
}

// annotateToolCallsSource sets run.ToolCallsCountSource to "native" or
// "derived" based on the adapter family. For derive-eligible runs whose
// own ToolCallsCount is still 0 AND whose auditCounter is wired, it
// overrides ToolCallsCount with the count of matching audit_records in
// the run's window. A non-derive-eligible run always stamps "native"
// and leaves ToolCallsCount alone.
//
// The override is intentionally one-way: we never lower a non-zero
// adapter-reported ToolCallsCount, even on a CLI adapter — if the
// stream-json follow-up lands and starts populating ToolCalls natively,
// the native count wins automatically without code changes here.
//
// Errors from the audit query are logged and swallowed. The run row
// stays unchanged (still 0, stamped "derived" so the UI still hints at
// the unreliability). Better to surface a misleading 0 than to fail the
// whole runs API on a counter outage.
func (s *Service) annotateToolCallsSource(ctx context.Context, run *store.WorkerRun) {
	if run == nil {
		return
	}
	if !isCLIAdapter(run.ModelProvider) {
		run.ToolCallsCountSource = toolCallsSourceNative
		return
	}
	// CLI-family run. Stamp derived regardless of whether we end up
	// overriding the count — the field documents that the count for
	// this adapter family is NOT authoritative.
	run.ToolCallsCountSource = toolCallsSourceDerived
	if run.ToolCallsCount > 0 {
		// Adapter already reported a non-zero count (e.g. a future
		// stream-json patch landed for opencode_cli). Leave it.
		return
	}
	if s.auditCounter == nil {
		return
	}
	if run.WorkspaceID == "" {
		// Pre-denormalisation rows (very early M0 dev installs) have an
		// empty WorkspaceID. The audit query refuses to run without
		// one — leave the count at 0 and let the UI hint explain.
		return
	}
	start := run.StartedAt
	end := time.Now().UTC()
	if run.FinishedAt != nil {
		end = *run.FinishedAt
	}
	// Defensive: zero/empty time bound. Audit timestamps are written
	// in UTC RFC3339-equivalent strings; an empty start would scan
	// every row. Skip the query in that case.
	if start.IsZero() {
		return
	}
	counter, ok := s.auditCounter.(childCLISessionCounter)
	if !ok {
		return
	}
	clientTypes := runner.CLIChildClientTypes(run.ModelProvider)
	if len(clientTypes) == 0 {
		return
	}
	sessions, err := counter.CountChildCLIToolCallsBySession(
		ctx, run.WorkspaceID, start, end, clientTypes)
	if err != nil {
		slog.Warn("worker run tool_calls_count derive failed",
			"run_id", run.ID, "worker_id", run.WorkerID,
			"workspace_id", run.WorkspaceID, "error", err)
		return
	}
	// Same attribution policy the runner's cap uses, deliberately shared:
	// the number the UI shows and the number the cap fires on must never
	// disagree. Unattributable leaves the count at 0 — a run whose audit
	// rows cannot be told apart from a concurrent run's has no honest
	// count to display.
	n, attributed := runner.AttributeCLIToolCalls(sessions, end)
	if !attributed {
		return
	}
	run.ToolCallsCount = n
}

// annotateRunsToolCallsSource fans annotateToolCallsSource across a
// slice. Used by ListRuns so the runs-list page gets the same hint as
// the single-run detail page.
func (s *Service) annotateRunsToolCallsSource(ctx context.Context, runs []*store.WorkerRun) {
	for _, r := range runs {
		s.annotateToolCallsSource(ctx, r)
	}
}
