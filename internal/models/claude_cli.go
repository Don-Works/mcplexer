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

	"github.com/don-works/mcplexer/internal/sandbox"
)

const (
	claudeCLIDefaultBinary = "claude"
	// claudeCLIErrStderrLen caps stderr included in wrapped exec errors
	// and slog snippets so a chatty subprocess doesn't blow up the
	// run.error column (or fill the log file).
	claudeCLIErrStderrLen = 256
	// claudeCLIProvider is the slog attribute value used so operators
	// can filter by adapter in aggregated log streams.
	claudeCLIProvider = "claude_cli"
)

// claudeCLIAdapter shells out to the locally-installed `claude` CLI in
// non-interactive mode (`claude -p --output-format json`). It bills against
// whatever credentials the host's `claude` install is using — OAuth login
// hits the user's Claude subscription (interactive quota today; the
// separate Agent SDK monthly credit pool after 2026-06-15), while
// ANTHROPIC_API_KEY hits per-token API billing.
//
// The adapter intentionally disables claude's built-in agent tools
// (`--tools ""`), so each Send is a pure LLM round-trip. Tool dispatch
// for the surrounding Worker continues to flow through mcplexer's own
// dispatcher just like every other provider. Full tool-calling parity
// (via stream-json) is a follow-up.
type claudeCLIAdapter struct {
	binaryPath string
	modelID    string
	runner     claudeCLIRunner
}

// claudeCLIRunner is the seam tests use to swap exec.CommandContext.
// In production it's claudeExecRunner; tests substitute a fake that
// returns canned stdout/stderr without touching the filesystem.
// workspacePath, when non-empty, is applied as the subprocess CWD so
// claude's own MCP-back-to-mcplexer connection lands in the worker's
// bound workspace (stdio CWD inference).
type claudeCLIRunner func(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error)

func newClaudeCLIAdapter(binaryPath, modelID string) *claudeCLIAdapter {
	if binaryPath == "" {
		binaryPath = claudeCLIDefaultBinary
	}
	// sandbox-exec ignores the parent's PATH, so a bare binary name
	// like "claude" produces ENOENT inside the sandbox. Resolve here
	// so wrapped invocations always succeed. Order: (1) the
	// MCPLEXER_TEST_CLAUDE_CLI_BIN env override (test rigs only),
	// (2) the Anthropic CLI installer's standard locations, (3) the
	// daemon's PATH via LookPath. Production daemons never set the env.
	if !filepath.IsAbs(binaryPath) {
		binaryPath = resolveBinaryPath(binaryPath, ClaudeCLIBinaryEnvVar, claudeStandardPaths)
	}
	return &claudeCLIAdapter{
		binaryPath: binaryPath,
		modelID:    modelID,
		runner:     claudeExecRunner,
	}
}

// claudeStandardPaths is the fallback search list for the claude
// binary. ~/.local/bin is the standard installer target; the rest
// cover Homebrew (both archs).
func claudeStandardPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".local", "bin", "claude"))
		out = append(out, filepath.Join(home, ".claude", "local", "claude"))
	}
	return append(out,
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
	)
}

