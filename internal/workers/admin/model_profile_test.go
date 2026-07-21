// model_profile_test.go — HTTP-level integration tests for the
// ModelProfileHandlers. Uses httptest + a real sqlite store so the tests
// cover the exact code paths the API hits in production.
package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// newModelProfileTestServer spins up a real sqlite store, seeds an
// AuthScope (needed for non-claude_cli providers), and mounts the five
// handler endpoints under /api/v1/model-profiles. Returns the server,
// the seeded scope id, and the underlying DB so tests can also assert
// directly against the store.
func newModelProfileTestServer(t *testing.T) (*httptest.Server, string, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "model_profiles.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	scope := &store.AuthScope{Name: "test-anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	h := &workersadmin.ModelProfileHandlers{Store: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/model-profiles", h.List)
	mux.HandleFunc("POST /api/v1/model-profiles", h.Create)
	mux.HandleFunc("GET /api/v1/model-profiles/{id}", h.Get)
	mux.HandleFunc("PUT /api/v1/model-profiles/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/model-profiles/{id}", h.Delete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, scope.ID, db
}

// doJSON issues a request, decodes the JSON body into result (when
// result != nil), and returns the status code.
func doJSON(
	t *testing.T, method, url string, body any, result any,
) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if result != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, result); err != nil {
			t.Fatalf("decode (status=%d, body=%s): %v", resp.StatusCode, raw, err)
		}
	}
	return resp.StatusCode
}

func TestModelProfileCreateGetListUpdateDelete(t *testing.T) {
	srv, scopeID, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"

	// --- Create ---
	createBody := map[string]any{
		"name":            "claude-shared",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com",
		"secret_scope_id": scopeID,
		"known_models":    []string{"claude-opus-4-7", "claude-sonnet-4-7"},
	}
	var created store.ModelProfile
	if code := doJSON(t, http.MethodPost, base, createBody, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d", code)
	}
	if created.ID == "" {
		t.Fatal("create: missing id")
	}
	if created.Builtin {
		t.Fatal("create: API must not be able to set Builtin=true")
	}
	if len(created.KnownModels) != 2 {
		t.Fatalf("create: KnownModels = %v", created.KnownModels)
	}

	// --- Get ---
	var got store.ModelProfile
	if code := doJSON(t, http.MethodGet, base+"/"+created.ID, nil, &got); code != http.StatusOK {
		t.Fatalf("get: status = %d", code)
	}
	if got.Name != "claude-shared" {
		t.Fatalf("get: name = %q", got.Name)
	}

	// --- List ---
	var list []store.ModelProfile
	if code := doJSON(t, http.MethodGet, base, nil, &list); code != http.StatusOK {
		t.Fatalf("list: status = %d", code)
	}
	if len(list) != 1 {
		t.Fatalf("list: len = %d, want 1", len(list))
	}

	// --- Update ---
	updateBody := map[string]any{
		"name":            "claude-shared-renamed",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com/v2",
		"secret_scope_id": scopeID,
		"known_models":    []string{"claude-opus-4-7"},
	}
	var updated store.ModelProfile
	if code := doJSON(t, http.MethodPut, base+"/"+created.ID, updateBody, &updated); code != http.StatusOK {
		t.Fatalf("update: status = %d", code)
	}
	if updated.Name != "claude-shared-renamed" {
		t.Fatalf("update: name = %q", updated.Name)
	}
	if len(updated.KnownModels) != 1 {
		t.Fatalf("update: KnownModels = %v", updated.KnownModels)
	}

	// --- Delete ---
	if code := doJSON(t, http.MethodDelete, base+"/"+created.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", code)
	}
	// --- Verify gone ---
	if code := doJSON(t, http.MethodGet, base+"/"+created.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("get-after-delete: status = %d", code)
	}
}

