package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/auth"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newProvisionHandler builds a fully-wired handler with everything the
// orchestrator needs: real sqlite, ephemeral secret prompt manager, secrets
// manager, addon registry + creator. The created addons directory is in a
// per-test tempdir.
func newProvisionHandler(t *testing.T) (*handler, *ephemeral.Manager, *addon.Registry, string) {
	t.Helper()

	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), dir+"/test.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	if err := db.CreateWorkspace(context.Background(), &store.Workspace{
		ID: "ws-global", Name: "Global", RootPath: "/",
		DefaultPolicy: "allow", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := db.CreateDownstreamServer(context.Background(), &store.DownstreamServer{
		ID: "mcpx-builtin", Name: "MCPlexer Built-in", Transport: "internal",
		ToolNamespace: "mcpx", Discovery: "static", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create mcpx-builtin: %v", err)
	}
	if err := db.CreateDownstreamServer(context.Background(), &store.DownstreamServer{
		ID: "secret-builtin", Name: "Secret Prompts", Transport: "internal",
		ToolNamespace: "secret", Discovery: "static", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create secret-builtin: %v", err)
	}
	for _, r := range []store.RouteRule{
		{
			ID: "route-mcpx", WorkspaceID: "ws-global", Priority: 100,
			PathGlob: "**", ToolMatch: json.RawMessage(`["mcpx__*"]`),
			DownstreamServerID: "mcpx-builtin", Policy: "allow",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "route-secret", WorkspaceID: "ws-global", Priority: 99,
			PathGlob: "**", ToolMatch: json.RawMessage(`["secret__*"]`),
			DownstreamServerID: "secret-builtin", Policy: "allow",
			CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := db.CreateRouteRule(context.Background(), &r); err != nil {
			t.Fatalf("create route: %v", err)
		}
	}

	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	secretsMgr := secrets.NewManager(db, enc)

	mgr, err := ephemeral.New(context.Background(), db, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("ephemeral.New: %v", err)
	}
	t.Cleanup(mgr.Stop)

	addonsDir := filepath.Join(dir, "addons")
	if err := os.MkdirAll(addonsDir, 0o755); err != nil {
		t.Fatalf("mkdir addons: %v", err)
	}
	resolver := func(serverID string) (string, error) {
		srv, err := db.GetDownstreamServer(context.Background(), serverID)
		if err != nil {
			return "", err
		}
		return srv.ToolNamespace, nil
	}
	authResolver := func(name string) string {
		scopes, err := db.ListAuthScopes(context.Background())
		if err != nil {
			return ""
		}
		for _, s := range scopes {
			if s.Name == name {
				return s.ID
			}
		}
		return ""
	}
	reg, err := addon.LoadDir(addonsDir, resolver, addon.WithAuthScopeResolver(authResolver))
	if err != nil {
		t.Fatalf("addon.LoadDir: %v", err)
	}
	creator := &addon.Creator{
		Registry: reg, Dir: addonsDir,
		Resolve: resolver, AuthScopeResolve: authResolver,
	}
	authInj := auth.NewInjector(secretsMgr, nil, db)
	exec := addon.NewExecutor(authInj.HeadersForDownstream)

	engine := routing.NewEngine(db)
	lister := &mockToolLister{}
	h := newHandler(db, engine, lister, nil, TransportSocket,
		nil, nil, nil, nil, reg, exec, nil, mgr, secretsMgr, nil, nil, nil, nil, nil, nil)
	h.addonCreator = creator
	h.sessions.clientPath = "/"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	return h, mgr, reg, addonsDir
}

// dispatchProvision drives mcpx__provision_mcp through the full tools/call
// pipeline (routing, builtin dispatch). Mirrors the path real agents hit.
func dispatchProvision(t *testing.T, h *handler, args string) (json.RawMessage, *RPCError) {
	t.Helper()
	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__provision_mcp",
		Arguments: json.RawMessage(args),
	})
	return h.handleToolsCall(context.Background(), params)
}

