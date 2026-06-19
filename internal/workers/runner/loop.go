package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	estimatedCharsPerToken  = 4
	maxModelToolResultBytes = 32 * 1024
)

// loopState carries the mutable bookkeeping for one run through the
// model ↔ tool loop. Separated from Runner so a single Runner can serve
// concurrent runs without sharing per-run mutation.
type loopState struct {
	worker       *store.Worker
	systemPrompt string
	tools        []models.ToolSchema
	adapter      models.ModelAdapter
	caps         Caps
	startedAt    time.Time
	runID        string

	// workspacePath is the filesystem root of worker.WorkspaceID,
	// resolved at prepareRun time and forwarded into every
	// SendRequest. Subprocess adapters (claude_cli, opencode_cli) use
	// it as cmd.Dir so the model's own MCP-back-to-mcplexer connection
	// binds to the right workspace via stdio CWD inference. Empty when
	// the worker has no workspace or the workspace row was missing.
	workspacePath string

	messages      []models.Message
	iteration     int
	toolCallCount int
	inputTokens   int
	outputTokens  int
	costUSD       float64
	meshMsgIDs    []string

	// consolidationsPerformed counts memory__save dispatches inside this
	// run. Populated unconditionally (every worker tallies its own
	// consolidations) but only consumed by the memory-consolidator
	// finalize path. The definition tracks the consolidator template's
	// canonical action: one memory__save call per consolidated cluster.
	// memory__invalidate dispatches are intentionally NOT counted —
	// invalidations are derivatives of a save (sources superseded_by the
	// new note), so counting both would double-report.
	consolidationsPerformed int

	// preApprovedTools are tool names that bypass propose-mode gating
	// for this single run. Populated from RunOpts.PreApprovedTools by
	// prepareRun; checked inside dispatchToolCalls before the
	// awaiting_approval short-circuit fires.
	preApprovedTools []string

	// pendingApproval captures the tool dispatch that caused a
	// propose-mode worker to stop. The runner uses this in finalize to
	// persist a WorkerApproval row (M1). Nil when the run terminated
	// for any other reason.
	pendingApproval *pendingApprovalInfo

	// maxLifetimeOutputTokens is the loop-aggregate output-token cap.
	// 0 means "no aggregate cap" (M0 behaviour). Distinct from
	// s.caps.MaxOutputTokens which is the per-turn ceiling passed into
	// adapter.Send.
	maxLifetimeOutputTokens int

	// triggerKind, triggerMessageID, triggerSourcePeer, triggerChainDepth
	// carry M4 mesh-trigger provenance. Populated from RunOpts in
	// prepareRun; surfaced into the started signal so observers see
	// "started — mesh-triggered by …" and into mesh-output tags so
	// downstream loop guards see the chain depth.
	triggerKind       string
	triggerMessageID  string
	triggerSourcePeer string
	triggerChainDepth int

	// accountingMissing is true when every adapter send so far reported
	// zero input tokens, zero output tokens AND zero cost. Maintained by
	// accountUsage; consumed at finalize to stamp the run snapshot so
	// the UI can show "accounting missing" instead of a misleading
	// $0.00 (grok_cli headless JSON, for example, may omit usage/cost
	// fields entirely on successful runs).
	accountingMissing bool

	// billingModel, subscriptionBucket, realCostUSD are the billing
	// classification accumulators for this run. Set by accountUsage on
	// every adapter send from ClassifyBilling + RealCostUSD; consumed
	// by persistRunFinalize to stamp the worker_runs row.
	billingModel       string
	subscriptionBucket string
	realCostUSD        float64
}

// pendingApprovalInfo holds the tool call that needs operator approval.
// Lives on loopState because dispatchToolCalls returns a loopOutcome
// (status), not the full call payload — and we need the payload to
// persist the WorkerApproval row.
type pendingApprovalInfo struct {
	toolName  string
	toolInput string // JSON-encoded
}

