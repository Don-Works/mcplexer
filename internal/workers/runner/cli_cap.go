package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

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
		*outcome = loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("max tool calls (%d) exceeded (cli audit count %d)", maxToolCalls, n),
		}
	}
}
