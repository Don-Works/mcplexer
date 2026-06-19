package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestEncryptor(t *testing.T) *secrets.AgeEncryptor {
	t.Helper()
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("new ephemeral encryptor: %v", err)
	}
	return enc
}

// seedAuthScope creates an auth scope and returns it.
func seedAuthScope(t *testing.T, db *sqlite.DB, name string) *store.AuthScope {
	t.Helper()
	scope := &store.AuthScope{Name: name, Type: "oauth2"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("seed auth scope: %v", err)
	}
	return scope
}

func TestHandleCreateOAuthProvider_Success(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)

	args := json.RawMessage(`{
		"name": "freeagent",
		"authorize_url": "https://example.com/auth",
		"token_url": "https://example.com/token",
		"client_id": "cid-123",
		"scopes": ["read", "write"],
		"use_pkce": true
	}`)

	result, err := handleCreateOAuthProvider(enc)(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}

	var got createOAuthProviderResult
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Fatal("expected provider ID")
	}
	if got.Name != "freeagent" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.HasClientSecret {
		t.Fatal("has_client_secret should be false when none supplied")
	}
	if got.LinkedScopeID != "" {
		t.Fatalf("linked_scope_id should be empty, got %q", got.LinkedScopeID)
	}

	// Verify persisted fields.
	stored, err := db.GetOAuthProvider(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ClientID != "cid-123" || !stored.UsePKCE {
		t.Fatalf("persisted fields wrong: client_id=%q use_pkce=%v", stored.ClientID, stored.UsePKCE)
	}
	var scopes []string
	if err := json.Unmarshal(stored.Scopes, &scopes); err != nil {
		t.Fatalf("unmarshal scopes: %v", err)
	}
	if len(scopes) != 2 || scopes[0] != "read" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestHandleCreateOAuthProvider_SecretEncryptedNeverSurfaced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)

	const secret = "super-secret-value"
	args, _ := json.Marshal(map[string]any{
		"name":          "with-secret",
		"client_id":     "cid",
		"client_secret": secret,
	})

	result, err := handleCreateOAuthProvider(enc)(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}

	// The plaintext secret must never appear in the response payload.
	if strings.Contains(text, secret) {
		t.Fatalf("response leaked plaintext client secret: %s", text)
	}

	var got createOAuthProviderResult
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if !got.HasClientSecret {
		t.Fatal("has_client_secret should be true")
	}

	// Verify the secret is encrypted at rest (not the raw plaintext) and
	// decrypts back to the original.
	stored, err := db.GetOAuthProvider(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.EncryptedClientSecret) == 0 {
		t.Fatal("expected encrypted client secret in store")
	}
	if string(stored.EncryptedClientSecret) == secret {
		t.Fatal("client secret stored as plaintext")
	}
	dec, err := enc.Decrypt(stored.EncryptedClientSecret)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(dec) != secret {
		t.Fatalf("decrypted = %q, want %q", string(dec), secret)
	}
}

func TestHandleCreateOAuthProvider_MissingName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)

	_, err := handleCreateOAuthProvider(enc)(ctx, db, json.RawMessage(`{"client_id":"x"}`))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleCreateOAuthProvider_NilEncryptorWithSecret(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{"name":"n","client_secret":"s"}`)
	_, err := handleCreateOAuthProvider(nil)(ctx, db, args)
	if err == nil {
		t.Fatal("expected error when encryptor nil and client_secret supplied")
	}
	if !strings.Contains(err.Error(), "encryption not configured") {
		t.Fatalf("error = %v", err)
	}

	// No secret + nil encryptor must still succeed (provider with no secret).
	result, err := handleCreateOAuthProvider(nil)(ctx, db, json.RawMessage(`{"name":"no-secret"}`))
	if err != nil {
		t.Fatalf("unexpected error for no-secret provider: %v", err)
	}
	_, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result for no-secret provider")
	}
}

func TestHandleCreateOAuthProvider_LinkScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)
	scope := seedAuthScope(t, db, "freeagent-scope")

	args, _ := json.Marshal(map[string]any{
		"name":          "freeagent",
		"client_id":     "cid",
		"link_scope_id": scope.ID,
	})

	result, err := handleCreateOAuthProvider(enc)(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}

	var got createOAuthProviderResult
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.LinkedScopeID != scope.ID {
		t.Fatalf("linked_scope_id = %q, want %q", got.LinkedScopeID, scope.ID)
	}

	// Partial update: oauth_provider_id set, name/type untouched.
	updated, err := db.GetAuthScope(ctx, scope.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.OAuthProviderID != got.ID {
		t.Fatalf("scope oauth_provider_id = %q, want %q", updated.OAuthProviderID, got.ID)
	}
	if updated.Name != "freeagent-scope" || updated.Type != "oauth2" {
		t.Fatalf("link clobbered other scope fields: name=%q type=%q", updated.Name, updated.Type)
	}
}

func TestHandleCreateOAuthProvider_LinkUnknownScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)

	args := json.RawMessage(`{"name":"p","link_scope_id":"does-not-exist"}`)
	_, err := handleCreateOAuthProvider(enc)(ctx, db, args)
	if err == nil {
		t.Fatal("expected error linking unknown scope")
	}
}

func TestInternalBackend_CreateOAuthProvider(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	enc := newTestEncryptor(t)

	b := NewInternalBackend(db, nil)
	b.SetEncryptor(enc)

	args := json.RawMessage(`{"name":"via-backend","client_secret":"x"}`)
	result, err := b.Call(ctx, "create_oauth_provider", args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if strings.Contains(text, `"x"`) {
		t.Fatalf("response leaked client secret: %s", text)
	}
	var got createOAuthProviderResult
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if !got.HasClientSecret {
		t.Fatal("has_client_secret should be true")
	}
}