// newLoopState builds the starting state. The first user message is the
// rendered prompt; the system slot carries the optional skill body.
func newLoopState(
	worker *store.Worker,
	systemPrompt, userPrompt string,
	tools []models.ToolSchema,
	adapter models.ModelAdapter,
	caps Caps,
	startedAt time.Time,
) *loopState {
	messages := []models.Message{}
	if userPrompt != "" {
		messages = append(messages, models.Message{Role: models.RoleUser, Content: userPrompt})
	}
	return &loopState{
		worker:       worker,
		systemPrompt: systemPrompt,
		tools:        tools,
		adapter:      adapter,
		caps:         caps,
		startedAt:    startedAt,
		messages:     messages,
	}
}

func (s *loopState) appendMeshID(id string) {
	if id == "" {
		return
	}
	s.meshMsgIDs = append(s.meshMsgIDs, id)
}

// isPreApproved reports whether toolName is in this run's pre-approved
// list (populated by RunOpts.PreApprovedTools). When true, propose-
// mode gating is skipped for that single tool.
func (s *loopState) isPreApproved(toolName string) bool {
	for _, n := range s.preApprovedTools {
		if n == toolName {
			return true
		}
	}
	return false
}

// loopOutcome captures the terminal state of one run.
type loopOutcome struct {
	status     string
	outputText string
	errorText  string
}

// summary returns a short, human-readable one-liner for the
// worker.finished signal content.
func (o loopOutcome) summary() string {
	if o.errorText != "" {
		return o.errorText
	}
	if o.outputText != "" {
		return truncate(o.outputText, 200)
	}
	return ""
}

// runLoop drives the model ↔ tool loop until a terminal state is
// reached. Caps are checked at the top of each iteration. Returns the
// outcome; never panics. ctx cancellation is treated like a failure.
func (r *Runner) runLoop(ctx context.Context, s *loopState) loopOutcome {
	for {
		if ctx.Err() != nil {
			return r.ctxCancelOutcome(ctx, s)
		}
		if o, exceeded := r.checkCaps(s); exceeded {
			return o
		}
		if o, exceeded := r.checkNextInputBudget(s); exceeded {
			return o
		}
		s.iteration++

		// Bound each adapter call to the remaining wall-clock budget.
		// Without this, a subprocess-backed adapter can block inside one
		// Send call forever and never return to the loop-level cap check.
		remaining := s.caps.MaxWallClock - r.clock.Now().Sub(s.startedAt)
		perCall := remaining + 5*time.Second
		if perCall < 30*time.Second {
			perCall = 30 * time.Second
		}
		sendCtx, cancelSend := context.WithTimeout(ctx, perCall)
		resp, err := s.adapter.Send(sendCtx, models.SendRequest{
			System:        s.systemPrompt,
			Messages:      s.messages,
			Tools:         s.tools,
			MaxTokens:     s.caps.MaxOutputTokens,
			WorkspacePath: s.workspacePath,
		})
		cancelSend()
		if err != nil {
			// Even on adapter error, emit a worker_model.send audit row
			// so token-burn before the crash is visible. Most adapters
			// return nil resp on error (resp will be nil), but some may
			// return a partially-populated response with cached input
			// tokens charged — fold those into the run accounting too.
			// See audit gap #3 (2026-05-21 audit pass).
			r.emitAuditModelSendError(ctx, s, resp, err.Error())
			if resp != nil {
				s.inputTokens += resp.InputTokens
				s.outputTokens += resp.OutputTokens
				s.costUSD += resp.CostUSD
			}
			// If the adapter's subprocess was killed because the run
			// context was cancelled, classify by cause: an operator
			// hard-stop becomes StatusCancelled, our wall-clock deadline
			// becomes cap_exceeded, and anything else stays a generic
			// failure. context.Cause distinguishes operator cancel
			// (errOperatorCancel) and our cap (errWallClockExceeded) from
			// a parent ctx deadline.
			if ctx.Err() != nil {
				if oc := r.ctxCancelOutcome(ctx, s); oc.status != StatusFailure {
					return oc
				}
			}
			return loopOutcome{status: StatusFailure, errorText: "adapter send: " + err.Error()}
		}
		r.accountUsage(ctx, s, resp)

		if len(resp.ToolCalls) == 0 {
			// TrimSpace at the source so whitespace-only model output is
			// indistinguishable from empty output everywhere downstream
			// (mesh findings, file sinks, the run row's output_text).
			return loopOutcome{status: StatusSuccess, outputText: strings.TrimSpace(resp.Text)}
		}
		s.messages = append(s.messages, models.Message{
			Role:      models.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		out, terminal := r.dispatchToolCalls(ctx, s, resp.ToolCalls)
		if terminal != nil {
			return *terminal
		}
		s.messages = append(s.messages, out...)
	}
}

// ctxCancelOutcome maps a cancelled run context to the right terminal
// outcome by inspecting context.Cause:
//
//   - errOperatorCancel → StatusCancelled with the operator's reason.
//     A human hard-stop is NOT a worker failure, so it gets its own
//     status (excluded from the auto-pause failure streak and from
//     delegation / model-rank failure counts).
//   - errWallClockExceeded → StatusCapExceeded (our wall-clock budget).
//   - anything else (parent deadline, parent cancel) → StatusFailure.
//
// Callers must only invoke this when ctx.Err() != nil.
func (r *Runner) ctxCancelOutcome(ctx context.Context, s *loopState) loopOutcome {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errOperatorCancel):
		return loopOutcome{
			status:    StatusCancelled,
			errorText: r.operatorCancelReason(s.runID),
		}
	case errors.Is(cause, errWallClockExceeded):
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("wall-clock (%s) exceeded", s.caps.MaxWallClock),
		}
	default:
		msg := "context cancelled"
		if err := ctx.Err(); err != nil {
			msg = "context cancelled: " + err.Error()
		}
		return loopOutcome{status: StatusFailure, errorText: msg}
	}
}

