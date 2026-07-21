//go:build darwin

package models

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelCLIRunnersLaunchUnderStrictDarwinSandbox(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	binDir := filepath.Join(root, "bin")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{home, binDir, workspace, filepath.Join(home, ".mcplexer")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	t.Setenv("GH_TOKEN", "must-not-leak")
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(root, "agent.sock"))
	t.Setenv("MCPLEXER_AUTH_TOKEN", "must-not-leak")
	t.Setenv("SLACK_BOT_TOKEN", "must-not-leak")
	t.Setenv("TELEGRAM_BOT_TOKEN", "must-not-leak")
	t.Setenv("OPENWA_API_KEY", "must-not-leak")
	t.Setenv("DATABASE_URL", "must-not-leak")
	t.Setenv("POSTGRES_PASSWORD", "must-not-leak")
	t.Setenv("SENTRY_AUTH_TOKEN", "must-not-leak")
	t.Setenv("FUTURE_PROVIDER_API_KEY", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "provider-test-key")
	t.Setenv("ANTHROPIC_API_KEY", "provider-test-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "provider-test-key")
	t.Setenv("GEMINI_API_KEY", "provider-test-key")
	t.Setenv("GOOGLE_API_KEY", "provider-test-key")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "https://gemini.example")
	t.Setenv("XAI_API_KEY", "provider-test-key")
	t.Setenv("OPENROUTER_API_KEY", "provider-test-key")
	t.Setenv("MIMO_API_KEY", "provider-test-key")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8443")
	caFile := filepath.Join(root, "private-ca.pem")
	t.Setenv("SSL_CERT_FILE", caFile)
	if err := os.WriteFile(filepath.Join(home, ".mcplexer", "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, []byte("test-ca"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "fake-model-cli")
	script := `#!/bin/sh
set -eu
if cat "$HOME/.mcplexer/secret" >/dev/null 2>&1; then
  exit 90
fi
if [ -n "${AWS_SECRET_ACCESS_KEY:-}" ] || [ -n "${GH_TOKEN:-}" ] || [ -n "${SSH_AUTH_SOCK:-}" ] || \
   [ -n "${MCPLEXER_AUTH_TOKEN:-}" ] || [ -n "${SLACK_BOT_TOKEN:-}" ] || [ -n "${TELEGRAM_BOT_TOKEN:-}" ] || \
   [ -n "${OPENWA_API_KEY:-}" ] || [ -n "${DATABASE_URL:-}" ] || [ -n "${POSTGRES_PASSWORD:-}" ] || \
   [ -n "${SENTRY_AUTH_TOKEN:-}" ] || [ -n "${FUTURE_PROVIDER_API_KEY:-}" ]; then
  exit 91
fi
test "${HTTPS_PROXY:-}" = http://proxy.example:8443
test "$(cat "$SSL_CERT_FILE")" = test-ca
case "$1" in
  anthropic)
    test "${ANTHROPIC_API_KEY:-}" = provider-test-key
    test "${CLAUDE_CODE_OAUTH_TOKEN:-}" = provider-test-key
    test -z "${OPENAI_API_KEY:-}${GEMINI_API_KEY:-}${XAI_API_KEY:-}${OPENROUTER_API_KEY:-}"
    ;;
  openai)
    test "${OPENAI_API_KEY:-}" = provider-test-key
    test -z "${ANTHROPIC_API_KEY:-}${CLAUDE_CODE_OAUTH_TOKEN:-}${GEMINI_API_KEY:-}${XAI_API_KEY:-}${OPENROUTER_API_KEY:-}"
    ;;
  gemini)
    test "${GEMINI_API_KEY:-}" = provider-test-key
    test "${GOOGLE_API_KEY:-}" = provider-test-key
    test "${GOOGLE_GEMINI_BASE_URL:-}" = https://gemini.example
    test -z "${OPENAI_API_KEY:-}${ANTHROPIC_API_KEY:-}${CLAUDE_CODE_OAUTH_TOKEN:-}${XAI_API_KEY:-}${OPENROUTER_API_KEY:-}"
    ;;
  xai)
    test "${XAI_API_KEY:-}" = provider-test-key
    test -z "${OPENAI_API_KEY:-}${ANTHROPIC_API_KEY:-}${CLAUDE_CODE_OAUTH_TOKEN:-}${GEMINI_API_KEY:-}${OPENROUTER_API_KEY:-}"
    ;;
  multi)
    test "${OPENAI_API_KEY:-}" = provider-test-key
    test "${ANTHROPIC_API_KEY:-}" = provider-test-key
    test "${GEMINI_API_KEY:-}" = provider-test-key
    test "${XAI_API_KEY:-}" = provider-test-key
    test "${OPENROUTER_API_KEY:-}" = provider-test-key
    test "${MIMO_API_KEY:-}" = provider-test-key
    test -z "${CLAUDE_CODE_OAUTH_TOKEN:-}"
    ;;
  *)
    exit 92
    ;;
esac
printf workspace-ok > "$PWD/model-cli-marker"
printf scratch-ok > "$TMPDIR/model-cli-marker"
printf sandboxed-cli-ok
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	runners := []struct {
		name             string
		environmentClass string
		run              func(context.Context, string, []string, string, string) ([]byte, []byte, error)
	}{
		{"claude", "anthropic", claudeExecRunner},
		{"opencode", "multi", opencodeExecRunner},
		{"codex", "openai", codexExecRunner},
		{"gemini", "gemini", geminiExecRunner},
		{"grok", "xai", grokExecRunner},
		{"mimo", "multi", mimoExecRunner},
		{"pi", "multi", piExecRunner},
	}
	for _, tt := range runners {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := tt.run(
				context.Background(), bin, []string{tt.environmentClass}, "private prompt", workspace,
			)
			if err != nil {
				t.Fatalf("runner failed: %v; stderr=%s", err, stderr)
			}
			if strings.TrimSpace(string(stdout)) != "sandboxed-cli-ok" {
				t.Fatalf("stdout = %q, want sandboxed-cli-ok", stdout)
			}
			marker, err := os.ReadFile(filepath.Join(workspace, "model-cli-marker"))
			if err != nil {
				t.Fatalf("workspace marker: %v", err)
			}
			if string(marker) != "workspace-ok" {
				t.Fatalf("workspace marker = %q", marker)
			}
		})
	}
}
