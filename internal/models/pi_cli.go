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
	piCLIDefaultBinary = "pi"
	piCLIErrOutputLen  = 256
	piCLIProvider      = "pi_cli"
)

// piCLIAdapter shells out to the locally-installed `pi` CLI (the Pi
// coding harness, pi.dev / @earendil-works/pi-coding-agent) in
// non-interactive JSON mode. The model + provider routing live in the
// host's ~/.pi/agent/models.json — Pi resolves the provider key from
// ModelID itself — so this adapter passes only --model <ModelID> plus the
// non-interactive flag set. Credentials stay with the host install;
// mcplexer never reads or stores them.
//
// SECURITY: the prompt (System + Messages flattened) is NEVER passed in
// argv — argv is world-readable via `ps auxww` on multi-tenant boxes, so a
// positional `pi -p <message>` would leak the full system+user prompt to
// any local user. Instead the composed prompt is fed via stdin, matching
// claude_cli/codex_cli (see L7 in the security audit). Pi reads piped
// stdin in non-interactive `-p`/json mode and uses it as the initial
// message (pi-coding-agent dist/cli/initial-message.js + readPipedStdin in
// main.js: stdin is consumed when it is not a TTY). `--thinking off` is
// MANDATORY: against local reasoning models Pi otherwise loops forever in
// its reasoning trace.
type piCLIAdapter struct {
	binaryPath string
	modelID    string
	runner     piCLIRunner
}

// piCLIRunner is the test seam. prompt is fed to the subprocess via stdin
// by the production runner (never as an argv element), keeping the
// system + user text out of `ps`.
type piCLIRunner func(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error)

func newPiCLIAdapter(binaryPath, modelID string) *piCLIAdapter {
	if binaryPath == "" {
		binaryPath = piCLIDefaultBinary
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, PiCLIBinaryEnvVar, piStandardPaths)
	}
	return &piCLIAdapter{
		binaryPath: binaryPath,
		modelID:    modelID,
		runner:     piExecRunner,
	}
}

func piStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".pi", "bin", "pi"),
			filepath.Join(home, ".local", "bin", "pi"),
		)
	}
	return append(out,
		"/opt/homebrew/bin/pi",
		"/usr/local/bin/pi",
	)
}

func (a *piCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildPiCLIArgs(a.modelID)
	prompt := buildClaudeCLIStdin(req.System, req.Messages)
	// SECURITY: the composed prompt (system + user text) is fed via stdin,
	// NOT appended to argv — argv is world-readable via `ps` (L7). Pi reads
	// piped stdin as the initial message in non-interactive mode.
	slog.LogAttrs(ctx, slog.LevelDebug, "pi_cli: dispatch",
		slog.String("provider", piCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(prompt)),
	)
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, prompt, req.WorkspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), piCLIErrOutputLen)
		ctxReason := ""
		if cerr := ctx.Err(); cerr != nil {
			ctxReason = cerr.Error()
			if cause := context.Cause(ctx); cause != nil && cause.Error() != cerr.Error() {
				ctxReason = cerr.Error() + " (cause: " + cause.Error() + ")"
			}
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "pi_cli: non-zero exit",
			slog.String("provider", piCLIProvider),
			slog.String("model_id", a.modelID),
			slog.String("binary", a.binaryPath),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
			slog.String("ctx_error", ctxReason),
		)
		if ctxReason != "" {
			return nil, fmt.Errorf("pi_cli: run: %w (ctx: %s, stderr: %s)", err, ctxReason, snip)
		}
		return nil, fmt.Errorf("pi_cli: run: %w (stderr: %s)", err, snip)
	}
	resp, parseErr := parsePiJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), piCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelError, "pi_cli: parse failed",
			slog.String("provider", piCLIProvider),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("pi_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "pi_cli: success",
		slog.String("provider", piCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

// buildPiCLIArgs assembles the non-interactive flag set. The prompt is
// NOT included here — it is fed via stdin by the runner (Pi reads piped
// stdin as the initial message), keeping it out of argv. --thinking off is
// MANDATORY for local reasoning models.
func buildPiCLIArgs(modelID string) []string {
	args := []string{
		"-p",
		"--mode", "json",
		"--no-session",
		"--approve",
		"--thinking", "off",
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

func piExecRunner(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	// SECURITY (L7): the shared runner feeds the composed prompt via stdin,
	// not argv, so the system + user text never appears in `ps`.
	return runSandboxedModelCLI(
		ctx, binary, args, prompt, workspacePath,
		piCLISandboxConfig, piCLIEnvironmentPolicy(),
	)
}
