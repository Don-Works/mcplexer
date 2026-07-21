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

// cliChildClientTypes mirrors admin.childCLIClientTypes — the MCP
// client_type values CLI subprocesses announce when opening their
// MCP-back-to-mcplexer connection.
var cliChildClientTypes = []string{
	"claude_cli",
	"claude_code",
	"claude-code",
	"opencode",
	"opencode_cli",
	"grok",
	"grok_cli",
	"xai",
	"xai_cli",
	"mimo",
	"mimo_cli",
	"mimocode",
	"gemini",
	"gemini_cli",
	"codex_cli",
	"pi",
	"pi_cli",
}

// CLIToolCallCounter counts audit_records produced by CLI child MCP
// sessions during a worker run window. Implemented by store.AuditStore.
type CLIToolCallCounter interface {
	CountChildCLIToolCalls(ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string) (int, error)
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
	if r.cliToolCounter == nil || wsID == "" || run.StartedAt.IsZero() {
		return
	}
	end := time.Now().UTC()
	n, err := r.cliToolCounter.CountChildCLIToolCalls(
		ctx, wsID, run.StartedAt, end, cliChildClientTypes,
	)
	if err != nil {
		slog.Warn("cli tool call count query failed",
			"workspace_id", wsID, "run_id", run.ID, "error", err)
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
