package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/compact"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// arrayResultLister returns a downstream result whose text content is an array
// of >=3 homogeneous objects — exactly the shape that compactor.CompactToolResult
// would turn into the columnar {_cols,_rows,_fixed} map.
type arrayResultLister struct {
	mockToolLister
	mu   sync.Mutex
	body json.RawMessage
}

func (l *arrayResultLister) Call(_ context.Context, _, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
	l.mu.Lock()
	l.callCount++
	l.mu.Unlock()
	return l.body, nil
}

// arrayProfilesBody is a CallToolResult whose single text block is a 3-element
// array of {id,name} objects — the canonical list-tool shape that was being
// mangled into columnar form before reaching JS.
func arrayProfilesBody() json.RawMessage {
	return json.RawMessage(
		`{"content":[{"type":"text","text":"[{\"id\":\"p1\",\"name\":\"fast\"},{\"id\":\"p2\",\"name\":\"smart\"},{\"id\":\"p3\",\"name\":\"cheap\"}]"}],"isError":false}`,
	)
}

// newArrayResultHandler wires a handler whose only downstream tool
// (profiles__list) returns arrayProfilesBody, with a real SettingsService so
// CompactResponses is genuinely enabled (the default).
func newArrayResultHandler(t *testing.T, body json.RawMessage) (*handler, *arrayResultLister) {
	t.Helper()
	lister := &arrayResultLister{
		mockToolLister: mockToolLister{
			tools: map[string]json.RawMessage{
				"profiles-server": toolsJSON(Tool{
					Name:        "list",
					Description: "List model profiles",
					InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
				}),
			},
		},
		body: body,
	}
	ms := &mockStore{
		servers: []store.DownstreamServer{
			{ID: "profiles-server", ToolNamespace: "profiles", Discovery: "static"},
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
					ID: "allow-profiles", WorkspaceID: "ws-global",
					Priority: 10, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["profiles__*"]`),
					DownstreamServerID: "profiles-server",
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
	h.sessions.session = &store.Session{ID: "sess-iterable"}
	return h, lister
}

// TestHandleToolsCall_CodeModeArrayResultIsIterable drives a downstream tool
// that returns an array of >=3 objects through the REAL code-mode dispatch path
// (mcpx__execute_code -> handlerToolCaller -> handleToolsCall -> sandbox) and
// asserts the value the JS sees is a naturally iterable plain array: it can be
// .map'd, indexed, and Array.isArray returns true. Before the fix the gateway's
// CompactToolResult columnarized the array into {_cols,_rows,_fixed}, so .map
// returned undefined and r[0].name was unreadable — the agent read "0 profiles".
func TestHandleToolsCall_CodeModeArrayResultIsIterable(t *testing.T) {
	h, _ := newArrayResultHandler(t, arrayProfilesBody())

	params, _ := json.Marshal(CallToolRequest{
		Name: "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code":"` +
			`const r = profiles.list({});` +
			`print('isArray=' + Array.isArray(r) + ' len=' + r.length + ' first=' + r[0].name + ' ids=' + r.map(p => p.id).join(','));"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	joined := joinContent(tr.Content)
	if tr.IsError {
		t.Fatalf("execute_code errored (columnar value would break .map/[i]):\n%s", joined)
	}
	if !strings.Contains(joined, "isArray=true len=3 first=fast ids=p1,p2,p3") {
		t.Fatalf("array result not naturally iterable in JS:\n%s", joined)
	}
	// Columnar markers must never appear in the agent-visible output.
	for _, marker := range []string{"_cols", "_rows", "_fixed"} {
		if strings.Contains(joined, marker) {
			t.Fatalf("columnar marker %q leaked into code-mode output:\n%s", marker, joined)
		}
	}
}

// TestHandleToolsCall_CodeModeArrayResultNotColumnar asserts directly at the
// dispatch seam that handleToolsCall, when the call is internal (code-mode),
// returns the array text UN-columnarized — and crucially that the same payload
// run through the compactor WOULD columnarize, proving the skip is load-bearing
// rather than a no-op on this input.
func TestHandleToolsCall_CodeModeArrayResultNotColumnar(t *testing.T) {
	body := arrayProfilesBody()
	h, lister := newArrayResultHandler(t, body)

	// Sanity: the compactor really does columnarize this exact payload, so a
	// passing assertion below means the skip — not the input shape — is what
	// keeps the result iterable.
	if columnar := compact.New().CompactToolResult(body); !strings.Contains(string(columnar), "_cols") {
		t.Fatalf("precondition failed: compactor did not columnarize the test payload: %s", columnar)
	}

	ctx := withInternalCodeModeCall(context.Background())
	params, _ := json.Marshal(CallToolRequest{
		Name:      "profiles__list",
		Arguments: json.RawMessage(`{}`),
	})
	out, rpcErr := h.handleToolsCall(ctx, params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("downstream calls = %d, want 1", lister.callCount)
	}

	// Extract the text block and assert it parses back to a plain array.
	var env struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal result envelope: %v", err)
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content: %s", out)
	}
	text, _ := env.Content[0]["text"].(string)
	if strings.Contains(text, "_cols") || strings.Contains(text, "_rows") {
		t.Fatalf("internal code-mode result was columnarized (must stay a plain array): %s", text)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		t.Fatalf("internal code-mode result text is not a plain array: %q (%v)", text, err)
	}
	if len(arr) != 3 || arr[0]["name"] != "fast" {
		t.Fatalf("array content wrong: %#v", arr)
	}
}