// claudeCLIResultEnvelope is the json shape emitted by
// `claude -p --output-format json`. Only the fields we consume are
// declared; claude may add fields without breaking decode.
type claudeCLIResultEnvelope struct {
	Type           string  `json:"type"`
	Subtype        string  `json:"subtype"`
	IsError        bool    `json:"is_error"`
	APIErrorStatus any     `json:"api_error_status"`
	Result         string  `json:"result"`
	StopReason     string  `json:"stop_reason"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	Usage          struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Send executes one `claude -p` invocation and returns the parsed
// envelope as a SendResponse. Bills via the host claude install's
// credentials (OAuth subscription or API key). Returns no ToolCalls
// because claude's internal agent loop is disabled here.
func (a *claudeCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	start := time.Now()
	args := buildClaudeCLIArgs(a.modelID, req)
	stdin := buildClaudeCLIStdin(req.System, req.Messages)
	slog.LogAttrs(ctx, slog.LevelDebug, "claude_cli: dispatch",
		slog.String("provider", claudeCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int("arg_count", len(args)),
		slog.Int("prompt_len", len(stdin)),
	)
	stdout, stderr, err := a.runner(ctx, a.binaryPath, args, stdin, req.WorkspacePath)
	durMS := time.Since(start).Milliseconds()
	if err != nil {
		return nil, a.logExitError(ctx, err, stderr, durMS)
	}
	var env claudeCLIResultEnvelope
	if jerr := json.Unmarshal(stdout, &env); jerr != nil {
		return nil, a.logDecodeError(ctx, jerr, stdout, durMS)
	}
	if env.IsError {
		return nil, a.logEnvelopeError(ctx, env.Result, durMS)
	}
	inputTokens := env.Usage.InputTokens + env.Usage.CacheReadInputTokens + env.Usage.CacheCreationInputTokens
	slog.LogAttrs(ctx, slog.LevelDebug, "claude_cli: success",
		slog.String("provider", claudeCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.Int("input_tokens", inputTokens),
		slog.Int("output_tokens", env.Usage.OutputTokens),
		slog.Float64("cost_usd", env.TotalCostUSD),
	)
	return &SendResponse{
		Text:         env.Result,
		InputTokens:  inputTokens,
		OutputTokens: env.Usage.OutputTokens,
		CostUSD:      env.TotalCostUSD,
		StopReason:   normalizeClaudeCLIStop(env.StopReason),
	}, nil
}

// logExitError emits a Warn record for a non-zero subprocess exit and
// returns the wrapped error that callers see. Centralizing the log+wrap
// pair keeps Send under the 50-line cap and guarantees that operators
// reading slog see the same stderr snippet that appears in audit.
func (a *claudeCLIAdapter) logExitError(ctx context.Context, runErr error, stderr []byte, durMS int64) error {
	stderrSnip := truncate(string(stderr), claudeCLIErrStderrLen)
	slog.LogAttrs(ctx, slog.LevelWarn, "claude_cli: non-zero exit",
		slog.String("provider", claudeCLIProvider),
		slog.String("model_id", a.modelID),
		slog.String("binary", a.binaryPath),
		slog.Int64("duration_ms", durMS),
		slog.String("exit_error", runErr.Error()),
		slog.String("stderr_truncated", stderrSnip),
	)
	return fmt.Errorf("claude_cli: run: %w (stderr: %s)", runErr, stderrSnip)
}

// logDecodeError emits an Error record when the result envelope is
// unparseable JSON and returns the wrapped error.
func (a *claudeCLIAdapter) logDecodeError(ctx context.Context, jerr error, stdout []byte, durMS int64) error {
	stdoutSnip := truncate(string(stdout), claudeCLIErrStderrLen)
	slog.LogAttrs(ctx, slog.LevelError, "claude_cli: decode envelope failed",
		slog.String("provider", claudeCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.String("stdout_truncated", stdoutSnip),
		slog.String("decode_error", jerr.Error()),
	)
	return fmt.Errorf("claude_cli: decode result envelope: %w (stdout: %s)", jerr, stdoutSnip)
}

// logEnvelopeError emits a Warn record for an `is_error:true` envelope
// (claude itself signalling an upstream failure: rate-limit, OAuth
// expiry, etc.) and returns the wrapped error.
func (a *claudeCLIAdapter) logEnvelopeError(ctx context.Context, result string, durMS int64) error {
	errSnip := truncate(result, claudeCLIErrStderrLen)
	slog.LogAttrs(ctx, slog.LevelWarn, "claude_cli: error envelope",
		slog.String("provider", claudeCLIProvider),
		slog.String("model_id", a.modelID),
		slog.Int64("duration_ms", durMS),
		slog.String("error_result", errSnip),
	)
	return fmt.Errorf("claude_cli: error result: %s", result)
}

// buildClaudeCLIArgs assembles the argv for `claude -p`. We always pass
// --no-session-persistence so each Worker dispatch is a fresh session
// (no cross-run contamination via the user's session history) and
// --tools default so the host `claude` binary's built-in agent tools
// (Read, Glob, Grep, Edit, Bash, Task, ...) are available — many
// autonomous workers operate on the workspace's root_path filesystem
// (markdown CRMs, generated reports, source repos) and need them.
// Tool execution is still sandboxed via claudeCLISandboxConfig (no
// writes to ~/.claude or ~/.mcplexer), so prompt-injection cannot
// rewrite the gateway DB or install a malicious PreToolUse hook.
//
// --dangerously-skip-permissions is required for autonomous workers:
// in unattended mode the host `claude` binary has no human to approve
// MCP tool calls (mcpx__execute_code, downstream namespaces), so without
// this flag every call gets auto-denied and the worker reports
// "blocked on permissions" without doing any real work. The flag is
// safe in this context because the gateway-side allowlist
// (tool_allowlist_json) still gates which MCP tools the run can dispatch
// — claude itself bypassing its own approval prompts does not weaken
// the mcplexer policy that actually decides what executes.
//
// SECURITY: the system prompt is NEVER passed in argv — argv is
// world-readable via `ps auxww` on multi-tenant boxes. It's prepended
// onto stdin by buildClaudeCLIStdin instead (see L7 in security audit).
func buildClaudeCLIArgs(modelID string, _ SendRequest) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--tools", "default",
		"--no-session-persistence",
		"--dangerously-skip-permissions",
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args
}

// buildClaudeCLIStdin assembles the stdin payload for `claude -p`.
// The system prompt (if non-empty) is prepended as a "SYSTEM:" preamble
// followed by the flattened message list. Routing the system prompt
// through stdin instead of `--append-system-prompt` keeps it out of
// argv (and out of `ps`), which is L7 in the security audit.
func buildClaudeCLIStdin(system string, msgs []Message) string {
	body := flattenMessagesForCLI(msgs)
	if system == "" {
		return body
	}
	if body == "" {
		return "SYSTEM: " + system
	}
	return "SYSTEM: " + system + "\n\n" + body
}

// flattenMessagesForCLI collapses Messages into a single stdin prompt.
// In M0 practice the runner only ever calls claude_cli once per Worker
// dispatch with a single user message (since this adapter returns no
// ToolCalls and the loop terminates after one round-trip). The flatten
// path handles longer message lists defensively for future use.
func flattenMessagesForCLI(msgs []Message) string {
	if len(msgs) == 1 && msgs[0].Role == RoleUser {
		return msgs[0].Content
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.ToUpper(string(m.Role)))
		b.WriteString(": ")
		b.WriteString(m.Content)
	}
	return b.String()
}

func normalizeClaudeCLIStop(s string) string {
	switch s {
	case "end_turn":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	default:
		return StopOther
	}
}

// claudeExecRunner is the production claudeCLIRunner. It wraps the
// invocation in mcplexer's sandbox (sandbox-exec on macOS; identity
// transform on hosts without a usable driver), feeds prompt to stdin,
// captures stdout + stderr. See claudeCLISandboxConfig for the policy.
func claudeExecRunner(ctx context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
	wrapper := sandbox.NewCommandWrapper(claudeCLISandboxConfig())
	program, wrappedArgs, cleanup := wrapper.Wrap(binary, args)
	defer cleanup()

	// newSandboxedCLICmd isolates the subprocess into its own process
	// group and kills the whole group on ctx cancellation, so operator
	// hard-stop terminates the real `claude` process — not just the
	// sandbox-exec wrapper — and reclaims the token budget promptly.
	cmd := newSandboxedCLICmd(ctx, program, wrappedArgs)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if workspacePath != "" {
		cmd.Dir = workspacePath
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// claudeCLISandboxConfig returns the sandbox config used for every
// claude_cli subprocess. Network is host-passthrough until the
// mcplexer-proxy egress filter lands. ~/.claude/ goes into
// DenyWritePaths (read-allow + write-deny) so the CLI can READ
// .credentials.json for OAuth but cannot WRITE settings.json or
// install a malicious PreToolUse hook via prompt-injection (H3).
func claudeCLISandboxConfig() sandbox.Config {
	return sandbox.Config{
		Network: sandbox.NetworkHost,
		DenyPaths: []string{
			homeRelative(".gnupg"),
			homeRelative(".kube"),
			homeRelative(".config/gh"),
			homeRelative(".config/gcloud"),
			homeRelative(".npmrc"),
			homeRelative(".pypirc"),
			homeRelative(".netrc"),
		},
		DenyWritePaths: []string{
			homeRelative(".claude"),
		},
	}
}

// homeRelative joins $HOME + suffix, returning "" if HOME is unset so
// the deny path is dropped (sandbox.MergeDenyPaths filters empties).
func homeRelative(suffix string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return home + "/" + suffix
}
