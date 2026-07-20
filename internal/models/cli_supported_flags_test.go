package models

import (
	"context"
	"strings"
	"testing"
)

// This file is the cross-adapter guard against the failure mode that took
// gemini_cli and codex_cli off the air: an adapter emitting a flag the
// installed CLI does not have, locked in place by a unit test that asserted
// the broken argv. Every CLI in the matrix below parses argv STRICTLY — an
// unknown flag aborts the process before the prompt is read — so a wrong flag
// is a total launch failure, not a degraded run.
//
// Each supported-flag set was transcribed from the REAL `--help` of the
// version named in the comment, probed under an isolated HOME. When you bump
// a CLI, re-probe it and update the set; do not add an entry from memory.
//
// ---------------------------------------------------------------------------
// VERIFIED FLAG MATRIX — probed 2026-07-20 on darwin/arm64.
//
// This evidence ROTS the moment any of these CLIs updates. Treat a version
// mismatch as "unverified", not "fine": every one of these parsers is STRICT,
// so a retired flag is a total launch failure, not a degraded run.
//
//	adapter   binary version      parser + unknown-flag behaviour      status
//	--------  ------------------  -----------------------------------  ------
//	claude    2.1.215             commander, "error: unknown option"   clean
//	codex     codex-cli 0.144.5   clap, "unexpected argument"          FIXED
//	gemini    0.33.0              yargs strict, "Unknown argument"     FIXED
//	grok      0.2.103             clap, "unexpected argument"          clean
//	mimo      0.1.6               yargs, help + exit 1                 clean
//	opencode  1.17.4              yargs, help + exit 1                 clean
//	pi        0.80.7              (verified live by team lead)         clean
//
// Two findings worth carrying forward:
//
//  1. ABSENCE FROM --help IS NOT ABSENCE. grok accepts --no-auto-update even
//     though it appears nowhere in `grok --help`. Probe the flag directly
//     before deleting it as "unsupported".
//  2. FLAG EXISTENCE IS NOT ENOUGH. codex's flags were only half the bug —
//     headless mode is the `exec` SUBCOMMAND (a bare `codex` opens the
//     interactive TUI) and `exec` refuses to start outside a git repo. See
//     TestCodexCLIUsesExecSubcommand.
//
// Re-probe recipe (read-only, never touches real credentials):
//
//	env -i PATH="$PATH" HOME=/tmp/probehome \
//	  XDG_CONFIG_HOME=/tmp/probehome/config XDG_DATA_HOME=/tmp/probehome/data \
//	  XDG_CACHE_HOME=/tmp/probehome/cache XDG_STATE_HOME=/tmp/probehome/state \
//	  TERM=dumb NO_COLOR=1 <bin> --help
//
// ---------------------------------------------------------------------------

// codexSupportedFlags — codex-cli 0.144.5, `codex exec --help`.
// NB: -q, --format and --full-auto were REMOVED upstream and are rejected
// with "error: unexpected argument"; headless mode is the `exec` subcommand.
var codexSupportedFlags = map[string]bool{
	"--config": true, "--enable": true, "--disable": true,
	"--strict-config": true, "--image": true, "--model": true,
	"--oss": true, "--local-provider": true, "--profile": true,
	"--sandbox": true, "--dangerously-bypass-approvals-and-sandbox": true,
	"--dangerously-bypass-hook-trust": true, "--cd": true, "--add-dir": true,
	"--skip-git-repo-check": true, "--ephemeral": true,
	"--ignore-user-config": true, "--ignore-rules": true,
	"--output-schema": true, "--color": true, "--json": true,
	"--output-last-message": true, "--help": true, "--version": true,
}