// TestProvisionMCP_BearerEndToEnd is the happy-path integration test:
//
//   - Agent calls mcpx__provision_mcp with a bearer-auth API.
//   - The orchestrator creates the parent server, auth scope, route, addon.
//   - A secret prompt blocks until a fake "human" submits the token.
//   - The token is persisted into the auth scope encrypted; agent never sees it.
//   - The addon is hot-registered and its tools are now in the registry.
//
// The whole flow is exercised through the real tools/call dispatcher path so
// regressions in routing or auth scope resolution surface here.
func TestProvisionMCP_BearerEndToEnd(t *testing.T) {
	h, mgr, reg, addonsDir := newProvisionHandler(t)

	// Stand up a tiny OpenAPI server to fetch from.
	const openAPISpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Demo API", "version": "1"},
  "servers": [{"url": "https://api.demo.test/v1"}],
  "components": {"securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}}},
  "security": [{"bearer": []}],
  "paths": {
    "/widgets": {
      "get": {"operationId": "listWidgets", "summary": "List widgets",
        "responses": {"200": {"description": "ok"}}}
    },
    "/widgets/{id}": {
      "get": {"operationId": "getWidget", "summary": "Get widget",
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "ok"}}}
    }
  }
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAPISpec))
	}))
	defer srv.Close()

	args := `{
		"api_name": "demo",
		"description": "Demo API for provisioning test",
		"spec_url": "` + srv.URL + `/openapi.json",
		"auth": {"kind": "bearer"},
		"secret_label": "Demo API token",
		"secret_reason": "End-to-end provisioning test"
	}`

	resCh := make(chan struct {
		raw  json.RawMessage
		rerr *RPCError
	}, 1)
	go func() {
		raw, rerr := dispatchProvision(t, h, args)
		resCh <- struct {
			raw  json.RawMessage
			rerr *RPCError
		}{raw, rerr}
	}()

	// Wait for the secret prompt to appear, then submit the human's token.
	const humanToken = "tok_abc_super_secret"
	var promptID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := mgr.ListPendingForTest()
		if err == nil && len(rows) == 1 {
			promptID = rows[0].ID
			break
		}
		select {
		case r := <-resCh:
			t.Fatalf("orchestrator returned before prompt: rerr=%+v raw=%s", r.rerr, string(r.raw))
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if promptID == "" {
		t.Fatal("no pending secret prompt after dispatch — orchestrator did not call secret prompt path")
	}
	if err := mgr.Submit(context.Background(), promptID, []byte(humanToken)); err != nil {
		t.Fatalf("submit token: %v", err)
	}

	var raw json.RawMessage
	var rerr *RPCError
	select {
	case r := <-resCh:
		raw, rerr = r.raw, r.rerr
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator did not return after secret submitted")
	}
	if rerr != nil {
		t.Fatalf("orchestrator error: %+v", rerr)
	}

	var ctr CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if ctr.IsError {
		t.Fatalf("orchestrator IsError=true; content=%v", ctr.Content)
	}
	text := ctr.Content[0].Text

	// Critical: the agent NEVER sees the human's token, no matter what.
	if strings.Contains(text, humanToken) {
		t.Fatalf("orchestrator response leaked the human's token:\n%s", text)
	}

	// Provisioned tools must show up in the addon registry. The OpenAPI
	// slugifier lowercases operationId without splitting CamelCase, so
	// "listWidgets" becomes "listwidgets".
	if got := reg.GetTool("demo__listwidgets"); got == nil {
		t.Errorf("demo__listwidgets missing from registry after provision")
	}
	if got := reg.GetTool("demo__getwidget"); got == nil {
		t.Errorf("demo__getwidget missing from registry after provision")
	}

	// YAML on disk should NOT contain the token (confidence check on top of
	// the in-memory leak test).
	yamlPath := filepath.Join(addonsDir, "demo.yaml")
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read addon yaml: %v", err)
	}
	if strings.Contains(string(yamlBytes), humanToken) {
		t.Errorf("addon YAML on disk contains the human token")
	}
}

// TestProvisionMCP_RejectsOAuth2 verifies the OAuth2 bail-out: when the
// imported spec uses OAuth2, the orchestrator returns clear guidance and
// never tries to drive a secret prompt or write an addon.
func TestProvisionMCP_RejectsOAuth2(t *testing.T) {
	h, mgr, reg, _ := newProvisionHandler(t)

	const oauthSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "OAuthOnly API", "version": "1"},
  "servers": [{"url": "https://oauth.demo.test"}],
  "components": {"securitySchemes": {"oauth2": {
    "type": "oauth2",
    "flows": {"authorizationCode": {
      "authorizationUrl": "https://oauth.demo.test/authorize",
      "tokenUrl": "https://oauth.demo.test/token",
      "scopes": {"read": "read access"}
    }}
  }}},
  "security": [{"oauth2": ["read"]}],
  "paths": {"/things": {"get": {"operationId": "listThings",
    "summary": "List things",
    "responses": {"200": {"description": "ok"}}}}}
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(oauthSpec))
	}))
	defer srv.Close()

	raw, rerr := dispatchProvision(t, h, `{
		"api_name": "oauthonly",
		"spec_url": "`+srv.URL+`/openapi.json"
	}`)
	if rerr != nil {
		t.Fatalf("orchestrator error: %+v", rerr)
	}
	var ctr CallToolResult
	_ = json.Unmarshal(raw, &ctr)
	text := ctr.Content[0].Text
	if !strings.Contains(strings.ToLower(text), "oauth2") {
		t.Errorf("expected OAuth2 bail-out message; got: %s", text)
	}

	// No prompts were created (orchestrator bailed before secret capture).
	rows, _ := mgr.ListPendingForTest()
	if len(rows) != 0 {
		t.Errorf("unexpected pending prompts after OAuth bail-out: %d", len(rows))
	}
	// No addon registered.
	if reg.GetTool("oauthonly__listthings") != nil {
		t.Errorf("addon registered despite OAuth2 bail-out")
	}
}

// TestProvisionMCP_RejectsBadAPIName guards the namespace regex; we don't
// want agents creating addons with "; rm -rf /" or similar in their name.
func TestProvisionMCP_RejectsBadAPIName(t *testing.T) {
	h, _, _, _ := newProvisionHandler(t)

	for _, bad := range []string{"", "Bad-Name", "with space", "1leadingdigit", strings.Repeat("a", 100)} {
		raw, rerr := dispatchProvision(t, h, `{"api_name": "`+bad+`"}`)
		if rerr != nil && rerr.Code != CodeInvalidParams {
			t.Fatalf("bad api_name %q: rpc error %+v", bad, rerr)
		}
		if rerr == nil {
			var ctr CallToolResult
			_ = json.Unmarshal(raw, &ctr)
			if !ctr.IsError {
				t.Errorf("api_name %q: expected isError, got: %s", bad, ctr.Content[0].Text)
			}
		}
	}
}
