package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// opencodeCLIAdapter shells out to the locally-installed `opencode` CLI
// in non-interactive mode (`opencode run --model X --format json`). It
// bills against the user's stored opencode credentials — opencode itself
// owns provider routing (Anthropic, OpenRouter, Minimax, LM Studio, MLX
// and ~30 other backends) and returns NDJSON events on stdout.
//
// The adapter is deliberately read-only: opencode's agent loop has its
// own tool surface, but we want mcplexer's dispatcher to remain the only
// path through which a worker can call tools. So this adapter only
// returns the assistant's TEXT output and never surfaces the tool-call
// events opencode emits. Workers that need tools wire them via the
// existing mcplexer tool dispatcher.
type opencodeCLIAdapter struct {
	binaryPath string
	attachURL  string
	modelID    string
	runner     opencodeCLIRunner
}

// opencodeCLIRunner is the test seam. workspacePath, when non-empty,
// becomes the subprocess CWD so opencode's own MCP-back-to-mcplexer
// connection lands in the worker's bound workspace (stdio CWD inference).
type opencodeCLIRunner func(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error)

func newOpenCodeCLIAdapter(endpointURL, modelID string) *opencodeCLIAdapter {
	var attachURL string
	binaryPath := endpointURL
	if isHTTPURL(endpointURL) {
		attachURL = strings.TrimSpace(endpointURL)
		binaryPath = ""
	}
	if binaryPath == "" {
		binaryPath = "opencode"
	}
	// sandbox-exec ignores the parent's PATH, so a bare binary name
	// like "opencode" produces ENOENT inside the sandbox. Resolve here
	// so the wrapped invocation always succeeds. We try (1) the
	// MCPLEXER_TEST_OPENCODE_CLI_BIN env override (test rigs only),
	// (2) opencode's standard install locations (~/.opencode/bin,
	// ~/.local/bin, Homebrew on both archs), then (3) LookPath against
	// the daemon's own PATH; give up after — the bare name stays and
	// the eventual error is informative.
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, OpenCodeCLIBinaryEnvVar, opencodeStandardPaths)
	}
	return &opencodeCLIAdapter{
		binaryPath: binaryPath,
		attachURL:  attachURL,
		modelID:    modelID,
		runner:     opencodeExecRunner,
	}
}

func isHTTPURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// opencodeStandardPaths is the fallback search list for the opencode
// binary when the daemon's PATH doesn't include it. Ordered most-
// likely first (user install, then Homebrew on both archs).
func opencodeStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".opencode", "bin", "opencode"))
		out = append(out, filepath.Join(home, ".local", "bin", "opencode"))
	}
	return append(out,
		"/opt/homebrew/bin/opencode",
		"/usr/local/bin/opencode",
	)
}

// opencodeCLIEvent is one NDJSON record from `opencode run --format json`.
// Only fields we consume are declared; opencode may add more without
// breaking decode.
type opencodeCLIEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Reason string `json:"reason"`
		Tokens struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cache  struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Cost float64 `json:"cost"`
	} `json:"part"`
}

// maxOpenCodeAttempts caps how many times Send invokes opencode for a
// single logical turn. opencode (observed on 1.15.12) intermittently tears
// down the run subprocess after a tool call BEFORE flushing the model's
// final assistant `text` part — the NDJSON stream ends on a reasoning /
// tool-calls step with no terminal "stop", so we parse a perfectly
// successful run that carries EMPTY text. Downstream that becomes a worker
// reply with no body (a Telegram "thinking" bubble that never resolves).
// The truncation is a non-deterministic teardown race, so one clean retry
// recovers the reply in the overwhelming majority of cases. We retry ONLY
// when the parse succeeded with no text — never on a real error, and never
// when the model genuinely produced output — so a model that legitimately
// says nothing can't spin here.
const maxOpenCodeAttempts = 2

func (a *opencodeCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := []string{
		"run", "--format", "json",
	}
	if a.attachURL != "" {
		args = append(args, "--attach", a.attachURL)
		if req.WorkspacePath != "" {
			args = append(args, "--dir", req.WorkspacePath)
		}
	}
	if a.modelID != "" {
		args = append(args, "--model", a.modelID)
	}
	stdin := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "opencode_cli: dispatch",
		slog.String("model_id", a.modelID),
		slog.String("attach_url", a.attachURL),
		slog.Int("prompt_len", len(stdin)),
	)

	var resp *SendResponse
	var err error
	for attempt := 1; attempt <= maxOpenCodeAttempts; attempt++ {
		resp, err = a.runAndParseRetrying(ctx, args, stdin, req.WorkspacePath, start)
		if err != nil {
			return nil, err
		}
		if resp.Text != "" || attempt == maxOpenCodeAttempts {
			break
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "opencode_cli: empty assistant text — retrying once",
			slog.String("model_id", a.modelID),
			slog.Int("attempt", attempt),
			slog.Int("output_tokens", resp.OutputTokens),
			slog.String("stop_reason", resp.StopReason),
		)
	}
	if resp.Text == "" {
		// Both attempts truncated. Don't fail the run (the model DID work,
		// tokens were spent) — return the empty response and let the output
		// layer surface a visible "couldn't generate a reply" fallback so a
		// pending placeholder still resolves. See emitOutputs.
		slog.LogAttrs(ctx, slog.LevelWarn, "opencode_cli: no final assistant text after retries",
			slog.String("model_id", a.modelID),
			slog.Int("attempts", maxOpenCodeAttempts),
			slog.Int("output_tokens", resp.OutputTokens),
		)
	}
	return resp, nil
}