// checkCaps returns (outcome, true) when a cap is exceeded; otherwise
// (zero, false). All caps return status=cap_exceeded — the operator
// distinguishes via the error message.
func (r *Runner) checkCaps(s *loopState) (loopOutcome, bool) {
	if s.iteration >= s.caps.MaxIterations {
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("max iterations (%d) exceeded", s.caps.MaxIterations),
		}, true
	}
	if s.toolCallCount >= s.caps.MaxToolCalls {
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("max tool calls (%d) exceeded", s.caps.MaxToolCalls),
		}, true
	}
	elapsed := r.clock.Now().Sub(s.startedAt)
	if elapsed >= s.caps.MaxWallClock {
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("wall-clock (%s) exceeded", s.caps.MaxWallClock),
		}, true
	}
	// Positive cap -> abort once cumulative input tokens hit it. applyDefaults
	// ensures the runner always has a positive aggregate input ceiling unless
	// a test constructs loopState manually.
	if s.caps.MaxInputTokens > 0 && s.inputTokens >= s.caps.MaxInputTokens {
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("input tokens (%d) exceeded cap (%d)", s.inputTokens, s.caps.MaxInputTokens),
		}, true
	}
	// maxLifetimeOutputTokens is the loop-aggregate cap separate from
	// s.caps.MaxOutputTokens (which is the per-turn ceiling passed to
	// the adapter). The lifetime cap is populated only when the Worker
	// explicitly sets MaxOutputTokens — runner defaults leave it at 0
	// (no aggregate cap), so existing M0 behaviour is preserved.
	if s.maxLifetimeOutputTokens > 0 && s.outputTokens >= s.maxLifetimeOutputTokens {
		return loopOutcome{
			status:    StatusCapExceeded,
			errorText: fmt.Sprintf("output tokens (%d) exceeded cap (%d)", s.outputTokens, s.maxLifetimeOutputTokens),
		}, true
	}
	return loopOutcome{}, false
}

