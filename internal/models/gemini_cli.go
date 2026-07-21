package models

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	geminiCLIDefaultBinary = "gemini"
	geminiCLIErrOutputLen  = 256
	geminiCLIProvider      = "gemini_cli"
)

// geminiCLIAdapter shells out to Google's `gemini` CLI in non-interactive
// JSON mode (`gemini --output-format json --sandbox false --model X`).
// Credentials stay with the host CLI (GEMINI_API_KEY or `gemini auth`);
// mcplexer never reads or stores the Gemini API key.
type geminiCLIAdapter struct {
	binaryPath string
	modelID    string
	runner     geminiCLIRunner
}

// geminiCLIRunner is the test seam. prompt is passed via stdin.
type geminiCLIRunner func(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error)

func newGeminiCLIAdapter(binaryPath, modelID string) *geminiCLIAdapter {
	if binaryPath == "" {
		binaryPath = geminiCLIDefaultBinary
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, GeminiCLIBinaryEnvVar, geminiStandardPaths)
	}
	return &geminiCLIAdapter{
		binaryPath: binaryPath,
		modelID:    modelID,
		runner:     geminiExecRunner,
	}
}

func geminiStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".gemini", "bin", "gemini"),
			filepath.Join(home, ".local", "bin", "gemini"),
		)
	}
	return append(out,
		"/opt/homebrew/bin/gemini",
		"/usr/local/bin/gemini",
	)
}

func (a *geminiCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildGeminiCLIArgs(a.modelID, req.WorkspacePath)
	prompt := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "gemini_cli: dispatch",
		slog.String("provider", geminiCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(prompt)),
	)
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, prompt, req.WorkspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), geminiCLIErrOutputLen)
		ctxReason := ""
		if cerr := ctx.Err(); cerr != nil {
			ctxReason = cerr.Error()
			if cause := context.Cause(ctx); cause != nil && cause.Error() != cerr.Error() {
				ctxReason = cerr.Error() + " (cause: " + cause.Error() + ")"
			}
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "gemini_cli: non-zero exit",
			slog.String("provider", geminiCLIProvider),
			slog.String("model_id", a.modelID),
			slog.String("binary", a.binaryPath),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
			slog.String("ctx_error", ctxReason),
		)
		if ctxReason != "" {
			return nil, fmt.Errorf("gemini_cli: run: %w (ctx: %s, stderr: %s)", err, ctxReason, snip)
		}
		return nil, fmt.Errorf("gemini_cli: run: %w (stderr: %s)", err, snip)
	}

	resp, parseErr := parseGeminiJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), geminiCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelError, "gemini_cli: parse failed",
			slog.String("provider", geminiCLIProvider),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("gemini_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "gemini_cli: success",
		slog.String("provider", geminiCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

func buildGeminiCLIArgs(modelID, workspacePath string) []string {
	args := []string{
		"--output-format", "json",
		"--sandbox", "false",
	}
	if workspacePath != "" {
		args = append(args, "--directory", workspacePath)
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

func geminiExecRunner(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	return runSandboxedModelCLI(
		ctx, binary, args, prompt, workspacePath,
		geminiCLISandboxConfig, geminiCLIEnvironmentPolicy(),
	)
}