// maxOpenCodeTransientRetries caps how many times runAndParseRetrying
// re-invokes opencode after a transient SERVER error — distinct from the
// empty-text retry in Send. The managed `opencode serve` is crash-prone;
// when it dies mid-run its supervisor restarts it within ~1s (capped
// backoff starts at 1s), so a single re-attach after a short pause
// often recovers the turn instead of killing the whole worker run with
// "Error: Session not found". Cold async startup can also take several
// seconds, so keep this window long enough to bridge readiness without
// blocking delegation creation.
const maxOpenCodeTransientRetries = 6

// isTransientOpenCodeError reports whether err looks like a recoverable
// server-side hiccup (the managed server crashed / is mid-restart, or the
// session it held vanished) rather than a real model or input error.
func isTransientOpenCodeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"session not found",
		"connection refused",
		"econnrefused",
		"dial tcp",
		"server closed",
		"unexpected eof",
		"connection reset",
		"database is locked",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// runAndParseRetrying wraps runAndParse with a bounded retry on transient
// server errors. A failed attempt produced no model output (opencode
// exited non-zero before streaming a reply), so re-running is safe — no
// partial side effects to double-apply.
func (a *opencodeCLIAdapter) runAndParseRetrying(
	ctx context.Context, args []string, stdin, workspacePath string, start time.Time,
) (*SendResponse, error) {
	var resp *SendResponse
	var err error
	for attempt := 1; attempt <= maxOpenCodeTransientRetries; attempt++ {
		resp, err = a.runAndParse(ctx, args, stdin, workspacePath, start)
		if err == nil || !isTransientOpenCodeError(err) || attempt == maxOpenCodeTransientRetries {
			return resp, err
		}
		backoff := time.Duration(attempt) * 1500 * time.Millisecond
		slog.LogAttrs(ctx, slog.LevelWarn, "opencode_cli: transient server error — retrying after backoff",
			slog.String("model_id", a.modelID),
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()),
			slog.Duration("backoff", backoff),
		)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return resp, err
}

