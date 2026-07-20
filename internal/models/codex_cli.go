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

// buildCodexCLIArgs assembles the argv for a headless codex run. Verified
// against codex-cli 0.144.5; codex parses argv with clap in STRICT mode, so
// any flag that does not exist aborts the run before the prompt is read.
//
// `exec` is mandatory: headless mode is a SUBCOMMAND, and a bare
// `codex <flags> <prompt>` starts the interactive TUI instead. The retired
// flags this replaces were all rejected outright by 0.144.5
// ("error: unexpected argument '-q' found", likewise --format, --full-auto):
//   - -q / --format json  ->  --json (JSONL event stream on stdout)
//   - --full-auto         ->  --sandbox workspace-write. `exec` never prompts
//     for approval, so the auto-approve half of --full-auto is implicit and
//     only the workspace-writable half needs stating. This deliberately keeps
//     codex's own sandbox on rather than escalating to
//     --dangerously-bypass-approvals-and-sandbox.
//
// --skip-git-repo-check is required because `codex exec` refuses to start
// outside a trusted directory ("Not inside a trusted directory and
// --skip-git-repo-check was not specified") and a worker workspace is not
// guaranteed to be a git repo. The mcplexer OS sandbox (codexCLISandboxConfig)
// remains the real filesystem boundary.
func buildCodexCLIArgs(modelID, workspacePath string) []string {
	args := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
	}
	if workspacePath != "" {
		args = append(args, "--cd", workspacePath)
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

func codexExecRunner(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
	return runSandboxedModelCLI(
		ctx, binary, args, stdin, workspacePath,
		codexCLISandboxConfig, codexCLIEnvironmentPolicy(),
	)
}
