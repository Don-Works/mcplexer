package gateway

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newSecretPromptHandler builds a gateway handler bound to a real sqlite db
// (the manager needs the SecretPromptStore methods) plus a working
// ephemeral manager. The lister has no downstream tools — only built-ins.
func newSecretPromptHandler(t *testing.T) (*handler, *ephemeral.Manager) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), dir+"/test.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Seed a global workspace + permissive routes, including secret-allow.
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
		t.Fatalf("create downstream: %v", err)
	}
	if err := db.CreateDownstreamServer(context.Background(), &store.DownstreamServer{
		ID: "secret-builtin", Name: "Secret Prompts", Transport: "internal",
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
		if err := db.CreateRouteRule(context.Background(), &r); err != nil {
			t.Fatalf("create route: %v", err)
		}
	}

	mgr, err := ephemeral.New(context.Background(), db, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("ephemeral.New: %v", err)
	}
	t.Cleanup(mgr.Stop)

	engine := routing.NewEngine(db)
	lister := &mockToolLister{}
	h := newHandler(db, engine, lister, nil, TransportSocket, nil, nil, nil, nil, nil, nil, nil, mgr, nil, nil, nil, nil, nil, nil, nil)
	h.sessions.clientPath = "/"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	return h, mgr
}

// TestSecretPromptDispatchEndToEnd: agent -> tools/call("secret__prompt") ->
// blocks until UI submits -> agent receives {file_path, ...}. The secret
// value MUST NOT appear in the result, only the path.
func TestSecretPromptDispatchEndToEnd(t *testing.T) {
	h, mgr := newSecretPromptHandler(t)

	// Run the dispatch in a goroutine; it blocks until Submit fires.
	type dispatchResult struct {
		raw  json.RawMessage
		rerr *RPCError
	}
	resCh := make(chan dispatchResult, 1)
	go func() {
		params, _ := json.Marshal(CallToolRequest{
			Name: "secret__prompt",
			Arguments: json.RawMessage(`{
				"reason": "connect to prod customers",
				"label": "PROD_DATABASE_URL",
				"timeout_sec": 10,
				"delete_on_read": false
			}`),
		})
		raw, rerr := h.handleToolsCall(context.Background(), params)
		resCh <- dispatchResult{raw: raw, rerr: rerr}
	}()

	// Wait for the manager to register a pending prompt.
	var promptID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := mgr.ListPendingForTest()
		if err == nil && len(rows) == 1 {
			promptID = rows[0].ID
			break
		}
		select {
		case r := <-resCh:
			t.Fatalf("dispatch returned early: rerr=%+v raw=%s", r.rerr, string(r.raw))
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if promptID == "" {
		t.Fatal("no pending prompt after dispatch")
	}

	const secretValue = "postgres://shhhh:p4ss@example/db"
	if err := mgr.Submit(context.Background(), promptID, []byte(secretValue)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case r := <-resCh:
		if r.rerr != nil {
			t.Fatalf("dispatch error: %+v", r.rerr)
		}
		var ctr CallToolResult
		if err := json.Unmarshal(r.raw, &ctr); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if ctr.IsError {
			t.Fatalf("result IsError = true; content=%v", ctr.Content)
		}
		if len(ctr.Content) != 1 {
			t.Fatalf("result content count = %d, want 1", len(ctr.Content))
		}
		text := ctr.Content[0].Text
		// The secret value must NEVER appear in the response.
		if strings.Contains(text, secretValue) {
			t.Fatalf("response contained the secret value:\n%s", text)
		}
		// The result is JSON containing file_path/handle/expires_at.
		var payload struct {
			FilePath  string `json:"file_path"`
			Handle    string `json:"handle"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			t.Fatalf("response is not JSON: %s", text)
		}
		if payload.FilePath == "" || payload.Handle == "" {
			t.Fatalf("missing fields: %+v", payload)
		}
		if payload.Handle != promptID {
			t.Errorf("handle = %q, want %q", payload.Handle, promptID)
		}
		// File should exist with the secret bytes.
		got, err := os.ReadFile(payload.FilePath)
		if err != nil {
			t.Fatalf("read secret file: %v", err)
		}
		if string(got) != secretValue {
			t.Errorf("file contents differ from submitted secret")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not return after Submit")
	}
}

// TestSecretPromptCancelledIsErrorResult verifies a user cancel surfaces as
// an MCP isError result (not an RPC error) and never leaks any path.
func TestSecretPromptCancelledIsErrorResult(t *testing.T) {
	h, mgr := newSecretPromptHandler(t)

	type dispatchResult struct {
		raw  json.RawMessage
		rerr *RPCError
	}
	resCh := make(chan dispatchResult, 1)
	go func() {
		params, _ := json.Marshal(CallToolRequest{
			Name: "secret__prompt",
			Arguments: json.RawMessage(`{
				"reason": "connect", "label": "X", "timeout_sec": 5
			}`),
		})
		raw, rerr := h.handleToolsCall(context.Background(), params)
		resCh <- dispatchResult{raw: raw, rerr: rerr}
	}()

	var promptID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := mgr.ListPendingForTest()
		if err == nil && len(rows) == 1 {
			promptID = rows[0].ID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if promptID == "" {
		t.Fatal("no pending prompt after dispatch")
	}
	if err := mgr.Cancel(context.Background(), promptID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case r := <-resCh:
		if r.rerr != nil {
			t.Fatalf("expected isError result, got RPC error: %+v", r.rerr)
		}
		var ctr CallToolResult
		if err := json.Unmarshal(r.raw, &ctr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !ctr.IsError {
			t.Errorf("expected IsError = true on cancel")
		}
		// Never leak a /tmp path.
		for _, c := range ctr.Content {
			if strings.Contains(c.Text, "/secrets/ephemeral/") {
				t.Errorf("cancel result leaks file path: %s", c.Text)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not return after Cancel")
	}
}