// runAndParse performs ONE opencode invocation + NDJSON parse. Factored out
// of Send so the empty-text retry loop can re-invoke without duplicating the
// kill-source disambiguation + logging.
func (a *opencodeCLIAdapter) runAndParse(
	ctx context.Context, args []string, stdin, workspacePath string, start time.Time,
) (*SendResponse, error) {
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, stdin, workspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), 256)
		// Disambiguate the kill source so future failures aren't opaque.
		// Three common cases land here as `signal: killed`:
		//   (1) parent ctx cancelled / deadline exceeded — we kill the
		//       subprocess via exec.CommandContext;
		//   (2) wall-clock budget cap fired upstream in the runner;
		//   (3) external SIGKILL (OOM, sandbox-exec watchdog, user kill).
		// Inspect ctx.Err() to separate (1)+(2) from (3) before logging.
		ctxReason := ""
		if cerr := ctx.Err(); cerr != nil {
			ctxReason = cerr.Error()
			if cause := context.Cause(ctx); cause != nil && cause.Error() != cerr.Error() {
				ctxReason = cerr.Error() + " (cause: " + cause.Error() + ")"
			}
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "opencode_cli: non-zero exit",
			slog.String("binary", a.binaryPath),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
			slog.String("ctx_error", ctxReason),
		)
		if ctxReason != "" {
			return nil, fmt.Errorf("opencode_cli: run: %w (ctx: %s, stderr: %s)", err, ctxReason, snip)
		}
		return nil, fmt.Errorf("opencode_cli: run: %w (stderr: %s)", err, snip)
	}

	resp, parseErr := parseOpenCodeNDJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), 256)
		slog.LogAttrs(ctx, slog.LevelError, "opencode_cli: parse failed",
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("opencode_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "opencode_cli: success",
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

// parseOpenCodeNDJSON walks the NDJSON stream emitted by `opencode run
// --format json` and collapses it into a SendResponse:
//   - text events are buffered per step; on `step_finish` we KEEP them
//     only when the step terminated for a non-tool-use reason. Steps
//     that ended in `tool_use` are the model "thinking aloud" between
//     tool batches ("Now I have all the data, let me write the report.")
//     — useful for live observability, useless in the final answer.
//     Dropping them avoids the prelude noise that previously bloated
//     output_text + leaked into the mesh summary.
//   - token counts + cost accumulate across every step_finish so
//     multi-step runs report the true totals. (Earlier behaviour
//     overwrote on each step, so multi-step token counts were the
//     last step only.)
//
// Any individual line that doesn't parse as JSON is skipped — opencode
// occasionally emits non-JSON banner lines on stderr but rarely on
// stdout; defensive parsing avoids whole-stream failure on those.
func parseOpenCodeNDJSON(raw []byte) (*SendResponse, error) {
	var stepText []string  // text accumulated for the current in-flight step
	var finalText []string // text retained from terminal (non-tool_use) steps
	// lastToolStepText holds the text from the most recent tool-use step we
	// dropped from finalText. It's a fallback for the case the dedupe logic
	// alone got wrong: a run that TERMINATES on a tool-call step never emits
	// a closing "stop" step, so finalText stays empty and the consumer (e.g.
	// the Telegram concierge's output_text -> mesh reply) gets a blank
	// message. When the model puts its actual answer in the same turn as its
	// last tool call ("Done — added the lead.", + a verify call), that answer
	// is the closest thing to a final reply we have. Surfacing it beats
	// delivering nothing; we only reach for it when finalText is empty, so
	// the normal stop-terminated path keeps excluding prelude narration.
	var lastToolStepText []string
	resp := &SendResponse{StopReason: StopEndTurn}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev opencodeCLIEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "text":
			if ev.Part.Text != "" {
				stepText = append(stepText, ev.Part.Text)
			}
		case "step_finish":
			resp.InputTokens += ev.Part.Tokens.Input + ev.Part.Tokens.Cache.Read + ev.Part.Tokens.Cache.Write
			resp.OutputTokens += ev.Part.Tokens.Output
			resp.CostUSD += ev.Part.Cost
			resp.StopReason = normalizeOpenCodeStop(ev.Part.Reason)
			// Drop text from steps whose only purpose was to call tools.
			// opencode normalises stop reasons OpenAI-style ("tool-calls",
			// with a hyphen) but upstream Anthropic providers might pass
			// "tool_use" through — accept both spellings so we don't
			// re-introduce the prelude-noise bug if opencode switches its
			// internal representation. Anything else (stop, length,
			// max_tokens, content_filter, etc.) keeps the step's text.
			if !isOpencodeToolUseReason(ev.Part.Reason) {
				finalText = append(finalText, stepText...)
			} else if len(stepText) > 0 {
				// Remember the dropped narration in case this turns out to
				// be the terminal step (run ended on a tool call) and we'd
				// otherwise return empty.
				lastToolStepText = stepText
			}
			stepText = nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Any trailing text never closed by a step_finish (truncated stream)
	// still counts — better to surface partial output than swallow it.
	finalText = append(finalText, stepText...)
	resp.Text = strings.TrimSpace(strings.Join(finalText, ""))
	// Run terminated on a tool-call step with no closing "stop" turn: the
	// only text the model produced lives in lastToolStepText. Fall back to
	// it so downstream consumers (Telegram reply, mesh summary) never get a
	// blank message when the model actually said something.
	if resp.Text == "" {
		resp.Text = strings.TrimSpace(strings.Join(lastToolStepText, ""))
	}
	// An empty result — no final text, and possibly no token data either —
	// means opencode produced nothing usable (a truncated/aborted run, seen
	// intermittently with local reasoning models). We deliberately do NOT
	// error here: an empty result is a delivery problem, not a parse
	// problem. Returning the empty response lets Send retry once (the
	// truncation is a non-deterministic race) and, if that still comes back
	// empty, lets the output layer surface a visible "couldn't reply"
	// fallback so a pending placeholder always resolves instead of hanging.
	return resp, nil
}

func normalizeOpenCodeStop(s string) string {
	switch s {
	case "stop":
		return StopEndTurn
	case "tool-calls", "tool_calls", "tool_use":
		return StopToolUse
	case "max_tokens", "length":
		return StopMaxTokens
	default:
		return StopOther
	}
}

// isOpencodeToolUseReason reports whether a step_finish.reason string
// means "this step ended because the model called tools" — i.e. its
// text content is mid-run narration, not the final answer. opencode
// emits the OpenAI-style "tool-calls" (hyphen) in practice; we accept
// the underscore variants too so future opencode changes don't
// silently re-bloat output_text.
func isOpencodeToolUseReason(s string) bool {
	switch s {
	case "tool-calls", "tool_calls", "tool_use":
		return true
	}
	return false
}

// opencodeExecRunner wraps the opencode subprocess in mcplexer's
// sandbox (sandbox-exec on macOS; identity transform elsewhere) and
// runs it. OpenCode receives its own read-only config/auth plus writable
// provider state/cache, while gateway and unrelated host credentials remain
// invisible at the kernel layer.
func opencodeExecRunner(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
	return runSandboxedModelCLI(
		ctx, binary, args, stdin, workspacePath,
		opencodeCLISandboxConfig, opencodeCLIEnvironmentPolicy(),
	)
}