func TestModelProfileCreateValidation(t *testing.T) {
	srv, scopeID, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"

	tests := []struct {
		name     string
		body     map[string]any
		wantCode int
	}{
		{
			name: "missing name",
			body: map[string]any{
				"provider": "anthropic", "endpoint_url": "x", "secret_scope_id": scopeID,
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "name too long",
			body: map[string]any{
				"name":            strings.Repeat("a", 81),
				"provider":        "anthropic",
				"endpoint_url":    "x",
				"secret_scope_id": scopeID,
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "invalid provider",
			body: map[string]any{
				"name":         "bad-prov",
				"provider":     "openrouter",
				"endpoint_url": "x",
			},
			wantCode: http.StatusBadRequest,
		},
		{
			// Anthropic endpoint URL is baked into the adapter; profile
			// only needs a secret scope. Should succeed.
			name: "anthropic missing endpoint (allowed)",
			body: map[string]any{
				"name":            "no-endpoint",
				"provider":        "anthropic",
				"secret_scope_id": scopeID,
			},
			wantCode: http.StatusCreated,
		},
		{
			name: "openai_compat missing endpoint",
			body: map[string]any{
				"name":            "compat-no-endpoint",
				"provider":        "openai_compat",
				"secret_scope_id": scopeID,
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "openai missing secret",
			body: map[string]any{
				"name":         "oa-no-secret",
				"provider":     "openai",
				"endpoint_url": "https://api.openai.com/v1",
			},
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := doJSON(t, http.MethodPost, base, tt.body, nil); code != tt.wantCode {
				t.Fatalf("status = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestModelProfileClaudeCliAllowsEmptySecret(t *testing.T) {
	srv, _, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"
	// claude_cli profiles may omit endpoint_url AND secret_scope_id —
	// the host's claude binary handles OAuth login on its own.
	body := map[string]any{
		"name":     "local-claude",
		"provider": "claude_cli",
	}
	var created store.ModelProfile
	if code := doJSON(t, http.MethodPost, base, body, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d", code)
	}
	if created.Provider != "claude_cli" {
		t.Fatalf("provider = %q", created.Provider)
	}
	if created.SecretScopeID != "" {
		t.Fatalf("unexpected SecretScopeID: %q", created.SecretScopeID)
	}
}

func TestModelProfileGrokCliAllowsEmptySecret(t *testing.T) {
	srv, _, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"
	body := map[string]any{
		"name":     "local-grok",
		"provider": "grok_cli",
	}
	var created store.ModelProfile
	if code := doJSON(t, http.MethodPost, base, body, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d", code)
	}
	if created.Provider != "grok_cli" {
		t.Fatalf("provider = %q", created.Provider)
	}
	if created.SecretScopeID != "" {
		t.Fatalf("unexpected SecretScopeID: %q", created.SecretScopeID)
	}
}

func TestModelProfileMiMoCliAllowsEmptySecret(t *testing.T) {
	srv, _, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"
	body := map[string]any{
		"name":         "local-mimo",
		"provider":     "mimo_cli",
		"known_models": []string{"xiaomi/mimo-v2.5"},
	}
	var created store.ModelProfile
	if code := doJSON(t, http.MethodPost, base, body, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d", code)
	}
	if created.Provider != "mimo_cli" {
		t.Fatalf("provider = %q", created.Provider)
	}
	if created.SecretScopeID != "" {
		t.Fatalf("unexpected SecretScopeID: %q", created.SecretScopeID)
	}
}

func TestModelProfileDuplicateNameConflict(t *testing.T) {
	srv, scopeID, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"

	body := map[string]any{
		"name":            "dup-prof",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com",
		"secret_scope_id": scopeID,
	}
	if code := doJSON(t, http.MethodPost, base, body, nil); code != http.StatusCreated {
		t.Fatalf("first create: %d", code)
	}
	if code := doJSON(t, http.MethodPost, base, body, nil); code != http.StatusConflict {
		t.Fatalf("dup create: status = %d, want 409", code)
	}
}

func TestModelProfileBuiltinIsImmutable(t *testing.T) {
	srv, scopeID, db := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"

	// Seed a builtin row directly via the store (the daemon path; the
	// API can't ever set Builtin=true, per Create handler logic).
	ctx := context.Background()
	builtin := &store.ModelProfile{
		Name:          "builtin-anthropic",
		Provider:      "anthropic",
		EndpointURL:   "https://api.anthropic.com",
		SecretScopeID: scopeID,
		Builtin:       true,
	}
	if err := db.CreateModelProfile(ctx, builtin); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}

	// Update -> 403.
	updateBody := map[string]any{
		"name":            "renamed",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com",
		"secret_scope_id": scopeID,
	}
	if code := doJSON(t, http.MethodPut, base+"/"+builtin.ID, updateBody, nil); code != http.StatusForbidden {
		t.Fatalf("update builtin: status = %d, want 403", code)
	}
	// Delete -> 403.
	if code := doJSON(t, http.MethodDelete, base+"/"+builtin.ID, nil, nil); code != http.StatusForbidden {
		t.Fatalf("delete builtin: status = %d, want 403", code)
	}
}

func TestModelProfileGetNotFound(t *testing.T) {
	srv, _, _ := newModelProfileTestServer(t)
	if code := doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/model-profiles/does-not-exist", nil, nil); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestModelProfileCreateForcedNonBuiltin(t *testing.T) {
	srv, scopeID, _ := newModelProfileTestServer(t)
	base := srv.URL + "/api/v1/model-profiles"

	// Caller tries to set builtin=true — Create must strip it.
	body := map[string]any{
		"name":            "trying-builtin",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com",
		"secret_scope_id": scopeID,
		"builtin":         true,
	}
	var created store.ModelProfile
	if code := doJSON(t, http.MethodPost, base, body, &created); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if created.Builtin {
		t.Fatal("Builtin should be forced false by the API")
	}
}
