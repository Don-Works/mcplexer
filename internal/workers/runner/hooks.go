// Package runner — hooks.go implements optional user-authored pre/post
// execute JS hooks for a worker.
//
// A worker may carry two scripts (store.Worker.PreExecuteScript /
// PostExecuteScript). They run in the SAME code-mode sandbox the worker's
// own mcpx__execute_code calls use, dispatched through the worker tool
// dispatcher, so they inherit the worker's tool allowlist, capability
// profile, workspace access and audit trail — no new sandbox or privilege
// surface. (There is no JS fetch; a script reaches an HTTP endpoint via a
// downstream tool namespace such as fetch.* / ip.* that the worker's
// allowlist permits.)
//
//   - pre-execute runs BEFORE prepareRun's adapter is ever called, so a
//     block costs zero model/CLI spend. It can gate the run: throw, or
//     call abort(reason), to BLOCK; return cleanly to PROCEED.
//   - post-execute runs at the top of finalize, after output is produced.
//     On an otherwise-successful run a block REJECTS the output (status
//     flips to "blocked", which suppresses channel emission downstream).
//
// Fail-closed: a script that throws, calls abort(), times out, or fails to
// dispatch BLOCKS — a broken gate stops the run rather than waving it
// through. The verdict is recovered two ways: a structured sentinel that
// the injected abort() helper prints (clean reason), or the envelope's
// isError flag (any uncaught throw / infra error).
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	hookPhasePre  = "pre"
	hookPhasePost = "post"

	// hookVerdictSentinel prefixes the structured verdict line that the
	// injected abort() helper prints, so the runner can recover a clean
	// reason from the sandbox's captured output even when the throw's
	// error text is noisy (stack/location annotations).
	hookVerdictSentinel = "@@MCPLEXER_HOOK_VERDICT@@"

	// maxHookReasonLen bounds the block reason copied onto the run's Error.
	maxHookReasonLen = 2000
	// maxHookOutputPreview bounds how much of the produced output text is
	// handed to a post-execute script as hook.run.output.
	maxHookOutputPreview = 4000
)

// hookContext is serialized to JSON and bound as the global `hook` object
// in the script's scope.
type hookContext struct {
	Phase  string          `json:"phase"`
	Worker hookWorkerCtx   `json:"worker"`
	Run    hookRunCtx      `json:"run"`
	Params json.RawMessage `json:"params,omitempty"`
}

type hookWorkerCtx struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WorkspaceID string `json:"workspace_id"`
	ExecMode    string `json:"exec_mode"`
}

