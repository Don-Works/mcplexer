package models

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/sandbox"
)

const (
	mimoCLIDefaultBinary = "mimo"
	mimoCLIErrOutputLen  = 256
	mimoCLIProvider      = "mimo_cli"
	maxMiMoAttempts      = 2
)

// mimoCLIAdapter shells out to Xiaomi's native `mimo` / mimocode CLI in
// non-interactive JSON mode (`mimo run --pure --format json --model X`).
// Credentials stay with the host CLI (`mimo providers login` / mimocode
// auth.json); mcplexer never reads or stores the MiMo API key.
type mimoCLIAdapter struct {
	binaryPath string
	attachURL  string
	modelID    string
	runner     mimoCLIRunner
}

// mimoCLIRunner is the test seam.
type mimoCLIRunner func(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error)

func newMiMoCLIAdapter(endpointURL, modelID string) *mimoCLIAdapter {
	var attachURL string
	binaryPath := endpointURL
	if isHTTPURL(endpointURL) {
		attachURL = strings.TrimSpace(endpointURL)
		binaryPath = ""
	}
	if binaryPath == "" {
		binaryPath = mimoCLIDefaultBinary
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, MiMoCLIBinaryEnvVar, mimoStandardPaths)
	}
	return &mimoCLIAdapter{
		binaryPath: binaryPath,
		attachURL:  attachURL,
		modelID:    modelID,
		runner:     mimoExecRunner,
	}
}

func mimoStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".mimo", "bin", "mimo"),
			filepath.Join(home, ".mimocode", "bin", "mimo"),
			filepath.Join(home, ".local", "bin", "mimo"),
			filepath.Join(home, ".bun", "bin", "mimo"),
			filepath.Join(home, "bin", "mimo"),
		)
	}
	return append(out,
		"/opt/homebrew/bin/mimo",
		"/usr/local/bin/mimo",
	)
}

func (a *mimoCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildMimoCLIArgs(a.modelID, a.attachURL, req.WorkspacePath)
	prompt := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "mimo_cli: dispatch",
		slog.String("provider", mimoCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(prompt)),
	)
	var resp *SendResponse
	var err error
	for attempt := 1; attempt <= maxMiMoAttempts; attempt++ {
		resp, err = a.runAndParseRetrying(ctx, args, prompt, req.WorkspacePath, start)
		if err != nil {
			return nil, err
		}
		if resp.Text != "" || attempt == maxMiMoAttempts {
			break
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "mimo_cli: empty assistant text - retrying once",
			slog.String("model_id", a.modelID),
			slog.Int("attempt", attempt),
			slog.Int("output_tokens", resp.OutputTokens),
			slog.String("stop_reason", resp.StopReason),
		)
	}
	if resp.Text == "" {
		slog.LogAttrs(ctx, slog.LevelWarn, "mimo_cli: no final assistant text after retries",
			slog.String("model_id", a.modelID),
			slog.Int("attempts", maxMiMoAttempts),
			slog.Int("output_tokens", resp.OutputTokens),
		)
	}
	return resp, nil
}

// buildMimoCLIArgs assembles the argv for `mimo run`. Mirrors
// buildClaudeCLIArgs' permission posture.
//
// --dangerously-skip-permissions is required for autonomous workers: in
// headless --pure mode mimo has no human to approve its interactive
// permission prompts, so any path it deems out-of-scope (e.g. an
// external_directory such as a /tmp scratch dir the task creates via
// os.MkdirTemp) is auto-REJECTED — and the run then wedges until the
// wall-clock cap kills it, burning the whole budget for zero output.
// Observed: a 32-min run, 62 tool calls, 0 tokens, stderr "permission
// requested: external_directory (/tmp/mcplexer-recall-event-...);
// auto-rejecting". The flag is safe here because the OS sandbox wrapper
// (mimoCLISandboxConfig → sandbox-exec/bwrap) and the gateway-side tool
// allowlist remain the real boundaries; mimo skipping its own prompts
// does not weaken mcplexer policy.
func buildMimoCLIArgs(modelID, attachURL, workspacePath string) []string {
	args := []string{"run", "--pure", "--format", "json", "--dangerously-skip-permissions"}
	if attachURL != "" {
		args = append(args, "--attach", attachURL)
	}
	if workspacePath != "" {
		args = append(args, "--dir", workspacePath)
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

func (a *mimoCLIAdapter) runAndParseRetrying(
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
		slog.LogAttrs(ctx, slog.LevelWarn, "mimo_cli: transient server error - retrying after backoff",
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

func (a *mimoCLIAdapter) runAndParse(
	ctx context.Context, args []string, stdin, workspacePath string, start time.Time,
) (*SendResponse, error) {
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, stdin, workspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), mimoCLIErrOutputLen)
		ctxReason := ""
		if cerr := ctx.Err(); cerr != nil {
			ctxReason = cerr.Error()
			if cause := context.Cause(ctx); cause != nil && cause.Error() != cerr.Error() {
				ctxReason = cerr.Error() + " (cause: " + cause.Error() + ")"
			}
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "mimo_cli: non-zero exit",
			slog.String("provider", mimoCLIProvider),
			slog.String("model_id", a.modelID),
			slog.String("binary", a.binaryPath),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
			slog.String("ctx_error", ctxReason),
		)
		if ctxReason != "" {
			return nil, fmt.Errorf("mimo_cli: run: %w (ctx: %s, stderr: %s)", err, ctxReason, snip)
		}
		return nil, fmt.Errorf("mimo_cli: run: %w (stderr: %s)", err, snip)
	}

	resp, parseErr := parseMimoJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), mimoCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelError, "mimo_cli: parse failed",
			slog.String("provider", mimoCLIProvider),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("mimo_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "mimo_cli: success",
		slog.String("provider", mimoCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

func mimoExecRunner(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
	wrapper := sandbox.NewCommandWrapper(mimoCLISandboxConfig())
	program, wrappedArgs, cleanupSandbox := wrapper.Wrap(binary, args)
	defer cleanupSandbox()

	cmd := newSandboxedCLICmd(ctx, program, wrappedArgs)
	if workspacePath != "" {
		cmd.Dir = workspacePath
	}
	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func mimoCLISandboxConfig() sandbox.Config {
	return opencodeCLISandboxConfig()
}
