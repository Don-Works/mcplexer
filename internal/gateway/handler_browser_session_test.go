package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// browserCtxLister captures the per-agent browser isolation id that the
// gateway stamps onto the dispatch context, so we can assert the MCP session
// id flows all the way down to the downstream Manager (which keys browser
// instances by it).
type browserCtxLister struct {
	mockToolLister
	mu  sync.Mutex
	got string
}

func (l *browserCtxLister) Call(ctx context.Context, _, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
	l.mu.Lock()
	l.got = downstream.BrowserSessionIDFromContext(ctx)
	l.callCount++
	l.mu.Unlock()
	return json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"isError":false}`), nil
}

func (l *browserCtxLister) captured() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.got
}

// TestHandleToolsCall_StampsSessionAsBrowserIsolationID drives a downstream
// call through the code-mode sandbox (the real dispatch path) and asserts the
// gateway tagged the call with this session's id, which is what gives each
// agent session its own browser instance downstream.
func TestHandleToolsCall_StampsSessionAsBrowserIsolationID(t *testing.T) {
	lister := &browserCtxLister{
		mockToolLister: mockToolLister{
			tools: map[string]json.RawMessage{
				"gh-server": toolsJSON(Tool{
					Name:        "create_issue",
					Description: "Create issue",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
				}),
			},
		},
	}
	ms := &mockStore{
		servers: []store.DownstreamServer{
			{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
			{
				ID: "mcpx-builtin", Name: "MCPlexer Built-in Tools",
				Transport: "internal", ToolNamespace: "mcpx", Discovery: "static",
			},
		},
		capUpdates: make(map[string]json.RawMessage),
		workspaces: []mockWorkspace{{id: "ws-global", rootPath: "/"}},
		routeRules: map[string][]store.RouteRule{
			"ws-global": {
				{
					ID: "builtin-allow", WorkspaceID: "ws-global",
					Priority: 100, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["mcpx__*"]`),
					DownstreamServerID: "mcpx-builtin",
				},
				{
					ID: "allow-gh", WorkspaceID: "ws-global",
					Priority: 10, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["github__*"]`),
					DownstreamServerID: "gh-server",
				},
			},
		},
	}
	h := newHandler(
		ms, routing.NewEngine(ms), lister, nil, TransportSocket,
		nil, nil, nil, config.NewSettingsService(ms), nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h.sessions.clientPath = "/test"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	h.sessions.session = &store.Session{ID: "sess-browser-xyz"}

	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code":"github.create_issue({ title: 'bug' }); print('ok');"}`),
	})
	_, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("downstream calls = %d, want 1", lister.callCount)
	}
	if got := lister.captured(); got != "sess-browser-xyz" {
		t.Fatalf("browser isolation id seen downstream = %q, want %q", got, "sess-browser-xyz")
	}
}