func (r *Runner) checkNextInputBudget(s *loopState) (loopOutcome, bool) {
	if s.caps.MaxInputTokens <= 0 {
		return loopOutcome{}, false
	}
	nextInput := estimateNextInputTokens(s)
	if s.inputTokens+nextInput < s.caps.MaxInputTokens {
		return loopOutcome{}, false
	}
	return loopOutcome{
		status: StatusCapExceeded,
		errorText: fmt.Sprintf(
			"estimated input tokens (%d current + %d next) would reach cap (%d)",
			s.inputTokens, nextInput, s.caps.MaxInputTokens,
		),
	}, true
}

func estimateNextInputTokens(s *loopState) int {
	chars := len(s.systemPrompt)
	for _, msg := range s.messages {
		chars += len(msg.Content) + len(msg.ToolUseID) + len(msg.ToolResult)
		for _, call := range msg.ToolCalls {
			chars += len(call.ID) + len(call.Name)
			if len(call.Input) > 0 {
				if raw, err := json.Marshal(call.Input); err == nil {
					chars += len(raw)
				}
			}
		}
	}
	for _, tool := range s.tools {
		chars += len(tool.Name) + len(tool.Description)
		if len(tool.InputSchema) > 0 {
			if raw, err := json.Marshal(tool.InputSchema); err == nil {
				chars += len(raw)
			}
		}
	}
	if chars <= 0 {
		return 0
	}
	return (chars + estimatedCharsPerToken - 1) / estimatedCharsPerToken
}

func trimToolResultForModel(raw string) string {
	if len(raw) <= maxModelToolResultBytes {
		return raw
	}
	marker := fmt.Sprintf(
		"\n\n[mcplexer: tool result truncated from %d to %d bytes for worker model context. Use a narrower query or write large output to a file.]",
		len(raw), maxModelToolResultBytes,
	)
	keep := maxModelToolResultBytes - len(marker)
	if keep <= 0 {
		return marker
	}
	return raw[:keep] + marker
}

