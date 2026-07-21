package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestAgeKey generates an ephemeral age identity in dir and returns the
// key-file path + its public recipient. SOPS_AGE_KEY_FILE is pointed at it
// for the duration of the test.
func newTestAgeKey(t *testing.T, dir string) (keyFile string, recipient string) {
	t.Helper()
	keyFile = filepath.Join(dir, "secrets", "age", "keys.txt")
	recipients, err := EnsureAgeKeyFile(keyFile)
	if err != nil {
		t.Fatalf("EnsureAgeKeyFile: %v", err)
	}
	if len(recipients) != 1 {
		t.Fatalf("expected 1 recipient, got %d", len(recipients))
	}
	t.Setenv(EnvAgeKeyFile, keyFile)
	return keyFile, recipients[0]
}

func TestWriteEncryptedScopes_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	keyFile, recipient := newTestAgeKey(t, tmp)

	scopes := map[string]ScopeSecrets{
		"stripe-prod": {
			Type:           "env",
			RedactionHints: []string{"STRIPE_*"},
			Values: map[string]string{
				"STRIPE_API_KEY":     "stripe_secret_abc123",
				"STRIPE_WEBHOOK_SEC": "whsec_xyz",
			},
		},
		"github-pat": {
			Type:   "env",
			Values: map[string]string{"GH_TOKEN": "github_secret_deadbeef"},
		},
	}

	if err := WriteEncryptedScopes(tmp, []string{recipient}, scopes); err != nil {
		t.Fatalf("WriteEncryptedScopes: %v", err)
	}

	src := NewSOPSSource(tmp, keyFile)
	for scope, sec := range scopes {
		for k, want := range sec.Values {
			got, ok, err := src.Get(context.Background(), scope, k)
			if err != nil {
				t.Fatalf("Get(%s,%s): %v", scope, k, err)
			}
			if !ok {
				t.Fatalf("Get(%s,%s): not found", scope, k)
			}
			if string(got) != want {
				t.Fatalf("Get(%s,%s)=%q want %q", scope, k, got, want)
			}
		}
	}

	// Missing scope + missing key both report not-found (no error).
	if _, ok, err := src.Get(context.Background(), "nope", "X"); err != nil || ok {
		t.Fatalf("missing scope: ok=%v err=%v", ok, err)
	}
	if _, ok, err := src.Get(context.Background(), "github-pat", "MISSING"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
}

func TestValueOnlyEncryption(t *testing.T) {
	tmp := t.TempDir()
	_, recipient := newTestAgeKey(t, tmp)

	scopes := map[string]ScopeSecrets{
		"stripe-prod": {
			Type:           "env",
			RedactionHints: []string{"STRIPE_*"},
			Values:         map[string]string{"STRIPE_API_KEY": "stripe_secret_SENSITIVE"},
		},
	}
	if err := WriteEncryptedScopes(tmp, []string{recipient}, scopes); err != nil {
		t.Fatalf("WriteEncryptedScopes: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, scopesFileRelPath))
	if err != nil {
		t.Fatalf("read encrypted file: %v", err)
	}
	content := string(raw)

	// Scope name + type + redaction hints stay plaintext (reviewable).
	for _, want := range []string{"stripe-prod", "env", "STRIPE_*", "STRIPE_API_KEY"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected plaintext %q in file, not found", want)
		}
	}
	// The secret VALUE must be encrypted, never plaintext.
	if strings.Contains(content, "stripe_secret_SENSITIVE") {
		t.Errorf("plaintext secret value leaked into encrypted file")
	}
	if !strings.Contains(content, "ENC[") {
		t.Errorf("expected SOPS ENC[...] marker, not found")
	}
}

func TestSOPSSource_CacheReloadOnMtime(t *testing.T) {
	tmp := t.TempDir()
	keyFile, recipient := newTestAgeKey(t, tmp)

	write := func(val string) {
		scopes := map[string]ScopeSecrets{
			"s": {Type: "env", Values: map[string]string{"K": val}},
		}
		if err := WriteEncryptedScopes(tmp, []string{recipient}, scopes); err != nil {
			t.Fatalf("WriteEncryptedScopes: %v", err)
		}
	}

	write("v1")
	src := NewSOPSSource(tmp, keyFile)
	got, _, _ := src.Get(context.Background(), "s", "K")
	if string(got) != "v1" {
		t.Fatalf("first read=%q want v1", got)
	}

	// Rewrite with a new value; bump mtime explicitly to be robust on
	// coarse-grained filesystems.
	write("v2")
	future := filepath.Join(tmp, scopesFileRelPath)
	bump := mustStat(t, future).ModTime().Add(2_000_000_000) // +2s
	if err := os.Chtimes(future, bump, bump); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got2, _, _ := src.Get(context.Background(), "s", "K")
	if string(got2) != "v2" {
		t.Fatalf("after rewrite read=%q want v2 (cache did not reload on mtime change)", got2)
	}
}

func TestSOPSSource_MissingFileEmpty(t *testing.T) {
	tmp := t.TempDir()
	keyFile, _ := newTestAgeKey(t, tmp)

	// No scopes.enc.yaml written: Get reports not-found, no error (clean
	// dual-read fallback to the age-DB blob).
	src := NewSOPSSource(tmp, keyFile)
	if _, ok, err := src.Get(context.Background(), "any", "K"); err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v (want false,nil)", ok, err)
	}
}

func TestWriteEncryptedScopes_NoRecipients(t *testing.T) {
	tmp := t.TempDir()
	if err := WriteEncryptedScopes(tmp, nil, map[string]ScopeSecrets{}); err == nil {
		t.Fatalf("expected error with no recipients")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi
}
