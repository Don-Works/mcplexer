package models

import (
	"os"
	"os/exec"
)

// Env vars that pin the absolute path of a provider binary, bypassing
// fallback lists + PATH discovery. Strictly opt-in: production launchd
// plists never set these — only the docker-compose integration harness
// does, so it can stub `claude` / `opencode` with a deterministic shim
// without poisoning $PATH for the rest of the daemon.
//
// Behaviour is "trust the env, but only when it points at an executable
// regular file". A stale/wrong env that points at nothing falls through
// to the existing fallback-list + LookPath chain, so a typo doesn't
// silently disable provider dispatch — the operator gets a regular
// "execvp() of X failed" later, same as today.
const (
	// ClaudeCLIBinaryEnvVar pins the claude binary path for the
	// claude_cli adapter. Test-only; production daemons must not set
	// this so OAuth + Homebrew install detection runs normally.
	ClaudeCLIBinaryEnvVar = "MCPLEXER_TEST_CLAUDE_CLI_BIN"
	// OpenCodeCLIBinaryEnvVar pins the opencode binary path for the
	// opencode_cli adapter. Same opt-in posture as ClaudeCLIBinaryEnvVar.
	OpenCodeCLIBinaryEnvVar = "MCPLEXER_TEST_OPENCODE_CLI_BIN"
	// GrokCLIBinaryEnvVar pins the grok binary path for the grok_cli
	// adapter. Same opt-in posture as the other subprocess adapters.
	GrokCLIBinaryEnvVar = "MCPLEXER_TEST_GROK_CLI_BIN"
	// MiMoCLIBinaryEnvVar pins the Xiaomi MiMo CLI binary path for the
	// mimo_cli adapter. Same opt-in posture as the other subprocess adapters.
	MiMoCLIBinaryEnvVar = "MCPLEXER_TEST_MIMO_CLI_BIN"
	// CodexCLIBinaryEnvVar pins the codex binary path for the codex_cli adapter.
	CodexCLIBinaryEnvVar = "MCPLEXER_TEST_CODEX_CLI_BIN"
	// GeminiCLIBinaryEnvVar pins the gemini binary path for the gemini_cli adapter.
	GeminiCLIBinaryEnvVar = "MCPLEXER_TEST_GEMINI_CLI_BIN"
	// PiCLIBinaryEnvVar pins the `pi` binary path for the pi_cli adapter.
	// Same opt-in posture as the other subprocess adapters.
	PiCLIBinaryEnvVar = "MCPLEXER_TEST_PI_CLI_BIN"
)

// resolveBinaryPath turns a bare binary name into an absolute path
// suitable for sandbox-exec (which ignores the parent's PATH).
//
// Resolution order (first match wins):
//  1. envVar — when non-empty AND set in the process env AND points at
//     a real executable file. Test-only override (see envVar consts).
//  2. fallbacks() — caller-supplied hardcoded install locations
//     (Homebrew, ~/.local/bin, ~/.claude/local, etc.).
//  3. exec.LookPath against the daemon's effective PATH.
//
// SECURITY (L3): step 2 runs BEFORE step 3 so a hostile binary in any
// writable PATH directory can't hijack the resolved path when a
// legitimate vendor install exists at a well-known location. The env
// override (step 1) is opt-in via an env var that production never
// sets, so it doesn't widen the hijack surface in normal operation.
// Returns the original name unchanged when every strategy fails — the
// eventual exec error then surfaces a clear "execvp() of X failed"
// string for the operator.
//
// Callers pass a closure rather than a fixed slice so per-provider
// fallback lists can lazy-build $HOME-relative entries (avoids
// initialising before os.UserHomeDir is callable).
func resolveBinaryPath(name, envVar string, fallbacks func() []string) string {
	if envVar != "" {
		if pinned := os.Getenv(envVar); pinned != "" {
			if info, err := os.Stat(pinned); err == nil && !info.IsDir() {
				return pinned
			}
		}
	}
	if fallbacks != nil {
		for _, p := range fallbacks() {
			if p == "" {
				continue
			}
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
	}
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved
	}
	return name
}
