// handler_memory_list_scope_test.go — gateway-level coverage for the
// scope param on memory__list. Verifies that:
//   - scope="" or scope="any" → default workspaces ∪ global behavior
//   - scope="global_only"     → returns only workspace_id IS NULL rows
//   - scope="workspace_only"  → returns only workspace-scoped rows
//   - invalid scope value     → error result (not an RPC error, but a
//     tool-call-level error string)
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newHandlerWithMemoryDB constructs a handler + memory.Service backed by a
// real in-memory SQLite store. Returns both so the test can seed rows
// directly via the service.
func newHandlerWithMemoryDB(t *testing.T) (*handler, *memory.Service) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.memorySvc = svc
	return h, svc
}

// seedMemory writes one memory entry via the service. wsID="" writes global.
func seedMemory(t *testing.T, svc *memory.Service, name, wsID string) string {
	t.Helper()
	ctx := context.Background()
	var ws *string
	if wsID != "" {
		ws = &wsID
	}
	id, err := svc.Write(ctx, memory.WriteOptions{
		Name:        name,
		Content:     name + "-content",
		WorkspaceID: ws,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return id
}

func seedMemoryFromSource(
	t *testing.T, svc *memory.Service, name, wsID, sourceSessionID string,
) string {
	t.Helper()
	ctx := context.Background()
	var ws *string
	if wsID != "" {
		ws = &wsID
	}
	id, err := svc.Write(ctx, memory.WriteOptions{
		Name:            name,
		Content:         name + "-content",
		WorkspaceID:     ws,
		SourceSessionID: sourceSessionID,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return id
}

// listResponse calls memory__list and returns the response text (now JSON
// for structured consumption by execute_code / workers). Fails the test on
// RPC-level errors. Callers using Contains still work because names/ids are
// embedded in the JSON.
func listResponse(
	t *testing.T, h *handler, ctx context.Context,
	extra string,
) string {
	t.Helper()
	raw := json.RawMessage(`{"limit":50` + extra + `}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__list", raw)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__list: handled=%v rpcErr=%v", handled, rpcErr)
	}
	return string(resp)
}

func TestMemoryListScope_Default(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)
	seedMemory(t, svc, "global-note", "")
	seedMemory(t, svc, "ws-note", "ws-list-scope")

	// Default handler scope is the "ws-global" workspace set by newTestHandler.
	// With scope="" the filter uses the default workspaces ∪ global behavior.
	// Global rows always appear; this confirms the handler passes the scope
	// param through correctly.
	resp := listResponse(t, h, ctx, `,"scope":""`)
	if !strings.Contains(resp, "global-note") {
		t.Errorf("scope='' should include global rows; got: %s", resp)
	}
}

func TestMemoryListScope_GlobalOnly(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)

	seedMemory(t, svc, "global-only-note", "")
	seedMemory(t, svc, "ws-should-not-appear", "ws-list-global")

	// Extend the session scope to include the workspace so the default filter
	// would normally include both — verify global_only excludes workspace rows.
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: "ws-list-global", RootPath: "/test/global"},
	}

	resp := listResponse(t, h, ctx, `,"scope":"global_only"`)
	if strings.Contains(resp, "ws-should-not-appear") {
		t.Errorf("scope=global_only should exclude workspace rows; got: %s", resp)
	}
	if !strings.Contains(resp, "global-only-note") {
		t.Errorf("scope=global_only should include global rows; got: %s", resp)
	}
}

func TestMemoryListScope_WorkspaceOnly(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)

	seedMemory(t, svc, "global-should-not-appear", "")
	seedMemory(t, svc, "ws-only-note", "ws-list-ws")

	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: "ws-list-ws", RootPath: "/test/ws"},
	}

	resp := listResponse(t, h, ctx, `,"scope":"workspace_only"`)
	if strings.Contains(resp, "global-should-not-appear") {
		t.Errorf("scope=workspace_only should exclude global rows; got: %s", resp)
	}
	if !strings.Contains(resp, "ws-only-note") {
		t.Errorf("scope=workspace_only should include workspace rows; got: %s", resp)
	}
}

func TestMemoryListScope_InvalidScope(t *testing.T) {
	ctx := context.Background()
	h, _ := newHandlerWithMemoryDB(t)

	raw := json.RawMessage(`{"scope":"bad-scope"}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__list", raw)
	if !handled {
		t.Fatalf("memory__list not handled")
	}
	if rpcErr != nil {
		t.Fatalf("expected tool-level error (not RPC), got rpcErr=%v", rpcErr)
	}
	body := string(resp)
	if !strings.Contains(body, "invalid scope") {
		t.Errorf("expected 'invalid scope' in response; got: %s", body)
	}
}

func TestMemoryListScope_AnyAliasesDefault(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)
	seedMemory(t, svc, "global-any-note", "")

	resp := listResponse(t, h, ctx, `,"scope":"any"`)
	if !strings.Contains(resp, "global-any-note") {
		t.Errorf("scope='any' should behave like default; got: %s", resp)
	}
}

func TestMemoryForgetBySourceUsesSessionScope(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)
	source := "sess-shared"
	global := seedMemoryFromSource(t, svc, "global-purge", "", source)
	wsA := seedMemoryFromSource(t, svc, "ws-a-purge", "ws-a", source)
	wsB := seedMemoryFromSource(t, svc, "ws-b-survive", "ws-b", source)
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: "ws-a", RootPath: "/test/ws-a"},
	}

	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__forget_by_source",
		json.RawMessage(`{"source_session_id":"sess-shared"}`))
	if !handled || rpcErr != nil {
		t.Fatalf("memory__forget_by_source: handled=%v rpcErr=%v", handled, rpcErr)
	}
	if !strings.Contains(string(resp), "2 in-scope") {
		t.Fatalf("expected scoped purge count in response, got %s", string(resp))
	}
	for _, id := range []string{global, wsA} {
		if _, err := svc.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected %s to be purged, err=%v", id, err)
		}
	}
	if _, err := svc.Get(ctx, wsB); err != nil {
		t.Fatalf("expected ws-b memory to survive: %v", err)
	}
}

func TestMemoryForgetBySourceWorkerRequiresWriteGrant(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)
	id := seedMemoryFromSource(t, svc, "read-only-survive", "ws-a", "sess-readonly")
	workerCtx := WithWorkerWorkspaceAccess(ctx, "ws-a", []WorkerWorkspaceGrant{
		{WorkspaceID: "ws-a", Access: store.WorkerWorkspaceAccessRead},
	})

	_, rpcErr, handled := h.dispatchMemoryTool(workerCtx, "memory__forget_by_source",
		json.RawMessage(`{"source_session_id":"sess-readonly"}`))
	if !handled {
		t.Fatal("memory__forget_by_source not handled")
	}
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "writable workspace grant") {
		t.Fatalf("expected writable-grant RPC error, got %v", rpcErr)
	}
	if _, err := svc.Get(ctx, id); err != nil {
		t.Fatalf("read-only worker should not purge memory: %v", err)
	}
}
