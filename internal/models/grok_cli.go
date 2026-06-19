package models

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/sandbox"
)

const (
	grokCLIDefaultBinary = "grok"
	grokCLIErrOutputLen  = 256
	grokCLIProvider      = "grok_cli"
)

// grokCLIAdapter shells out to xAI's `grok` CLI in headless JSON mode.
// Credentials stay with the host CLI (`grok login` or XAI_API_KEY).
// Observed Grok CLI JSON output may omit usage/cost fields; when absent,
// mcplexer records token/cost metrics as zero rather than inventing values.
type grokCLIAdapter struct {
	binaryPath string
	modelID    string
	runner     grokCLIRunner
}

// grokCLIRunner is the test seam. prompt is written to --prompt-file by
// the production runner so sensitive system text never appears in argv.
type grokCLIRunner func(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error)

func newGrokCLIAdapter(binaryPath, modelID string) *grokCLIAdapter {
	if binaryPath == "" {
		binaryPath = grokCLIDefaultBinary
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, GrokCLIBinaryEnvVar, grokStandardPaths)
	}
	return &grokCLIAdapter{
		binaryPath: binaryPath,
		modelID:    modelID,
		runner:     grokExecRunner,
	}
}

func grokStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".grok", "downloads", "grok-macos-aarch64"),
			filepath.Join(home, ".local", "bin", "grok"),
			filepath.Join(home, "bin", "grok"),
		)
	}
	return append(out,
		"/Applications/cmux.app/Contents/Resources/bin/grok",
		"/opt/homebrew/bin/grok",
		"/usr/local/bin/grok",
	)
}

func (a *grokCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildGrokCLIArgs(a.modelID, req.WorkspacePath)
	prompt := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "grok_cli: dispatch",
		slog.String("provider", grokCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(prompt)),
	)
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, prompt, req.WorkspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		snip := truncate(string(stderr), grokCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelWarn, "grok_cli: non-zero exit",
			slog.String("provider", grokCLIProvider),
			slog.String("model_id", a.modelID),
			slog.String("binary", a.binaryPath),
			slog.Int64("duration_ms", durMS),
			slog.String("stderr_truncated", snip),
		)
		return nil, fmt.Errorf("grok_cli: run: %w (stderr: %s)", err, snip)
	}
	resp, parseErr := parseGrokJSON(stdout)
	if parseErr != nil {
		snip := truncate(string(stdout), grokCLIErrOutputLen)
		slog.LogAttrs(ctx, slog.LevelError, "grok_cli: parse failed",
			slog.String("provider", grokCLIProvider),
			slog.String("model_id", a.modelID),
			slog.Int64("duration_ms", durMS),
			slog.String("stdout_truncated", snip),
			slog.String("parse_error", parseErr.Error()),
		)
		return nil, fmt.Errorf("grok_cli: parse output: %w", parseErr)
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "grok_cli: success",
		slog.String("provider", grokCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Float64("cost_usd", resp.CostUSD),
	)
	return resp, nil
}

func buildGrokCLIArgs(modelID, workspacePath string) []string {
	args := []string{
		"--no-auto-update",
		"--output-format", "json",
		"--always-approve",
		"--no-alt-screen",
		"--no-memory",
	}
	if workspacePath != "" {
		args = append(args, "--cwd", workspacePath)
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

func grokExecRunner(ctx context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	promptFile, cleanup, err := writeGrokPromptFile(prompt)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	if promptFile != "" {
		args = append(append([]string(nil), args...), "--prompt-file", promptFile)
	}
	wrapper := sandbox.NewCommandWrapper(grokCLISandboxConfig())
	program, wrappedArgs, cleanupSandbox := wrapper.Wrap(binary, args)
	defer cleanupSandbox()

	// newSandboxedCLICmd isolates the subprocess into its own process
	// group and kills the whole group on ctx cancellation, so operator
	// hard-stop terminates the real `grok` process — not just the
	// sandbox-exec wrapper — and reclaims the token budget promptly.
	cmd := newSandboxedCLICmd(ctx, program, wrappedArgs)
	if workspacePath != "" {
		cmd.Dir = workspacePath
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func writeGrokPromptFile(prompt string) (string, func(), error) {
	if prompt == "" {
		return "", func() {}, nil
	}
	f, err := os.CreateTemp("", "mcplexer-grok-prompt-*.txt")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}

func grokCLISandboxConfig() sandbox.Config {
	return opencodeCLISandboxConfig()
}