// dispatchToolCalls runs each tool call in order. Returns the
// RoleTool messages to append to the conversation. The terminal
// return is non-nil when the loop must short-circuit (propose-mode
// hit a WriteClass tool).
//
// Propose-mode gating runs in TWO layers:
//
//  1. PRE-DISPATCH: dispatcher.Classify(call.Name) is consulted before
//     any side-effect-producing call. If propose + write-class + not
//     pre-approved, the loop short-circuits to awaiting_approval and
//     DispatchTool is never invoked. This is the SECURITY contract —
//     the prior behaviour invoked DispatchTool and only checked the
//     result.WriteClass flag, which meant the write had already happened.
//
//  2. POST-DISPATCH: result.WriteClass is still checked as a defensive
//     layer in case Classify mis-reports (e.g. a future tool taxonomy
//     decides at runtime). Identical short-circuit path.
func (r *Runner) dispatchToolCalls(
	ctx context.Context,
	s *loopState,
	calls []models.ToolCall,
) ([]models.Message, *loopOutcome) {
	out := make([]models.Message, 0, len(calls))
	for _, call := range calls {
		// Re-check the per-run tool-call cap BEFORE dispatching the next
		// call. checkCaps only fires at the top of runLoop (once per model
		// turn); a single turn can return N > MaxToolCalls tool calls and,
		// without this guard, every one of them would dispatch (with side
		// effects) before the outer cap check fires again. In autonomous +
		// write-class mode that is an unbounded-side-effects bypass, so the
		// cap MUST be enforced inside the inner loop too.
		if s.toolCallCount >= s.caps.MaxToolCalls {
			return nil, &loopOutcome{
				status:    StatusCapExceeded,
				errorText: fmt.Sprintf("max tool calls (%d) exceeded", s.caps.MaxToolCalls),
			}
		}
		s.appendMeshID(r.emitToolCall(ctx, s.worker.ID, s.runID, call.Name))
		inputJSON, _ := json.Marshal(call.Input)
		r.publishToolCall(s, call.Name, inputJSON, true)
		if outcome := r.preDispatchGate(ctx, s, call, inputJSON); outcome != nil {
			// preDispatchGate already emits the audit denial row. Mirror
			// it on the run bus so live UI sees the gate fire before the
			// awaiting_approval status frame lands.
			r.publishToolCall(s, call.Name, inputJSON, false)
			return nil, outcome
		}
		writeClass := r.dispatcher.Classify(call.Name)
		r.emitAuditToolDispatch(ctx, s, call.Name, writeClass, true)
		result, err := r.dispatcher.DispatchTool(ctx, ToolCallRequest{
			Name:      call.Name,
			InputJSON: string(inputJSON),
			WorkerID:  s.worker.ID,
			RunID:     s.runID,
		})
		if err != nil {
			out = append(out, models.Message{
				Role:       models.RoleTool,
				ToolUseID:  call.ID,
				ToolResult: "error: " + err.Error(),
			})
			s.toolCallCount++
			continue
		}
		if s.worker.ExecMode == ExecModePropose && result.WriteClass && !s.isPreApproved(call.Name) {
			// Post-dispatch gate caught a write-class tool the pre-dispatch
			// Classify missed. Forensics audit: even though the dispatch
			// already executed, mark this tool as DENIED in the ledger so
			// incident reconstruction can see the propose-mode gate fired
			// after the fact (the prior allowed=true row above shows the
			// optimistic pre-classification; this row shows the corrected
			// verdict). See audit gap #1 (2026-05-21 audit pass).
			r.emitAuditToolDispatch(ctx, s, call.Name, true, false)
			r.publishToolCall(s, call.Name, inputJSON, false)
			s.pendingApproval = &pendingApprovalInfo{
				toolName:  call.Name,
				toolInput: string(inputJSON),
			}
			s.appendMeshID(r.emitAwaitingApproval(ctx, s.worker.ID, s.runID, s.worker.Name, call.Name))
			return nil, &loopOutcome{
				status:     StatusAwaitingApproval,
				outputText: fmt.Sprintf("propose-mode worker stopped before write tool %q", call.Name),
			}
		}
		out = append(out, models.Message{
			Role:       models.RoleTool,
			ToolUseID:  call.ID,
			ToolResult: trimToolResultForModel(result.OutputJSON),
		})
		s.toolCallCount++
		// Tally consolidator-domain actions. The memory-consolidator
		// template's canonical action is one memory__save per cluster;
		// finalize reads s.consolidationsPerformed to stamp the
		// memory__consolidator_run audit row + the Tier-1 mesh
		// broadcast. We tally on EVERY worker so the counter is
		// available regardless of worker name — the consolidator
		// finalize path is the only consumer today, but the counter
		// stays generic for future consumers (e.g. a per-worker
		// "rows changed" dashboard tile).
		if !result.IsError && call.Name == "memory__save" {
			s.consolidationsPerformed++
		}
	}
	return out, nil
}

// preDispatchGate enforces propose-mode + write-class gating BEFORE the
// dispatcher executes the call. Returns a non-nil loopOutcome when the
// loop must short-circuit; nil means "proceed to DispatchTool". This is
// the SECURITY-critical guard — a missing or no-op Classify must NOT
// silently fall through to DispatchTool with side effects.
func (r *Runner) preDispatchGate(
	ctx context.Context, s *loopState, call models.ToolCall, inputJSON []byte,
) *loopOutcome {
	if s.worker.ExecMode != ExecModePropose {
		return nil
	}
	if s.isPreApproved(call.Name) {
		return nil
	}
	if !r.dispatcher.Classify(call.Name) {
		return nil
	}
	// Audit forensics: the gate is about to short-circuit because this
	// is a write-class tool in propose mode and not pre-approved. Emit a
	// worker_tool.dispatch{allowed:false} row BEFORE returning so
	// incident reconstruction can see the denial in the ledger. Without
	// this, the awaiting_approval fast path was forensics-blind. See
	// audit gap #1 (2026-05-21 audit pass).
	r.emitAuditToolDispatch(ctx, s, call.Name, true, false)
	s.pendingApproval = &pendingApprovalInfo{
		toolName:  call.Name,
		toolInput: string(inputJSON),
	}
	s.appendMeshID(r.emitAwaitingApproval(ctx, s.worker.ID, s.runID, s.worker.Name, call.Name))
	return &loopOutcome{
		status:     StatusAwaitingApproval,
		outputText: fmt.Sprintf("propose-mode worker stopped before write tool %q", call.Name),
	}
}

