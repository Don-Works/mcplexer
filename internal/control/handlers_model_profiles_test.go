package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// newModelProfileBackend wires an InternalBackend with the worker admin
// service (which carries the ModelProfileStore) plugged in, and seeds an
// AuthScope so non-CLI providers have a secret to reference. Returns
// (backend, db, scopeID).
func newModelProfileBackend(t *testing.T) (*InternalBackend, *sqlite.DB, string) {
	t.Helper()
	db := newTestDB(t)
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("seed auth scope: %v", err)
	}
	b := NewInternalBackend(db, nil)
	b.SetWorkerAdmin(admin.New(db, admin.Options{Workspaces: db}))
	return b, db, scope.ID
}

// callMPTool drives one InternalBackend.Call and returns (text, isError).
func callMPTool(t *testing.T, b *InternalBackend, name string, args any) (string, bool) {
	t.Helper()
	var raw json.RawMessage
	if args != nil {
		var err error
		raw, err = json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
	}
	out, err := b.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("backend.Call(%q): %v", name, err)
	}
	return parseToolResult(t, out)
}

// decodeProfile unmarshals a non-error tool result into a ModelProfile.
func decodeProfile(t *testing.T, text string) store.ModelProfile {
	t.Helper()
	var p store.ModelProfile
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("decode profile (%s): %v", text, err)
	}
	return p
}

func TestModelProfileToolsListedInToolsList(t *testing.T) {
	b := NewInternalBackend(newTestDB(t), nil)
	raw, err := b.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range listing.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"list_model_profiles", "get_model_profile", "create_model_profile",
		"update_model_profile", "set_model_profile_known_models",
		"delete_model_profile",
	} {
		if !got[want] {
			t.Errorf("ListTools missing %q", want)
		}
	}
}

// TestModelProfileToolsCreateGetListUpdateDelete exercises the full CRUD
// path through the MCP dispatch, including a KnownModels update.
func TestModelProfileToolsCreateGetListUpdateDelete(t *testing.T) {
	b, _, scopeID := newModelProfileBackend(t)

	// --- Create ---
	text, isErr := callMPTool(t, b, "create_model_profile", map[string]any{
		"name":            "claude-shared",
		"provider":        "anthropic",
		"endpoint_url":    "https://api.anthropic.com",
		"secret_scope_id": scopeID,
		"known_models":    []string{"claude-opus-4-7", "claude-sonnet-4-7"},
	})
	if isErr {
		t.Fatalf("create: unexpected error: %s", text)
	}
	created := decodeProfile(t, text)
	if created.ID == "" {
		t.Fatal("create: missing id")
	}
	if created.Builtin {
		t.Fatal("create: MCP must not be able to set Builtin=true")
	}
	if len(created.KnownModels) != 2 {
		t.Fatalf("create: KnownModels = %v", created.KnownModels)
	}

	// --- Get ---
	text, isErr = callMPTool(t, b, "get_model_profile", map[string]any{"id": created.ID})
	if isErr {
		t.Fatalf("get: unexpected error: %s", text)
	}
	if got := decodeProfile(t, text); got.Name != "claude-shared" {
		t.Fatalf("get: name = %q", got.Name)
	}

	// --- List ---
	text, isErr = callMPTool(t, b, "list_model_profiles", nil)
	if isErr {
		t.Fatalf("list: unexpected error: %s", text)
	}
	var list []store.ModelProfile
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list: len = %d, want 1", len(list))
	}

	// --- Update (partial: rename only; provider/endpoint/secret untouched,
	//     KnownModels untouched). This proves omit = unchanged. ---
	text, isErr = callMPTool(t, b, "update_model_profile", map[string]any{
		"id":   created.ID,
		"name": "claude-shared-renamed",
	})
	if isErr {
		t.Fatalf("update: unexpected error: %s", text)
	}
	updated := decodeProfile(t, text)
	if updated.Name != "claude-shared-renamed" {
		t.Fatalf("update: name = %q", updated.Name)
	}
	if updated.Provider != "anthropic" {
		t.Fatalf("update: provider clobbered = %q", updated.Provider)
	}
	if updated.SecretScopeID != scopeID {
		t.Fatalf("update: secret_scope_id clobbered = %q", updated.SecretScopeID)
	}
	if len(updated.KnownModels) != 2 {
		t.Fatalf("update: KnownModels clobbered = %v (omit should leave unchanged)", updated.KnownModels)
	}

	// --- Update KnownModels via the full update tool ---
	text, isErr = callMPTool(t, b, "update_model_profile", map[string]any{
		"id":           created.ID,
		"known_models": []string{"claude-opus-4-7"},
	})
	if isErr {
		t.Fatalf("update known_models: unexpected error: %s", text)
	}
	if got := decodeProfile(t, text); len(got.KnownModels) != 1 {
		t.Fatalf("update known_models: %v", got.KnownModels)
	}

	// --- Delete ---
	text, isErr = callMPTool(t, b, "delete_model_profile", map[string]any{"id": created.ID})
	if isErr {
		t.Fatalf("delete: unexpected error: %s", text)
	}
	if !strings.Contains(text, "\"deleted\": true") {
		t.Fatalf("delete: unexpected body: %s", text)
	}

	// --- Get-after-delete -> error ---
	text, isErr = callMPTool(t, b, "get_model_profile", map[string]any{"id": created.ID})
	if !isErr {
		t.Fatalf("get-after-delete: expected error, got %s", text)
	}
	if !strings.Contains(text, "not found") {
		t.Fatalf("get-after-delete: want 'not found', got %s", text)
	}
}

