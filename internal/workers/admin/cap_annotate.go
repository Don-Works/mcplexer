package admin

import (
	"context"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	toolCallsCapScopeGateway  = "gateway_loop"
	toolCallsCapScopeCLIAudit = "cli_audit"
)

// annotateToolCallsCap stamps how max_tool_calls applies to this run and
// whether the configured cap was exceeded. Call after tool_calls_count is
// final (native or derived).
func annotateToolCallsCap(run *store.WorkerRun, worker *store.Worker) {
	if run == nil {
		return
	}
	maxCalls := 0
	if worker != nil {
		maxCalls = worker.MaxToolCalls
	}
	if maxCalls <= 0 {
		run.ToolCallsCapScope = ""
		run.ToolCallsCapExceeded = false
		return
	}
	if models.IsCLIProvider(run.ModelProvider) {
		run.ToolCallsCapScope = toolCallsCapScopeCLIAudit
		run.ToolCallsCapExceeded = run.ToolCallsCount > maxCalls
		return
	}
	run.ToolCallsCapScope = toolCallsCapScopeGateway
	run.ToolCallsCapExceeded = run.Status == "cap_exceeded" &&
		strings.Contains(strings.ToLower(run.Error), "tool calls")
	if !run.ToolCallsCapExceeded && run.ToolCallsCount > maxCalls {
		run.ToolCallsCapExceeded = true
	}
}

func (s *Service) annotateRunAnnotations(ctx context.Context, run *store.WorkerRun, worker *store.Worker) {
	if run == nil {
		return
	}
	s.annotateToolCallsSource(ctx, run)
	run.StampAccountingMissing()
	annotateToolCallsCap(run, worker)
	annotateDeliverable(run)
}

func (s *Service) annotateRunsAnnotations(ctx context.Context, runs []*store.WorkerRun, worker *store.Worker) {
	s.annotateRunsToolCallsSource(ctx, runs)
	for _, r := range runs {
		if r == nil {
			continue
		}
		r.StampAccountingMissing()
		annotateToolCallsCap(r, worker)
		annotateDeliverable(r)
	}
}
