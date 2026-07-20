package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

const maxCLICapOutcomeErrorBytes = 4096

// cliChildClientTypesByProvider maps a CLI model_provider to the MCP
// client_type values ITS child announces when opening the
// MCP-back-to-mcplexer connection. Partitioning by provider (rather than
// matching one flat union) is half the attribution story: two concurrent
// runs on different CLI families no longer see each other's audit rows at
// all, because a grok_cli run never considers a session that announced
// "pi".
//
// The strings are exactly the union that used to be matched flat, so no
// row that counted before stops matching now — only which run it is
// attributed to changes. A provider absent from this map (or one whose
// child announces a clientInfo.name not listed here) yields no candidate
// sessions, which the policy below treats as "cannot attribute" — the
// fail-open direction.
var cliChildClientTypesByProvider = map[string][]string{
	models.ProviderClaudeCLI:   {"claude_cli", "claude_code", "claude-code"},
	models.ProviderOpenCodeCLI: {"opencode", "opencode_cli"},
	models.ProviderGrokCLI:     {"grok", "grok_cli", "xai", "xai_cli"},
	models.ProviderMiMoCLI:     {"mimo", "mimo_cli", "mimocode"},
	models.ProviderGeminiCLI:   {"gemini", "gemini_cli"},
	models.ProviderCodexCLI:    {"codex_cli"},
	models.ProviderPiCLI:       {"pi", "pi_cli"},
}

// CLIChildClientTypes returns the client_type values to match for a CLI
// model_provider, or nil when the provider has no known child harness.
func CLIChildClientTypes(modelProvider string) []string {
	return cliChildClientTypesByProvider[modelProvider]
}

// CLIToolCallCounter counts audit_records produced by CLI child MCP
// sessions during a worker run window. Implemented by store.AuditStore.
//
// The flat count this declares is no longer what the cap runs on — see
// CLISessionToolCallCounter — but it stays the wiring contract so a store
// that has not grown session attribution yet still satisfies Deps.
type CLIToolCallCounter interface {
	CountChildCLIToolCalls(ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string) (int, error)
}

// CLISessionToolCallCounter returns the per-session breakdown of
// audit_records produced by CLI child MCP sessions during a worker run
// window. The sqlite store implements it; the configured CLIToolCallCounter
// is asserted to it at use time. A counter that does not implement it
// yields no attribution, so the cap simply does not fire — the safe
// direction, and the reason this is not folded into CLIToolCallCounter.
type CLISessionToolCallCounter interface {
	CountChildCLIToolCallsBySession(
		ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string,
	) ([]store.ChildCLISessionCount, error)
}

// AttributeCLIToolCalls decides whether a set of CLI-child sessions active
// in a run's window can be attributed to that ONE run, and returns the
// tool-call total if so.
//
// The store has already excluded sessions that predate the window, so the
// operator's own orchestrator session is gone by the time we get here. What
// remains is the concurrency question: one CLI adapter may spawn several
// child processes over a run (claude_cli starts a fresh `claude` per Send),
// and those sessions are strictly SEQUENTIAL. Two different runs executing
// at the same time produce sessions that OVERLAP. So:
//
//   - no sessions            → (0, false)  nothing to attribute
//   - sessions never overlap → (sum, true) one run's sequence of children
//   - any two overlap        → (0, false)  ambiguous; could be another run
//
// A session still open (disconnected_at NULL — the child that just exited,
// or one whose disconnect never landed) is treated as running to the end of
// the window, so it overlaps everything after it. That biases an uncertain
// case towards "cannot attribute", which is the direction that matters: an
// unenforced cap costs a few extra tool calls, a misattributed one
// manufactures a cap_exceeded failure on a worker that did nothing wrong.
//
// sessions must be ordered by ConnectedAt ascending (the store guarantees
// this).
func AttributeCLIToolCalls(sessions []store.ChildCLISessionCount, windowEnd time.Time) (int, bool) {
	if len(sessions) == 0 {
		return 0, false
	}
	total := 0
	var maxEnd time.Time
	for i, s := range sessions {
		end := windowEnd
		if s.DisconnectedAt != nil {
			end = *s.DisconnectedAt
		}
		// !After == "starts at or before the previous session ended", i.e.
		// the two sessions were alive at the same instant.
		if i > 0 && !s.ConnectedAt.After(maxEnd) {
			return 0, false
		}
		if end.After(maxEnd) {
			maxEnd = end
		}
		total += s.Count
	}
	return total, true
}

