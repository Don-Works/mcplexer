package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newSecretListRefsHandler builds a handler wired with a real secrets
// manager + two pre-populated auth scopes so the dispatch path returns
// the same shape an agent would see in production.
func newSecretListRefsHandler(t *testing.T) *handler {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.New(ctx, dir+"/test.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "ws-global", Name: "Global", RootPath: "/",
		DefaultPolicy: "allow", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "secret-builtin", Name: "Secret Builtin", Transport: "internal",
		ToolNamespace: "secret", Discovery: "static", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create secret-builtin: %v", err)
	}
	for _, r := range []store.RouteRule{
		{
			ID: "secret-allow", WorkspaceID: "ws-global", Priority: 100,
			PathGlob: "**", ToolMatch: json.RawMessage(`["secret__*"]`),
			DownstreamServerID: "secret-builtin", Policy: "allow",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "allow-rest", WorkspaceID: "ws-global", Priority: 1,
			PathGlob: "**", ToolMatch: json.RawMessage(`["*"]`),
			Policy: "allow", CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := db.CreateRouteRule(ctx, &r); err != nil {
			t.Fatalf("create route: %v", err)
		}
	}

	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	for _, scope := range []store.AuthScope{
		{ID: "scope-stripe", Name: "stripe-prod", Type: "env", Source: "test"},
		{ID: "scope-github", Name: "github", Type: "env", Source: "test"},
	} {
		if err := db.CreateAuthScope(ctx, &scope); err != nil {
			t.Fatalf("create scope %s: %v", scope.ID, err)
		}
	}
	sm := secrets.NewManager(db, enc)
	for _, x := range []struct{ scope, key, val string }{
		{"scope-stripe", "api-key", "stripe_secret_AAA"},
		{"scope-stripe", "webhook-secret", "whsec_BBB"},
		{"scope-github", "pat", "github_secret_CCC"},
	} {
		if err := sm.Put(ctx, x.scope, x.key, []byte(x.val)); err != nil {
			t.Fatalf("put %s/%s: %v", x.scope, x.key, err)
		}
	}

	engine := routing.NewEngine(db)
	lister := &mockToolLister{}
	h := newHandler(db, engine, lister, nil, TransportSocket, nil, nil, nil, nil, nil, nil, nil, nil, sm, nil, nil, nil, nil, nil, nil)
	h.sessions.clientPath = "/"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	return h
}

// extractRefs unwraps the MCP CallToolResult envelope and decodes the
// {"refs":[...]} payload the agent receives.
func extractRefs(t *testing.T, raw json.RawMessage) []secretRefEntry {
	t.Helper()
	var ctr CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("unmarshal CallToolResult: %v", err)
	}
	if ctr.IsError {
		t.Fatalf("result IsError = true; content=%v", ctr.Content)
	}
	if len(ctr.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(ctr.Content))
	}
	var payload struct {
		Refs []secretRefEntry `json:"refs"`
	}
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), &payload); err != nil {
		t.Fatalf("payload not JSON: %v\n%s", err, ctr.Content[0].Text)
	}
	return payload.Refs
}

func TestSecretListRefs_ReturnsAllScopesAndKeys(t *testing.T) {
	h := newSecretListRefsHandler(t)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "secret__list_refs",
		Arguments: json.RawMessage(`{}`),
	})
	raw, rerr := h.handleToolsCall(context.Background(), params)
	if rerr != nil {
		t.Fatalf("dispatch error: %+v", rerr)
	}
	refs := extractRefs(t, raw)
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3: %+v", len(refs), refs)
	}

	keysByScope := map[string]map[string]bool{}
	for _, r := range refs {
		// Ref field must match secret://<key> exactly — that's the value the
		// agent splices into tool args.
		if r.Ref != "secret://"+r.Key {
			t.Errorf("ref %q != secret://%s", r.Ref, r.Key)
		}
		if keysByScope[r.ScopeName] == nil {
			keysByScope[r.ScopeName] = map[string]bool{}
		}
		keysByScope[r.ScopeName][r.Key] = true
	}
	if !keysByScope["stripe-prod"]["api-key"] || !keysByScope["stripe-prod"]["webhook-secret"] {
		t.Errorf("stripe-prod keys missing: %+v", keysByScope["stripe-prod"])
	}
	if !keysByScope["github"]["pat"] {
		t.Errorf("github keys missing: %+v", keysByScope["github"])
	}
}

func TestSecretListRefs_PlaintextNeverInResponse(t *testing.T) {
	h := newSecretListRefsHandler(t)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "secret__list_refs",
		Arguments: json.RawMessage(`{}`),
	})
	raw, rerr := h.handleToolsCall(context.Background(), params)
	if rerr != nil {
		t.Fatalf("dispatch error: %+v", rerr)
	}
	body := string(raw)
	for _, plaintext := range []string{"stripe_secret_AAA", "whsec_BBB", "github_secret_CCC"} {
		if strings.Contains(body, plaintext) {
			t.Errorf("response leaked plaintext %q:\n%s", plaintext, body)
		}
	}
}

func TestSecretListRefs_FilterByScopeName(t *testing.T) {
	h := newSecretListRefsHandler(t)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "secret__list_refs",
		Arguments: json.RawMessage(`{"scope":"github"}`),
	})
	raw, rerr := h.handleToolsCall(context.Background(), params)
	if rerr != nil {
		t.Fatalf("dispatch error: %+v", rerr)
	}
	refs := extractRefs(t, raw)
	if len(refs) != 1 {
		t.Fatalf("got %d refs after scope=github filter, want 1: %+v", len(refs), refs)
	}
	if refs[0].ScopeName != "github" || refs[0].Key != "pat" {
		t.Errorf("filtered wrong entry: %+v", refs[0])
	}
}

func TestSecretListRefs_FilterByScopeID(t *testing.T) {
	h := newSecretListRefsHandler(t)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "secret__list_refs",
		Arguments: json.RawMessage(`{"scope":"scope-stripe"}`),
	})
	raw, rerr := h.handleToolsCall(context.Background(), params)
	if rerr != nil {
		t.Fatalf("dispatch error: %+v", rerr)
	}
	refs := extractRefs(t, raw)
	if len(refs) != 2 {
		t.Fatalf("got %d refs after scope=scope-stripe filter, want 2: %+v", len(refs), refs)
	}
}

func TestSecretListRefs_UnknownScopeReturnsEmpty(t *testing.T) {
	h := newSecretListRefsHandler(t)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "secret__list_refs",
		Arguments: json.RawMessage(`{"scope":"does-not-exist"}`),
	})
	raw, rerr := h.handleToolsCall(context.Background(), params)
	if rerr != nil {
		t.Fatalf("dispatch error: %+v", rerr)
	}
	refs := extractRefs(t, raw)
	if len(refs) != 0 {
		t.Errorf("got %d refs for unknown scope, want 0: %+v", len(refs), refs)
	}
}