// TestModelProfileSetKnownModelsTool covers the curation shorthand.
func TestModelProfileSetKnownModelsTool(t *testing.T) {
	b, _, scopeID := newModelProfileBackend(t)

	text, isErr := callMPTool(t, b, "create_model_profile", map[string]any{
		"name":            "pool",
		"provider":        "openai",
		"secret_scope_id": scopeID,
		"known_models":    []string{"gpt-old"},
	})
	if isErr {
		t.Fatalf("create: %s", text)
	}
	created := decodeProfile(t, text)

	// Replace the whole list in one call.
	text, isErr = callMPTool(t, b, "set_model_profile_known_models", map[string]any{
		"id":           created.ID,
		"known_models": []string{"gpt-5.5", "gpt-5.5-mini", "o4"},
	})
	if isErr {
		t.Fatalf("set known_models: %s", text)
	}
	got := decodeProfile(t, text)
	if len(got.KnownModels) != 3 {
		t.Fatalf("set known_models: %v", got.KnownModels)
	}
	// Other fields must be intact.
	if got.Provider != "openai" || got.SecretScopeID != scopeID {
		t.Fatalf("set known_models clobbered other fields: %+v", got)
	}

	// Empty list clears the pool (curation can empty it).
	text, isErr = callMPTool(t, b, "set_model_profile_known_models", map[string]any{
		"id":           created.ID,
		"known_models": []string{},
	})
	if isErr {
		t.Fatalf("clear known_models: %s", text)
	}
	if got := decodeProfile(t, text); len(got.KnownModels) != 0 {
		t.Fatalf("clear known_models: %v", got.KnownModels)
	}
}

