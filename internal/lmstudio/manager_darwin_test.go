//go:build darwin

package lmstudio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLMSLaunchesInsideStrictDarwinSandbox(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MCPLEXER_AUTH_TOKEN", "must-not-leak")
	t.Setenv("SLACK_BOT_TOKEN", "must-not-leak")
	t.Setenv("DATABASE_URL", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("FUTURE_PROVIDER_API_KEY", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8443")
	caFile := filepath.Join(root, "private-ca.pem")
	t.Setenv("SSL_CERT_FILE", caFile)
	if err := os.WriteFile(filepath.Join(home, "unlisted-host-file"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, []byte("test-ca"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "lms")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
if cat "$HOME/unlisted-host-file" >/dev/null 2>&1; then
  exit 90
fi
if [ -n "${MCPLEXER_AUTH_TOKEN:-}" ] || [ -n "${SLACK_BOT_TOKEN:-}" ] || \
   [ -n "${DATABASE_URL:-}" ] || [ -n "${OPENAI_API_KEY:-}" ] || \
   [ -n "${FUTURE_PROVIDER_API_KEY:-}" ]; then
  exit 91
fi
test -n "${PATH:-}"
test -n "${HOME:-}"
test -n "${TMPDIR:-}"
test "${HTTPS_PROXY:-}" = http://proxy.example:8443
test "$(cat "$SSL_CERT_FILE")" = test-ca
printf scratch-ok > "$TMPDIR/lms-marker"
printf sandboxed-lms-ok
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	m := &Manager{enabled: true, lmsPath: bin}
	out, err := m.runLMS(context.Background(), 5*time.Second, "ls")
	if err != nil {
		t.Fatalf("runLMS: %v; output=%s", err, out)
	}
	if strings.TrimSpace(out) != "sandboxed-lms-ok" {
		t.Fatalf("output = %q, want sandboxed-lms-ok", out)
	}
}
