package models

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveBinaryPath_EnvWinsOverFallback locks in the resolver's
// load-bearing contract for the integration harness: when the test
// env (MCPLEXER_TEST_CLAUDE_CLI_BIN / _OPENCODE_CLI_BIN) is set and
// points at a real file, that path WINS over both the fallback list
// AND exec.LookPath. The docker-compose harness relies on this to
// pin a deterministic stub binary without poisoning $PATH.
//
// Negative branches:
//   - env unset → fallback list wins.
//   - env set to a non-existent path → falls through to the fallback
//     list (a typo must not silently disable dispatch).
//   - env set to a directory → ignored (Stat says IsDir(); same fall
//     through).
//   - no env, no fallback hit → exec.LookPath; absent that, the bare
//     name comes back unchanged.
func TestResolveBinaryPath_EnvWinsOverFallback(t *testing.T) {
	envVar := "MCPLEXER_TEST_RESOLVE_FAKE_BIN"
	dir := t.TempDir()

	envBinary := filepath.Join(dir, "env-binary")
	if err := os.WriteFile(envBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write env binary: %v", err)
	}
	fallbackBinary := filepath.Join(dir, "fallback-binary")
	if err := os.WriteFile(fallbackBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fallback binary: %v", err)
	}
	fallbacks := func() []string { return []string{fallbackBinary} }

	t.Run("env_set_to_real_file_wins", func(t *testing.T) {
		t.Setenv(envVar, envBinary)
		got := resolveBinaryPath("ignored-name", envVar, fallbacks)
		if got != envBinary {
			t.Fatalf("env-pinned: want %s, got %s", envBinary, got)
		}
	})

	t.Run("env_unset_uses_fallback", func(t *testing.T) {
		// t.Setenv("") would still set; use OS-level unset.
		_ = os.Unsetenv(envVar)
		got := resolveBinaryPath("ignored-name", envVar, fallbacks)
		if got != fallbackBinary {
			t.Fatalf("env-unset: want fallback %s, got %s", fallbackBinary, got)
		}
	})

	t.Run("env_points_at_missing_path_falls_through", func(t *testing.T) {
		t.Setenv(envVar, filepath.Join(dir, "does-not-exist"))
		got := resolveBinaryPath("ignored-name", envVar, fallbacks)
		if got != fallbackBinary {
			t.Fatalf("bad-env: want fallback %s, got %s", fallbackBinary, got)
		}
	})

	t.Run("env_points_at_directory_falls_through", func(t *testing.T) {
		t.Setenv(envVar, dir)
		got := resolveBinaryPath("ignored-name", envVar, fallbacks)
		if got != fallbackBinary {
			t.Fatalf("env-is-dir: want fallback %s, got %s", fallbackBinary, got)
		}
	})

	t.Run("empty_envVar_arg_skips_check", func(t *testing.T) {
		// Pass envVar="" — the resolver should ignore the env path entirely
		// even when a real env is set. Guards future callers that wire
		// resolveBinaryPath without an env override.
		t.Setenv(envVar, envBinary)
		got := resolveBinaryPath("ignored-name", "", fallbacks)
		if got != fallbackBinary {
			t.Fatalf("empty-envVar: want fallback %s, got %s", fallbackBinary, got)
		}
	})
}