// TestModelProfileBuiltinImmutableViaMCP is the load-bearing guard: a
// daemon-seeded Builtin row must refuse update / set_known_models /
// delete through the MCP surface, exactly as it does over REST.
func TestModelProfileBuiltinImmutableViaMCP(t *testing.T) {
	b, db, scopeID := newModelProfileBackend(t)

	// Seed a builtin row directly via the store (the daemon path; neither
	// the API nor MCP can ever set Builtin=true).
	builtin := &store.ModelProfile{
		Name:          "builtin-anthropic",
		Provider:      "anthropic",
		EndpointURL:   "https://api.anthropic.com",
		SecretScopeID: scopeID,
		Builtin:       true,
	}
	if err := db.CreateModelProfile(context.Background(), builtin); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}

	// update -> error.
	text, isErr := callMPTool(t, b, "update_model_profile", map[string]any{
		"id":   builtin.ID,
		"name": "renamed",
	})
	if !isErr {
		t.Fatalf("update builtin: expected error, got %s", text)
	}
	if !strings.Contains(text, "builtin") {
		t.Fatalf("update builtin: want 'builtin' in error, got %s", text)
	}

	// set_model_profile_known_models -> error (inherits the Builtin guard).
	text, isErr = callMPTool(t, b, "set_model_profile_known_models", map[string]any{
		"id":           builtin.ID,
		"known_models": []string{"x"},
	})
	if !isErr {
		t.Fatalf("set known_models on builtin: expected error, got %s", text)
	}
	if !strings.Contains(text, "builtin") {
		t.Fatalf("set known_models on builtin: want 'builtin', got %s", text)
	}

	// delete -> error.
	text, isErr = callMPTool(t, b, "delete_model_profile", map[string]any{"id": builtin.ID})
	if !isErr {
		t.Fatalf("delete builtin: expected error, got %s", text)
	}
	if !strings.Contains(text, "builtin") {
		t.Fatalf("delete builtin: want 'builtin', got %s", text)
	}

	// And confirm it's still there + unchanged.
	got, err := db.GetModelProfile(context.Background(), builtin.ID)
	if err != nil {
		t.Fatalf("get builtin after refused ops: %v", err)
	}
	if got.Name != "builtin-anthropic" || !got.Builtin {
		t.Fatalf("builtin row mutated: %+v", got)
	}
}

// TestModelProfileCreateValidationViaMCP confirms the MCP surface reuses
// the same validateModelProfile rules (secret required for non-CLI
// providers; CLI providers exempt; bad provider rejected).
func TestModelProfileCreateValidationViaMCP(t *testing.T) {
	b, _, _ := newModelProfileBackend(t)

	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name:    "missing name",
			args:    map[string]any{"provider": "anthropic", "secret_scope_id": "x"},
			wantErr: true,
		},
		{
			name:    "invalid provider",
			args:    map[string]any{"name": "bad", "provider": "openrouter"},
			wantErr: true,
		},
		{
			name:    "openai missing secret",
			args:    map[string]any{"name": "oa", "provider": "openai"},
			wantErr: true,
		},
		{
			name:    "openai_compat missing endpoint",
			args:    map[string]any{"name": "c", "provider": "openai_compat", "secret_scope_id": "x"},
			wantErr: true,
		},
		{
			// CLI provider needs no secret + no endpoint.
			name:    "claude_cli no secret ok",
			args:    map[string]any{"name": "local-claude", "provider": "claude_cli"},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := callMPTool(t, b, "create_model_profile", tc.args)
			if isErr != tc.wantErr {
				t.Fatalf("isErr = %v, want %v (body: %s)", isErr, tc.wantErr, text)
			}
		})
	}
}

// TestModelProfileToolsUnavailableWithoutAdmin confirms a clean structured
// error (not a panic) when the worker admin service isn't wired.
func TestModelProfileToolsUnavailableWithoutAdmin(t *testing.T) {
	b := NewInternalBackend(newTestDB(t), nil) // no SetWorkerAdmin
	for _, name := range []string{
		"list_model_profiles", "get_model_profile", "create_model_profile",
		"update_model_profile", "set_model_profile_known_models",
		"delete_model_profile",
	} {
		text, isErr := callMPTool(t, b, name, map[string]any{"id": "x", "known_models": []string{}})
		if !isErr {
			t.Fatalf("%s: expected error without worker admin, got %s", name, text)
		}
	}
}
