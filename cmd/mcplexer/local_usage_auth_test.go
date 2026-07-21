package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

type recordingSecretReader struct {
	value []byte
	err   error
	scope string
	key   string
	calls int
}

func (r *recordingSecretReader) Get(_ context.Context, scopeID, key string) ([]byte, error) {
	r.calls++
	r.scope, r.key = scopeID, key
	return r.value, r.err
}

func TestLocalUsageAuthReaderReadsOpenCodeProviders(t *testing.T) {
	home := t.TempDir()
	writeAuthFile(t, home, "opencode", `{
		"minimax": {"key": "mm-secret-token"},
		"zai-coding-plan": {"key": "zai-secret-token"},
		"openrouter": {"key": "or-secret-token"}
	}`)
	reader := &localUsageAuthReader{
		secrets: &recordingSecretReader{value: []byte("db-secret")},
		homeDir: func() (string, error) { return home, nil },
	}
	cases := []struct {
		key  string
		want string
	}{
		{store.LocalAuthKeyMiniMax, "mm-secret-token"},
		{store.LocalAuthKeyZAI, "zai-secret-token"},
		{store.LocalAuthKeyOpenRouter, "or-secret-token"},
	}
	for _, tc := range cases {
		got, err := reader.Get(context.Background(), store.LocalAuthScopeOpenCode, tc.key)
		if err != nil || string(got) != tc.want {
			t.Fatalf("Get(%s) = %q err=%v", tc.key, string(got), err)
		}
	}
}

func TestLocalUsageAuthReaderReadsMiMoProvider(t *testing.T) {
	home := t.TempDir()
	writeAuthFile(t, home, "mimocode", `{
		"xiaomi": {"key": "mimo-secret-token", "metadata": {"base_url": "https://api.example"}}
	}`)
	reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}
	got, err := reader.Get(context.Background(), store.LocalAuthScopeMiMo, store.LocalAuthKeyMiMoXiaomi)
	if err != nil || string(got) != "mimo-secret-token" {
		t.Fatalf("Get() = %q err=%v", string(got), err)
	}
}

func TestLocalUsageAuthReaderFallsBackToEncryptedStore(t *testing.T) {
	secrets := &recordingSecretReader{value: []byte("encrypted-key")}
	reader := &localUsageAuthReader{secrets: secrets}
	got, err := reader.Get(context.Background(), "scope-1", "api_key")
	if err != nil || string(got) != "encrypted-key" {
		t.Fatalf("Get() = %q err=%v", string(got), err)
	}
	if secrets.scope != "scope-1" || secrets.key != "api_key" || secrets.calls != 1 {
		t.Fatalf("secrets = scope:%q key:%q calls:%d", secrets.scope, secrets.key, secrets.calls)
	}
}

func TestLocalUsageAuthReaderRejectsUnsafeFiles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}
		base := filepath.Join(home, ".local", "share", "opencode")
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		authPath := filepath.Join(base, "auth.json")
		if err := os.Symlink(filepath.Join(base, "real-auth.json"), authPath); err != nil {
			t.Fatal(err)
		}
		_, err := readLocalCLIAuth(
			reader.homeDir, store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax,
		)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		home := t.TempDir()
		reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}
		writeAuthFile(t, home, "opencode", strings.Repeat("a", localAuthMaxFileBytes+1))
		_, err := readLocalCLIAuth(
			reader.homeDir, store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax,
		)
		if err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversize error = %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		home := t.TempDir()
		reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}
		_, err := readLocalCLIAuth(
			reader.homeDir, store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax,
		)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing error = %v", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		home := t.TempDir()
		reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}
		writeAuthFile(t, home, "opencode", `{"minimax":{"key":`)
		_, err := readLocalCLIAuth(
			reader.homeDir, store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax,
		)
		if err == nil {
			t.Fatal("expected malformed decode error")
		}
	})
}

func TestLocalUsageAuthReaderNeverLeaksSecretsInErrors(t *testing.T) {
	home := t.TempDir()
	secret := "super-secret-api-key-value"
	writeAuthFile(t, home, "opencode", `{"minimax":{"key":"`+secret+`"}}`)
	reader := &localUsageAuthReader{homeDir: func() (string, error) { return home, nil }}

	_, err := reader.Get(context.Background(), store.LocalAuthScopeOpenCode, "unknown-provider")
	if err == nil {
		t.Fatal("expected unsupported reference error")
	}
	assertNoSecretLeak(t, err.Error(), secret)

	missing := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	_, err = reader.Get(context.Background(), store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax)
	if err == nil {
		t.Fatal("expected missing file error")
	}
	assertNoSecretLeak(t, err.Error(), secret)
}

func writeAuthFile(t *testing.T, home, product, body string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", product)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoSecretLeak(t *testing.T, message, secret string) {
	t.Helper()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %q", message)
	}
	if strings.Contains(message, filepath.Join(".local", "share")) {
		t.Fatalf("error leaked auth path: %q", message)
	}
}