// hookRunCtx is the run-shaped slice of context a hook can read. The
// post-only fields (Status..ToolCalls) are zero/empty for a pre hook.
type hookRunCtx struct {
	ID               string  `json:"id"`
	TriggerKind      string  `json:"trigger_kind,omitempty"`
	TriggerMessageID string  `json:"trigger_message_id,omitempty"`
	Status           string  `json:"status,omitempty"`
	Output           string  `json:"output,omitempty"`
	Error            string  `json:"error,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	ToolCalls        int     `json:"tool_calls,omitempty"`
}

// hookResult is the parsed verdict of one hook execution.
type hookResult struct {
	blocked bool
	reason  string
}

// runPreExecuteHook runs the worker's pre-execute script (if any) before the
// model loop. Returns a terminal *loopOutcome to skip the loop and finalize
// the run as "blocked", or nil to proceed.
func (r *Runner) runPreExecuteHook(
	ctx context.Context, worker *store.Worker, run *store.WorkerRun, state *loopState,
) *loopOutcome {
	if strings.TrimSpace(worker.PreExecuteScript) == "" {
		return nil
	}
	res := r.runHook(ctx, worker, run.ID, hookPhasePre, worker.PreExecuteScript, hookRunCtx{
		ID:               run.ID,
		TriggerKind:      state.triggerKind,
		TriggerMessageID: state.triggerMessageID,
	})
	if !res.blocked {
		return nil
	}
	slog.Info("worker pre-execute hook blocked run",
		"worker_id", worker.ID, "run_id", run.ID, "reason", res.reason)
	return &loopOutcome{status: StatusBlocked, errorText: res.reason}
}

// runPostExecuteHook runs the worker's post-execute script (if any) at the top
// of finalize and returns the (possibly mutated) outcome. A block on an
// otherwise-successful run flips status to "blocked", which causes
// emitTerminalOutputs to suppress the draft from every channel sink. A run
// that already reached a non-success terminal status keeps it (the post hook
// only annotates the error) — we never upgrade a failure into a success.
func (r *Runner) runPostExecuteHook(
	ctx context.Context, worker *store.Worker, run *store.WorkerRun, state *loopState, outcome loopOutcome,
) loopOutcome {
	if strings.TrimSpace(worker.PostExecuteScript) == "" {
		return outcome
	}
	// Skip a paused run (it resumes and finalizes again later) and a run the
	// pre-gate already blocked (the worker never executed).
	if outcome.status == StatusAwaitingApproval || outcome.status == StatusBlocked {
		return outcome
	}
	res := r.runHook(ctx, worker, run.ID, hookPhasePost, worker.PostExecuteScript, hookRunCtx{
		ID:               run.ID,
		TriggerKind:      state.triggerKind,
		TriggerMessageID: state.triggerMessageID,
		Status:           outcome.status,
		Output:           truncate(outcome.outputText, maxHookOutputPreview),
		Error:            outcome.errorText,
		InputTokens:      state.inputTokens,
		OutputTokens:     state.outputTokens,
		CostUSD:          state.costUSD,
		ToolCalls:        state.toolCallCount,
	})
	if !res.blocked {
		return outcome
	}
	slog.Info("worker post-execute hook rejected run",
		"worker_id", worker.ID, "run_id", run.ID,
		"prior_status", outcome.status, "reason", res.reason)
	if outcome.status == StatusSuccess {
		outcome.status = StatusBlocked
		outcome.errorText = res.reason
		return outcome
	}
	if outcome.errorText == "" {
		outcome.errorText = res.reason
	}
	return outcome
}

// runHook composes the verdict preamble + the user script, runs it through
// the worker's mcpx__execute_code dispatch path, and parses the verdict.
func (r *Runner) runHook(
	ctx context.Context, worker *store.Worker, runID, phase, script string, runCtx hookRunCtx,
) hookResult {
	if r.dispatcher == nil {
		// No sandbox available (degraded wiring). Don't block on a missing
		// runtime — proceed and let the absence be visible in logs.
		slog.Warn("worker hook skipped: no dispatcher wired",
			"worker_id", worker.ID, "run_id", runID, "phase", phase)
		return hookResult{}
	}
	inputJSON, err := json.Marshal(map[string]string{
		"code": composeHookCode(phase, worker, runCtx, script),
	})
	if err != nil {
		return hookResult{blocked: true, reason: hookReason(phase, "input marshal failed: "+err.Error())}
	}
	res, err := r.dispatcher.DispatchTool(ctx, ToolCallRequest{
		Name:      "mcpx__execute_code",
		InputJSON: string(inputJSON),
		WorkerID:  worker.ID,
		RunID:     runID,
	})
	if err != nil {
		return hookResult{blocked: true, reason: hookReason(phase, "dispatch failed: "+truncate(err.Error(), maxHookReasonLen))}
	}
	return parseHookVerdict(phase, res)
}

// composeHookCode wraps the user script with a preamble that binds the `hook`
// context object and provides abort()/proceed() helpers.
func composeHookCode(phase string, worker *store.Worker, runCtx hookRunCtx, script string) string {
	hc := hookContext{
		Phase: phase,
		Worker: hookWorkerCtx{
			ID:          worker.ID,
			Name:        worker.Name,
			WorkspaceID: worker.WorkspaceID,
			ExecMode:    worker.ExecMode,
		},
		Run: runCtx,
	}
	if pj := strings.TrimSpace(worker.ParametersJSON); pj != "" && pj != "null" && json.Valid([]byte(pj)) {
		hc.Params = json.RawMessage(pj)
	}
	ctxJSON, err := json.Marshal(hc)
	if err != nil {
		ctxJSON = []byte("{}")
	}
	jsLiteral := jsSafeJSON(string(ctxJSON))

	var b strings.Builder
	b.WriteString("var hook = ")
	b.WriteString(jsLiteral)
	b.WriteString(";\n")
	b.WriteString("function abort(reason){var r=(reason===undefined||reason===null)?\"\":String(reason);")
	b.WriteString("print(" + strconv.Quote(hookVerdictSentinel) + "+JSON.stringify({action:\"abort\",reason:r}));")
	b.WriteString("throw new Error(" + strconv.Quote("mcplexer "+phase+"-execute hook aborted: ") + "+r);}\n")
	b.WriteString("function proceed(){}\n")
	b.WriteString("// ---- worker " + phase + "_execute_script ----\n")
	b.WriteString(script)
	b.WriteString("\n")
	return b.String()
}

// jsSafeJSON escapes U+2028 / U+2029 — valid in JSON strings but (pre-ES2019)
// illegal as raw characters inside a JS string literal — so the embedded hook
// context object literal parses under goja regardless of what landed in a
// worker name or parameters blob.
func jsSafeJSON(s string) string {
	lineSep := string(rune(0x2028))
	paraSep := string(rune(0x2029))
	if !strings.Contains(s, lineSep) && !strings.Contains(s, paraSep) {
		return s
	}
	s = strings.ReplaceAll(s, lineSep, "\\u2028")
	s = strings.ReplaceAll(s, paraSep, "\\u2029")
	return s
}

// parseHookVerdict turns the execute_code result envelope into a verdict.
// An explicit abort() sentinel always wins (clean reason); otherwise any
// isError (uncaught throw / infra failure) is a fail-closed block.
func parseHookVerdict(phase string, res ToolCallResult) hookResult {
	text := collectEnvelopeText(res.OutputJSON)
	if reason, ok := extractHookSentinel(text); ok {
		return hookResult{blocked: true, reason: hookReason(phase, reason)}
	}
	if res.IsError {
		return hookResult{blocked: true, reason: hookReason(phase, firstErrorLine(text))}
	}
	return hookResult{}
}

// collectEnvelopeText flattens an MCP tools/call result envelope's text
// content. Handles both the codemode envelope ({"content":[{"text":...}]})
// and the dispatcher's transport-error shape ({"error":...}); falls back to
// the raw JSON when neither parses.
func collectEnvelopeText(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return raw
	}
	var parts []string
	for _, c := range env.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 && env.Error != "" {
		return env.Error
	}
	return strings.Join(parts, "\n")
}

// extractHookSentinel scans for the verdict line the abort() helper prints
// and returns the reason when action == "abort".
func extractHookSentinel(text string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		_, payload, found := strings.Cut(line, hookVerdictSentinel)
		if !found {
			continue
		}
		var v struct {
			Action string `json:"action"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &v); err != nil {
			continue
		}
		if v.Action == "abort" {
			return v.Reason, true
		}
	}
	return "", false
}

// firstErrorLine pulls the most informative line out of an errored
// envelope's text — the sandbox's "Error: ..." line when present, else the
// whole (trimmed) text.
func firstErrorLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Error:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return strings.TrimSpace(text)
}

// hookReason formats the final block reason stamped on the run's Error.
func hookReason(phase, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "no reason given"
	}
	return truncate(fmt.Sprintf("%s-execute hook blocked the run: %s", phase, detail), maxHookReasonLen)
}
