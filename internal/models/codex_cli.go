package models

import (
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

const (
	codexCLIDefaultBinary = "codex"
	codexCLIErrOutputLen  = 256
	codexCLIProvider      = "codex_cli"
)

// codexCLIAdapter shells out to OpenAI's `codex` CLI in non-interactive
// JSON mode. Credentials stay with the host install (OPENAI_API_KEY or
// codex login); mcplexer never reads or stores the OpenAI API key.
//
// Codex CLI emits a JSON envelope on stdout with the assistant's reply,
// stop reason, and usage data. When the CLI omits usage/cost fields
// (headless mode or older versions), mcplexer records zeros rather than
// inventing values.
type codexCLIAdapter struct {
	binaryPath string
	modelID    string
	runner     codexCLIRunner
}

// codexCLIRunner is the test seam. prompt is written to stdin by the
// production runner.
type codexCLIRunner func(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error)

func newCodexCLIAdapter(binaryPath, modelID string) *codexCLIAdapter {
	if binaryPath == "" {
		binaryPath = codexCLIDefaultBinary
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, CodexCLIBinaryEnvVar, codexStandardPaths)
	}
	return &codexCLIAdapter{
		binaryPath: binaryPath,
		modelID:    modelID,
		runner:     codexExecRunner,
	}
}

func codexStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".codex", "bin", "codex"),
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, "bin", "codex"),
		)
	}
	return append(out,
		"/opt/homebrew/bin/codex",
		"/usr/local/bin/codex",
	)
}

// codexCLIEvent is the JSON envelope emitted by `codex --format json`.
// Only fields we consume are declared; codex may add more without
// breaking decode.
type codexCLIEvent struct {
	Type string `json:"type"`
	// Flat envelope fields (codex >= 0.2 style)
	Text         string  `json:"text"`
	Result       string  `json:"result"`
	StopReason   string  `json:"stop_reason"`
	IsError      bool    `json:"is_error"`
	Error        string  `json:"error"`
	CostUSD      float64 `json:"cost_usd"`
	OutputTokens int     `json:"output_tokens"`
	// Nested usage object (codex >= 0.3 / structured output)
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		TotalTokens              int `json:"total_tokens"`
	} `json:"usage"`
	// Nested message object (alternative envelope shape)
	Message struct {
		Content    string `json:"content"`
		StopReason string `json:"stop_reason"`
	} `json:"message"`
	// Nested tokens object (another common shape)
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
}

func (a *codexCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildCodexCLIArgs(a.modelID, req.WorkspacePath)
	prompt := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "codex_cli: dispatch",
		slog.String("provider", codexCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(prompt)),
	)
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, prompt, req.WorkspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), codexCLIErrOutputLen)
		ctxReason := ""
		if cerr := ctx.Err(); cerr != nil {
			ctxReason = cerr.Error()
			if cause := context.Cause(ctx); cause != nil && cause.Error() != cerr.Error() {
				ctxReason = cerr.Error() + " (cause: " + cause.Error() + ")"
			}
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "codex_cli: non-zero exit",
			slog.String("provider", codexCLIProvider),
			slog.String("model_id", a.modelID),
			slog.String("binary", a.binaryPath),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
			slog.String("ctx_error", ctxReason),
		)
		if ctxReason != "" {
			return nil, fmt.Errorf("codex_cli: run: %w (ctx: %s, stderr: %s)", err, ctxReason, snip)
		}
		return nil, fmt.Errorf("codex_cli: run: %w (stderr: %s)", err, snip)
	}
	resp, parseErr := parseCodexJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), codexCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelError, "codex_cli: parse failed",
			slog.String("provider", codexCLIProvider),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("codex_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "codex_cli: success",
		slog.String("provider", codexCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

func buildCodexCLIArgs(modelID, workspacePath string) []string {
	args := []string{
		"-q",
		"--format", "json",
		"--full-auto",
	}
	if workspacePath != "" {
		args = append(args, "--cd", workspacePath)
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

// parseCodexJSON decodes the JSON envelope emitted by `codex --format
// json` into a SendResponse. It tolerates multiple envelope shapes:
//   - flat: {"text":"…","stop_reason":"stop","usage":{…}}
//   - nested message: {"message":{"content":"…"},…}
//   - nested tokens: {"tokens":{"input":N,"output":N}}
//   - error: {"is_error":true,"error":"…"}
//
// Usage fields may be absent (headless mode, older CLI versions); when
// missing, token counts and cost are reported as zero rather than
// inventing values.
func parseCodexJSON(raw []byte) (*SendResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("codex_cli: empty output")
	}
	// Codex may emit NDJSON (multiple lines). Walk lines looking for
	// the result envelope — skip banners and non-JSON prefix lines.
	var env codexCLIEvent
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r\n")
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		// Accept the first parseable JSON object that looks like a
		// result (has text/result/message or is an error).
		if env.Text != "" || env.Result != "" || env.Message.Content != "" || env.IsError || env.Error != "" || env.Usage.OutputTokens > 0 || env.OutputTokens > 0 {
			found = true
			break
		}
	}
	if !found {
		// No recognisable envelope found — try the whole blob as one
		// JSON object (single-envelope mode).
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("codex_cli: no JSON envelope in output")
		}
	}

	if env.IsError || env.Error != "" {
		errMsg := env.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return nil, fmt.Errorf("codex_cli: error: %s", errMsg)
	}

	text := env.Text
	if text == "" {
		text = env.Result
	}
	if text == "" {
		text = env.Message.Content
	}

	inputTokens := env.Usage.InputTokens + env.Usage.CacheReadInputTokens + env.Usage.CacheCreationInputTokens
	outputTokens := env.Usage.OutputTokens
	if outputTokens == 0 {
		outputTokens = env.OutputTokens
	}
	if outputTokens == 0 {
		outputTokens = env.Tokens.Output
	}
	if inputTokens == 0 {
		inputTokens = env.Tokens.Input
	}

	stopReason := env.StopReason
	if stopReason == "" {
		stopReason = env.Message.StopReason
	}

	return &SendResponse{
		Text:         strings.TrimSpace(text),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostUSD:      env.CostUSD,
		StopReason:   normalizeCodexStop(stopReason),
	}, nil
}

func normalizeCodexStop(s string) string {
	switch s {
	case "stop", "end_turn":
		return StopEndTurn
	case "tool_use", "tool_use_end", "tool_calls":
		return StopToolUse
	case "max_tokens", "length":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}

func codexExecRunner(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
	return runSandboxedModelCLI(
		ctx, binary, args, stdin, workspacePath,
		codexCLISandboxConfig, codexCLIEnvironmentPolicy(),
	)
}
