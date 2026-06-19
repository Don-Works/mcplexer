package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateAPIToken_GeneratesNewToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "api-key")

	tok, err := LoadOrCreateAPIToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateAPIToken: %v", err)
	}
	if !validHexToken(tok) {
		t.Fatalf("generated token failed validation: %q", tok)
	}
	if len(tok) != APITokenBytes*2 {
		t.Errorf("token length = %d, want %d", len(tok), APITokenBytes*2)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != APITokenFileMode {
		t.Errorf("token file mode = %v, want %v", info.Mode().Perm(), APITokenFileMode)
	}

	// Idempotent: subsequent reads return the same token.
	again, err := LoadOrCreateAPIToken(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateAPIToken: %v", err)
	}
	if again != tok {
		t.Errorf("token changed across calls: %q -> %q", tok, again)
	}
}

func TestLoadOrCreateAPIToken_TightensPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	// Write a valid token with overly-loose perms.
	const validToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(validToken+"\n"), 0o644); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	tok, err := LoadOrCreateAPIToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateAPIToken: %v", err)
	}
	if tok != validToken {
		t.Errorf("token mismatch: %q vs %q", tok, validToken)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != APITokenFileMode {
		t.Errorf("perms not tightened: got %v want %v", info.Mode().Perm(), APITokenFileMode)
	}
}

func TestLoadOrCreateAPIToken_RejectsGarbageFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	if err := os.WriteFile(path, []byte("not a hex token!"), 0o600); err != nil {
		t.Fatalf("seed bad token: %v", err)
	}

	_, err := LoadOrCreateAPIToken(path)
	if err == nil {
		t.Fatal("expected error for invalid token file, got nil")
	}
	if !strings.Contains(err.Error(), "valid token") {
		t.Errorf("error should mention validation, got: %v", err)
	}
}

func TestLoadOrCreateAPIToken_EmptyPath(t *testing.T) {
	_, err := LoadOrCreateAPIToken("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoadOrCreateAPIToken_TokensAreDistinctAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateAPIToken(filepath.Join(dir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateAPIToken(filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two fresh tokens should differ; if this fails check rand source")
	}
}

func TestDefaultAPITokenPath(t *testing.T) {
	if got := DefaultAPITokenPath(""); got != "" {
		t.Errorf("DefaultAPITokenPath(\"\") = %q, want \"\"", got)
	}
	if got := DefaultAPITokenPath("/home/x"); got != "/home/x/.mcplexer/api-key" {
		t.Errorf("DefaultAPITokenPath(/home/x) = %q", got)
	}
}

func TestValidHexToken(t *testing.T) {
	cases := []struct {
		s     string
		valid bool
	}{
		{"", false},
		{"abc", false}, // too short
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},  // 64 hex chars
		{"0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", true},  // upper case ok
		{"0123456789abcdefxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", false}, // non-hex
		{"00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", false}, // too long
	}
	for _, c := range cases {
		got := validHexToken(c.s)
		if got != c.valid {
			t.Errorf("validHexToken(%q) = %v, want %v", c.s, got, c.valid)
		}
	}
}