// accountUsage folds adapter token + cost telemetry into the run state
// AND emits one worker_model.send audit record per adapter call so the
// dashboard's audit feed shows every model invocation (not just the
// summary at run end).
//
// Cost preference: if the adapter reported a non-zero CostUSD
// (claude_cli, which emits total_cost_usd in its result envelope), we
// trust that authoritative number. Otherwise we fall back to the
// pricing-table-derived EstimateCostUSD. Workers using claude_cli
// therefore see the exact $ that Claude reported, which matters because
// the pricing model behind OAuth-authed claude calls (subscription
// quota / Agent SDK pool) is not derivable from per-token rates.
func (r *Runner) accountUsage(ctx context.Context, s *loopState, resp *models.SendResponse) {
	costDelta := resp.CostUSD
	if costDelta == 0 {
		costDelta = models.EstimateCostUSD(
			s.worker.ModelProvider, s.worker.ModelID, resp.InputTokens, resp.OutputTokens,
		)
	}
	s.inputTokens += resp.InputTokens
	s.outputTokens += resp.OutputTokens
	s.costUSD += costDelta
	cl := models.ClassifyBilling(s.worker.ModelProvider, s.worker.ModelID)
	s.billingModel = string(cl.Model)
	s.subscriptionBucket = string(cl.Bucket)
	s.realCostUSD += models.RealCostUSD(
		s.worker.ModelProvider, s.worker.ModelID,
		resp.InputTokens, resp.OutputTokens, resp.CostUSD,
	)
	// 0 in + 0 out + $0 on a send that nominally succeeded means the
	// adapter's accounting is missing (e.g. grok_cli omitting usage
	// fields), not that the call was free. The flag is recomputed from
	// the running totals so one later send WITH telemetry clears it.
	s.accountingMissing = s.inputTokens == 0 && s.outputTokens == 0 && s.costUSD == 0
	r.emitAuditModelSend(ctx, s, resp.InputTokens, resp.OutputTokens, costDelta)

	// Publish the assistant prose for this turn so live UI tabs can
	// stream the transcript in real time. Empty text (tool-only turns)
	// is dropped to keep the event volume tight.
	if resp.Text != "" {
		r.runBus.Publish(&RunEvent{
			Kind:      RunEventKindTextDelta,
			WorkerID:  s.worker.ID,
			RunID:     s.runID,
			Iteration: s.iteration,
			Text:      resp.Text,
		})
	}
	// And the cumulative usage tick — counters here are the running
	// totals after this turn, matching what the persisted row will show
	// at finalize.
	r.runBus.Publish(&RunEvent{
		Kind:         RunEventKindUsage,
		WorkerID:     s.worker.ID,
		RunID:        s.runID,
		Iteration:    s.iteration,
		InputTokens:  s.inputTokens,
		OutputTokens: s.outputTokens,
		CostUSD:      s.costUSD,
		ToolCalls:    s.toolCallCount,
	})
}

// publishToolCall emits a tool_call event on the run bus. Input JSON is
// truncated to keep SSE frames small; the full payload lives in the
// audit ledger if anyone needs it. allowed=false means propose-mode
// gating denied this call (mirror of the audit denial row).
func (r *Runner) publishToolCall(s *loopState, name string, inputJSON []byte, allowed bool) {
	const maxInputBytes = 512
	in := string(inputJSON)
	if len(in) > maxInputBytes {
		in = in[:maxInputBytes] + "…"
	}
	r.runBus.Publish(&RunEvent{
		Kind:          RunEventKindToolCall,
		WorkerID:      s.worker.ID,
		RunID:         s.runID,
		Iteration:     s.iteration,
		ToolName:      name,
		ToolInputJSON: in,
		ToolAllowed:   allowed,
	})
}

// truncate caps a long string for display in the worker.finished
// signal so the mesh row stays readable; full output lives on the
// WorkerRun row.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:max]) + "…"
}