// grokSupportedFlags — grok 0.2.103, `grok --help`.
// --no-auto-update is accepted by the binary but NOT listed in --help; it is
// verified by probe (a genuinely unknown flag yields "error: unexpected
// argument"), so absence from --help alone must not be read as absence.
var grokSupportedFlags = map[string]bool{
	"--agent": true, "--agents": true, "--allow": true, "--always-approve": true,
	"--best-of-n": true, "--continue": true, "--check": true, "--cwd": true,
	"--debug": true, "--debug-file": true, "--deny": true,
	"--disable-web-search": true, "--disallowed-tools": true,
	"--experimental-memory": true, "--fork-session": true, "--fullscreen": true,
	"--help": true, "--json-schema": true, "--leader-socket": true,
	"--model": true, "--max-turns": true, "--minimal": true,
	"--no-alt-screen": true, "--no-memory": true, "--no-plan": true,
	"--no-subagents": true, "--oauth": true, "--output-format": true,
	"--single": true, "--permission-mode": true, "--prompt-file": true,
	"--prompt-json": true, "--resume": true, "--reasoning-effort": true,
	"--restore-code": true, "--rules": true, "--session-id": true,
	"--sandbox": true, "--system-prompt-override": true, "--tools": true,
	"--version": true, "--verbatim": true, "--worktree": true,
	"--worktree-ref": true, "--no-auto-update": true,
}

// opencodeSupportedFlags — opencode 1.17.4, `opencode run --help`.
var opencodeSupportedFlags = map[string]bool{
	"--help": true, "--version": true, "--print-logs": true, "--log-level": true,
	"--pure": true, "--command": true, "--continue": true, "--session": true,
	"--fork": true, "--share": true, "--model": true, "--agent": true,
	"--format": true, "--file": true, "--title": true, "--attach": true,
	"--password": true, "--username": true, "--dir": true, "--port": true,
	"--variant": true, "--thinking": true, "--replay": true,
	"--replay-limit": true, "--interactive": true,
	"--dangerously-skip-permissions": true, "--demo": true,
}

// mimoSupportedFlags — mimo 0.1.6, `mimo run --help`.
var mimoSupportedFlags = map[string]bool{
	"--help": true, "--version": true, "--print-logs": true, "--log-level": true,
	"--pure": true, "--command": true, "--continue": true, "--session": true,
	"--fork": true, "--share": true, "--model": true, "--agent": true,
	"--format": true, "--file": true, "--title": true, "--attach": true,
	"--password": true, "--dir": true, "--port": true, "--variant": true,
	"--thinking": true, "--role": true,
	"--dangerously-skip-permissions": true,
}

// claudeSupportedFlags — claude 2.1.215, `claude --help` (subset covering
// every flag the adapter emits, plus close neighbours that must not be
// confused with them, e.g. --allowed-tools is NOT --tools).
var claudeSupportedFlags = map[string]bool{
	"--print": true, "--output-format": true, "--tools": true,
	"--no-session-persistence": true, "--dangerously-skip-permissions": true,
	"--model": true, "--add-dir": true, "--agent": true, "--agents": true,
	"--allowed-tools": true, "--disallowed-tools": true,
	"--append-system-prompt": true, "--input-format": true,
	"--verbose": true, "--effort": true, "--help": true, "--version": true,
}

// piSupportedFlags — Pi 0.80.7, `pi --help`. Team lead verified this argv
// live; the table keeps it from silently drifting.
var piSupportedFlags = map[string]bool{
	"--print": true, "--mode": true, "--no-session": true, "--approve": true,
	"--thinking": true, "--model": true, "--system-prompt": true,
	"--append-system-prompt": true, "--prompt-template": true,
	"--no-prompt-templates": true, "--help": true, "--version": true,
}

// captureOpenCodeArgs runs the opencode adapter against a fake runner to
// recover the argv, because opencode builds its args inline in Send rather
// than in a standalone builder like the other adapters.
func captureOpenCodeArgs(t *testing.T, modelID, attachURL, workspacePath string) []string {
	t.Helper()
	var got []string
	a := &opencodeCLIAdapter{
		modelID:   modelID,
		attachURL: attachURL,
		runner: func(_ context.Context, _ string, args []string, _ string, _ string) ([]byte, []byte, error) {
			got = append([]string(nil), args...)
			return []byte(`{"type":"step_finish","part":{"text":"ok","reason":"stop"}}`), nil, nil
		},
	}
	if _, err := a.Send(context.Background(), SendRequest{
		Messages:      []Message{{Role: RoleUser, Content: "hi"}},
		WorkspacePath: workspacePath,
	}); err != nil {
		t.Fatalf("opencode Send: %v", err)
	}
	return got
}