// attributedCLIToolCallCount reads this run's share of the CLI-child audit
// rows in its window. The second return is false whenever the rows cannot
// be pinned to this run — no session-attributed counter wired, an unknown
// provider family, a query failure, or an ambiguous set of candidate
// sessions. Callers must enforce nothing in that case; see
// AttributeCLIToolCalls.
func (r *Runner) attributedCLIToolCallCount(
	ctx context.Context, worker *store.Worker, run *store.WorkerRun, wsID string,
) (int, bool) {
	clientTypes := CLIChildClientTypes(worker.ModelProvider)
	if r.cliToolCounter == nil || wsID == "" || run.StartedAt.IsZero() || len(clientTypes) == 0 {
		return 0, false
	}
	counter, ok := r.cliToolCounter.(CLISessionToolCallCounter)
	if !ok {
		return 0, false
	}
	end := time.Now().UTC()
	sessions, err := counter.CountChildCLIToolCallsBySession(
		ctx, wsID, run.StartedAt, end, clientTypes,
	)
	if err != nil {
		slog.Warn("cli tool call count query failed",
			"workspace_id", wsID, "run_id", run.ID, "error", err)
		return 0, false
	}
	n, attributed := AttributeCLIToolCalls(sessions, end)
	if !attributed {
		slog.Info("cli tool call cap not enforced: run not attributable",
			"workspace_id", wsID, "run_id", run.ID,
			"model_provider", worker.ModelProvider,
			"candidate_sessions", len(sessions))
	}
	return n, attributed
}

// applyCLIToolCallCap checks audit-derived tool-call totals for CLI
// providers and upgrades the terminal outcome to cap_exceeded when the
// worker's max_tool_calls is breached. No-op for API adapters (their cap
// is enforced inside the gateway dispatch loop).
func (r *Runner) applyCLIToolCallCap(
	ctx context.Context,
	worker *store.Worker,
	run *store.WorkerRun,
	state *loopState,
	outcome *loopOutcome,
) {
	if r == nil || worker == nil || run == nil || state == nil || outcome == nil {
		return
	}
	if worker.MaxToolCalls <= 0 || !models.IsCLIProvider(worker.ModelProvider) {
		return
	}
	maxToolCalls := state.capsSnapshot().MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = worker.MaxToolCalls
	}
	wsID := run.WorkspaceID
	if wsID == "" {
		wsID = worker.WorkspaceID
	}
	n, attributed := r.attributedCLIToolCallCount(ctx, worker, run, wsID)
	if !attributed {
		return
	}
	state.toolCallCount = n
	if n > maxToolCalls {
		capError := fmt.Sprintf("max tool calls (%d) exceeded (cli audit count %d)", maxToolCalls, n)
		// Preserve any report/evidence the CLI produced. The cap is checked
		// from the audit ledger after the external CLI exits, so replacing the
		// whole outcome here used to erase output that could still help the
		// parent review or a retry. Only a successful outcome is promoted to
		// cap_exceeded; a pre-existing adapter failure or post-output block is
		// the more useful root cause and must keep its taxonomy.
		if outcome.status == StatusSuccess {
			outcome.status = StatusCapExceeded
			outcome.errorText = capError
			return
		}
		outcome.errorText = appendBoundedCLICapError(outcome.errorText, capError)
	}
}

func appendBoundedCLICapError(existing, capError string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return capError
	}
	// Rebuild an existing annotation instead of merely truncating the combined
	// text: a very long root cause could otherwise push the only cap evidence
	// beyond the retained prefix on a repeated call.
	if idx := strings.Index(existing, capError); idx >= 0 {
		existing = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(existing[:idx]), ";"))
		if existing == "" {
			return capError
		}
	}
	suffix := "; " + capError
	if len(existing)+len(suffix) <= maxCLICapOutcomeErrorBytes {
		return existing + suffix
	}
	headLimit := maxCLICapOutcomeErrorBytes - len(suffix) - len("…")
	if headLimit <= 0 {
		return truncate(capError, maxCLICapOutcomeErrorBytes-len("…"))
	}
	return truncate(existing, headLimit) + suffix
}
