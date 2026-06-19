package brain

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeDecryptor treats the "encrypted" bytes as a length-prefixed plaintext
// wrapper so the test controls the round-trip without real age. It mirrors
// the contract: Decrypt(blob) returns the plaintext the migrator unmarshals.
type fakeDecryptor struct {
	failOn string // when non-empty, Decrypt errors if plaintext contains this
}

func (f fakeDecryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	// Our fake "encryption" is identity (the stub store puts plaintext in
	// EncryptedData). A failure trigger lets us exercise the error path.
	if f.failOn != "" && string(ciphertext) != "" && containsBytes(ciphertext, f.failOn) {
		return nil, errors.New("fake decrypt failure")
	}
	return ciphertext, nil
}

func containsBytes(b []byte, s string) bool {
	return len(s) > 0 && len(b) >= len(s) && (string(b) == s || indexOf(string(b), s) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// fakeSecretSource satisfies the secretSource interface with in-memory rows.
type fakeSecretSource struct {
	scopes    []store.AuthScope
	providers []store.OAuthProvider
}

func (f fakeSecretSource) ListAuthScopes(ctx context.Context) ([]store.AuthScope, error) {
	return f.scopes, nil
}

func (f fakeSecretSource) ListOAuthProviders(ctx context.Context) ([]store.OAuthProvider, error) {
	return f.providers, nil
}

// blobOf JSON-encodes a key/value map as the "EncryptedData" the fake
// decryptor passes through unchanged.
func blobOf(t *testing.T, kv map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(kv)
	if err != nil {
		t.Fatalf("marshal blob: %v", err)
	}
	return b
}

func TestMigrateSecrets_RoundTripVerified(t *testing.T) {
	tmp := t.TempDir()
	keyFile, recipient := newTestAgeKey(t, tmp)

	src := fakeSecretSource{
		scopes: []store.AuthScope{
			{
				Name:           "stripe-prod",
				Type:           "env",
				RedactionHints: json.RawMessage(`["STRIPE_*"]`),
				EncryptedData:  blobOf(t, map[string]string{"STRIPE_API_KEY": "stripe_secret_1"}),
			},
			{
				Name:          "gh",
				Type:          "env",
				EncryptedData: blobOf(t, map[string]string{"GH_TOKEN": "github_secret_1"}),
			},
			// A scope with only oauth token data (empty values) is skipped.
			{Name: "empty", Type: "env", EncryptedData: nil},
		},
		providers: []store.OAuthProvider{
			{Name: "google", EncryptedClientSecret: []byte("client-secret-google")},
			{Name: "no-secret", EncryptedClientSecret: nil},
		},
	}

	rep, err := MigrateSecrets(context.Background(), tmp, []string{recipient}, src, fakeDecryptor{}, keyFile)
	if err != nil {
		t.Fatalf("MigrateSecrets: %v", err)
	}
	if !rep.RoundTripOK {
		t.Fatalf("expected round-trip OK")
	}
	if rep.Scopes != 2 {
		t.Errorf("Scopes=%d want 2", rep.Scopes)
	}
	if rep.Providers != 1 {
		t.Errorf("Providers=%d want 1", rep.Providers)
	}
	if rep.Values != 3 { // STRIPE_API_KEY, GH_TOKEN, OAUTH_CLIENT_SECRET
		t.Errorf("Values=%d want 3", rep.Values)
	}
	if !rep.WroteSopsRules {
		t.Errorf("expected .sops.yaml written")
	}

	// Verify the written file decrypts back to the expected values.
	loaded := NewSOPSSource(tmp, keyFile)
	got, ok, err := loaded.Get(context.Background(), "stripe-prod", "STRIPE_API_KEY")
	if err != nil || !ok || string(got) != "stripe_secret_1" {
		t.Fatalf("stripe value: got=%q ok=%v err=%v", got, ok, err)
	}
	got, ok, err = loaded.Get(context.Background(), "oauth:google", oauthClientSecretKey)
	if err != nil || !ok || string(got) != "client-secret-google" {
		t.Fatalf("oauth client secret: got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestMigrateSecrets_NoRecipients(t *testing.T) {
	tmp := t.TempDir()
	src := fakeSecretSource{}
	if _, err := MigrateSecrets(context.Background(), tmp, nil, src, fakeDecryptor{}, ""); err == nil {
		t.Fatalf("expected error with no recipients")
	}
}

func TestMigrateSecrets_DecryptError(t *testing.T) {
	tmp := t.TempDir()
	keyFile, recipient := newTestAgeKey(t, tmp)
	src := fakeSecretSource{
		scopes: []store.AuthScope{
			{Name: "s", Type: "env", EncryptedData: blobOf(t, map[string]string{"K": "POISON"})},
		},
	}
	// Decryptor fails when it sees POISON → MigrateSecrets surfaces the error.
	if _, err := MigrateSecrets(context.Background(), tmp, []string{recipient}, src, fakeDecryptor{failOn: "POISON"}, keyFile); err == nil {
		t.Fatalf("expected decrypt error to propagate")
	}
}

func TestScopeMapsEqual(t *testing.T) {
	a := map[string]ScopeSecrets{"x": {Type: "env", RedactionHints: []string{"A", "B"}, Values: map[string]string{"K": "v"}}}
	b := map[string]ScopeSecrets{"x": {Type: "env", RedactionHints: []string{"B", "A"}, Values: map[string]string{"K": "v"}}}
	if !scopeMapsEqual(a, b) {
		t.Errorf("expected equal (hint order normalised)")
	}
	c := map[string]ScopeSecrets{"x": {Type: "env", Values: map[string]string{"K": "DIFFERENT"}}}
	if scopeMapsEqual(a, c) {
		t.Errorf("expected unequal (value differs)")
	}
}