// TestCLIAdaptersEmitOnlySupportedFlags asserts that every `--flag` any
// adapter puts on the command line actually exists in the CLI it targets.
// A failure here means the adapter cannot launch at all.
func TestCLIAdaptersEmitOnlySupportedFlags(t *testing.T) {
	t.Parallel()
	const ws = "/tmp/project"
	cases := []struct {
		name      string
		cli       string
		argv      []string
		supported map[string]bool
	}{
		{"codex/with-workspace", "codex-cli 0.144.5", buildCodexCLIArgs("o3", ws), codexSupportedFlags},
		{"codex/bare", "codex-cli 0.144.5", buildCodexCLIArgs("", ""), codexSupportedFlags},
		{"grok/with-workspace", "grok 0.2.103", buildGrokCLIArgs("grok-4", ws), grokSupportedFlags},
		{"grok/bare", "grok 0.2.103", buildGrokCLIArgs("", ""), grokSupportedFlags},
		{"mimo/with-workspace", "mimo 0.1.6", buildMimoCLIArgs("m", "http://x", ws), mimoSupportedFlags},
		{"mimo/bare", "mimo 0.1.6", buildMimoCLIArgs("", "", ""), mimoSupportedFlags},
		{"claude/with-model", "claude 2.1.215", buildClaudeCLIArgs("sonnet", SendRequest{}), claudeSupportedFlags},
		{"claude/bare", "claude 2.1.215", buildClaudeCLIArgs("", SendRequest{}), claudeSupportedFlags},
		{"pi/with-model", "Pi 0.80.7", buildPiCLIArgs("local"), piSupportedFlags},
		{"pi/bare", "Pi 0.80.7", buildPiCLIArgs(""), piSupportedFlags},
		{"gemini/with-model", "gemini 0.33.0", buildGeminiCLIArgs("gemini-2.5-pro"), geminiCLISupportedFlags},
		{"gemini/bare", "gemini 0.33.0", buildGeminiCLIArgs(""), geminiCLISupportedFlags},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertOnlySupportedFlags(t, tc.cli, tc.argv, tc.supported)
		})
	}

	t.Run("opencode/attached", func(t *testing.T) {
		t.Parallel()
		assertOnlySupportedFlags(t, "opencode 1.17.4",
			captureOpenCodeArgs(t, "anthropic/sonnet", "http://localhost:4096", ws),
			opencodeSupportedFlags)
	})
	t.Run("opencode/local", func(t *testing.T) {
		t.Parallel()
		assertOnlySupportedFlags(t, "opencode 1.17.4",
			captureOpenCodeArgs(t, "anthropic/sonnet", "", ws),
			opencodeSupportedFlags)
	})
}

func assertOnlySupportedFlags(t *testing.T, cli string, argv []string, supported map[string]bool) {
	t.Helper()
	if len(argv) == 0 {
		t.Fatalf("%s: adapter emitted no argv at all", cli)
	}
	for _, arg := range argv {
		if !strings.HasPrefix(arg, "--") {
			continue // subcommand or flag value
		}
		// A flag passed as --key=value still names --key.
		name := arg
		if eq := strings.Index(name, "="); eq > 0 {
			name = name[:eq]
		}
		if !supported[name] {
			t.Errorf("%s: adapter emits %q, which that CLI does not accept — "+
				"it parses argv strictly and will abort before reading the prompt.\nfull argv: %v",
				cli, name, argv)
		}
	}
}

// TestCodexCLIUsesExecSubcommand pins the other half of the codex fix that a
// flag-existence check cannot catch: `codex exec` is what runs headlessly.
// Without the subcommand every flag below is still valid, but codex launches
// its INTERACTIVE TUI and the worker hangs until its wall-clock cap.
func TestCodexCLIUsesExecSubcommand(t *testing.T) {
	t.Parallel()
	args := buildCodexCLIArgs("o3", "/tmp/project")
	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("codex argv must start with the exec subcommand, got %v", args)
	}
	for _, retired := range []string{"-q", "--format", "--full-auto"} {
		for _, arg := range args {
			if arg == retired {
				t.Errorf("codex argv still carries retired flag %q (rejected by 0.144.5): %v", retired, args)
			}
		}
	}
	// codex exec refuses to start outside a trusted/git directory, and a
	// worker workspace is not guaranteed to be a repo.
	if !containsArg(args, "--skip-git-repo-check") {
		t.Errorf("codex argv must pass --skip-git-repo-check or exec aborts "+
			"with \"Not inside a trusted directory\": %v", args)
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
