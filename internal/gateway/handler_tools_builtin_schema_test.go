// handler_tools_builtin_schema_test.go — schema-aware argument coercion for
// BUILT-IN tools, exercised through the real tools/call dispatch path.
//
// Built-ins never appear in the downstream tools/list catalog, so
// toolInputStringFields used to return nil for them and every builtin dispatch
// fell back to the legacy coerce-everything path. That re-parsed any string
// argument starting with '[' or '{', which turned mcpx__delegate_worker's
// tool_allowlist_json — declared "type":"string" and carrying a JSON array as
// TEXT by contract — into a real array, and the DelegationInput decoder then
// rejected the call with:
//
//	cannot unmarshal array into Go struct field DelegationInput.tool_allowlist_json of type string
//
// The string-field helpers existed but nothing in production referenced them.
package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// delegationAllowlistJSON is the by-contract shape: a JSON array carried as a
// string. It must reach the DelegationInput decoder byte-identical.
const delegationAllowlistJSON = `["mcpx__execute_code","index__symbols"]`

// newDelegationCapableHandler wires a handler whose builtin surface actually
// includes the delegation tools — buildAllBuiltinTools gates them behind a
// non-nil workerAdmin, so without one there is no delegate_worker schema to
// resolve (and no dispatchable tool either).
func newDelegationCapableHandler(t *testing.T, lister ToolLister, servers []store.DownstreamServer) *handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "delegation.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(lister, servers)
	h.workerAdmin = workersadmin.New(db, workersadmin.Options{Workspaces: db})
	return h
}

// TestToolInputStringFieldsResolvesBuiltinSchema walks the exact production
// sequence at handler_tools.go: route the tool, then resolve its string fields
// from the resulting downstream server id.
func TestToolInputStringFieldsResolvesBuiltinSchema(t *testing.T) {
	ctx := context.Background()
	h := newDelegationCapableHandler(t, &mockToolLister{tools: map[string]json.RawMessage{}}, nil)

	routeResult, err := h.engine.RouteWithFallback(ctx, routing.RouteContext{
		ToolName: "mcpx__delegate_worker",
	}, h.routingClientRoot(ctx), h.routingWorkspaceAncestors(ctx))
	if err != nil {
		t.Fatalf("route mcpx__delegate_worker: %v", err)
	}
	if routeResult.DownstreamServerID != "mcpx-builtin" {
		t.Fatalf("routed to %q, want mcpx-builtin", routeResult.DownstreamServerID)
	}

	fields := h.toolInputStringFields(ctx, routeResult.DownstreamServerID,
		extractOriginalToolName("mcpx__delegate_worker"))
	if !fields["tool_allowlist_json"] {
		t.Fatalf("tool_allowlist_json did not resolve as a string field: %v", fields)
	}
	// Array/object-typed neighbours must stay coercible — the fix narrows the
	// coercer for declared strings only, it does not disable it.
	if fields["touches_files"] {
		t.Errorf("touches_files is type array and must remain coercible")
	}
	if fields["capability_profile"] {
		t.Errorf("capability_profile is type object and must remain coercible")
	}

	in := `{"tool_allowlist_json":` + mustJSONString(t, delegationAllowlistJSON) +
		`,"touches_files":"[\"internal/gateway/handler_tools.go\"]"}`
	got := coerceStringifiedArgs(json.RawMessage(in), fields)

	var decoded struct {
		ToolAllowlistJSON string   `json:"tool_allowlist_json"`
		TouchesFiles      []string `json:"touches_files"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("coerced args no longer decode: %v (%s)", err, got)
	}
	if decoded.ToolAllowlistJSON != delegationAllowlistJSON {
		t.Errorf("tool_allowlist_json = %q, want %q", decoded.ToolAllowlistJSON, delegationAllowlistJSON)
	}
	if len(decoded.TouchesFiles) != 1 || decoded.TouchesFiles[0] != "internal/gateway/handler_tools.go" {
		t.Errorf("touches_files = %#v, want the stringified array coerced to a real array", decoded.TouchesFiles)
	}
}

// TestHandleToolsCallKeepsDelegateWorkerAllowlistAsString is the end-to-end
// proof: dispatch mcpx__delegate_worker through tools/call and assert the
// DelegationInput decoder never sees an array where it declared a string.
func TestHandleToolsCallKeepsDelegateWorkerAllowlistAsString(t *testing.T) {
	h := newDelegationCapableHandler(t, &mockToolLister{tools: map[string]json.RawMessage{}}, nil)

	params, err := json.Marshal(CallToolRequest{
		Name: "mcpx__delegate_worker",
		Arguments: json.RawMessage(`{
			"objective": "audit the coercion path",
			"tool_allowlist_json": "[\"mcpx__execute_code\",\"index__symbols\"]"
		}`),
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	result, rpcErr := h.handleToolsCall(context.Background(), params)
	body := rpcErrorOrResultText(t, result, rpcErr)
	if strings.Contains(body, "tool_allowlist_json") &&
		strings.Contains(body, "cannot unmarshal") {
		t.Fatalf("delegate_worker still mangles tool_allowlist_json into an array: %s", body)
	}
	// Anything that decodes past the DelegationInput unmarshal is a pass: the
	// run then fails on real delegation preconditions (no provider/model wired
	// in this harness), which is a different, expected failure.
	if strings.Contains(body, "DelegationInput") {
		t.Fatalf("unexpected DelegationInput decode failure: %s", body)
	}
}

// TestToolInputStringFieldsBuiltinWithoutSchema covers the two ways a builtin
// can have no resolvable schema. Both must fall back to legacy coercion
// (nil map) rather than panic or invent fields.
func TestToolInputStringFieldsBuiltinWithoutSchema(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		withDelegator bool
		serverID      string
		tool          string
	}{
		{
			name:          "delegation disabled leaves delegate_worker unresolvable",
			withDelegator: false,
			serverID:      "mcpx-builtin",
			tool:          "delegate_worker",
		},
		{
			name:          "unknown builtin tool",
			withDelegator: true,
			serverID:      "mcpx-builtin",
			tool:          "no_such_builtin_tool",
		},
		{
			name:          "empty tool name",
			withDelegator: true,
			serverID:      "mcpx-builtin",
			tool:          "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h *handler
			if tc.withDelegator {
				h = newDelegationCapableHandler(t, &mockToolLister{tools: map[string]json.RawMessage{}}, nil)
			} else {
				h, _ = newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
			}
			if got := h.toolInputStringFields(ctx, tc.serverID, tc.tool); got != nil {
				t.Fatalf("got %v, want nil (legacy coercion fallback)", got)
			}
		})
	}
}

// TestToolInputStringFieldsDownstreamCatalogUnchanged pins that the non-builtin
// branch still resolves from the cached downstream tools/list catalog. The
// builtin fix must not have diverted it.
func TestToolInputStringFieldsDownstreamCatalogUnchanged(t *testing.T) {
	ctx := context.Background()
	catalog := json.RawMessage(`{"tools":[{"name":"create_view","inputSchema":{"type":"object","properties":{` +
		`"elements":{"type":"string"},"tags":{"type":"array"},"nullable_name":{"type":["string","null"]}}}}]}`)
	lister := &mockToolLister{tools: map[string]json.RawMessage{"excalidraw": catalog}}
	h, _ := newTestHandler(lister, []store.DownstreamServer{{
		ID: "excalidraw", Name: "Excalidraw", Transport: "stdio",
		ToolNamespace: "excalidraw", Discovery: "static",
		CapabilitiesCache: catalog,
	}})

	fields := h.toolInputStringFields(ctx, "excalidraw", "create_view")
	if !fields["elements"] {
		t.Fatalf("elements must resolve as a string field from the downstream catalog: %v", fields)
	}
	if !fields["nullable_name"] {
		t.Errorf(`type ["string","null"] must still resolve as a string field: %v`, fields)
	}
	if fields["tags"] {
		t.Errorf("tags is type array and must remain coercible")
	}

	// An unknown tool on a known downstream server still falls back to legacy.
	if got := h.toolInputStringFields(ctx, "excalidraw", "no_such_tool"); got != nil {
		t.Errorf("unknown downstream tool = %v, want nil", got)
	}
}

// TestCodeExecuteInputSchemaMirrorMatchesDefinition guards the one piece of
// duplication the split introduced. builtinStringFields resolves
// mcpx__execute_code from a types-only mirror rather than building the real
// definition (whose description costs a full downstream introspection), so the
// mirror must declare exactly the same string fields as the live schema.
func TestCodeExecuteInputSchemaMirrorMatchesDefinition(t *testing.T) {
	ctx := context.Background()
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)

	execTool, _ := h.buildCodeExecuteTool(ctx)
	if execTool.Name != codeExecuteToolName {
		t.Fatalf("execute_code tool name = %q, want %q", execTool.Name, codeExecuteToolName)
	}
	want := stringFieldsFromInputSchema(execTool.InputSchema)
	got := stringFieldsFromInputSchema(json.RawMessage(codeExecuteInputSchema))
	if len(want) != len(got) {
		t.Fatalf("mirror declares %v, live schema declares %v", got, want)
	}
	for field := range want {
		if !got[field] {
			t.Errorf("live schema declares string field %q that the mirror is missing", field)
		}
	}
	// And the resolution the dispatch path actually performs agrees.
	viaHandler := h.builtinStringFields(ctx, codeExecuteToolName)
	if !viaHandler["code"] {
		t.Errorf("builtinStringFields(%q) = %v, want code as a string field", codeExecuteToolName, viaHandler)
	}
}

// TestBuiltinCoercionDoesNotIntrospectDownstreams pins the property the
// preflight tests depend on: resolving a builtin's input schema must not
// trigger a downstream tools/list. Building execute_code's description does
// introspect, which is why the dispatch path must not build it.
func TestBuiltinCoercionDoesNotIntrospectDownstreams(t *testing.T) {
	ctx := context.Background()
	lister := &mockToolLister{tools: map[string]json.RawMessage{
		"gh-server": toolsJSON(Tool{Name: "create_issue"}),
	}}
	h := newDelegationCapableHandler(t, lister, []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	})

	for _, tool := range []string{"execute_code", "delegate_worker", "search_tools"} {
		if fields := h.toolInputStringFields(ctx, "mcpx-builtin", tool); fields == nil && tool != "search_tools" {
			t.Errorf("%s resolved no string fields", tool)
		}
	}
	if len(lister.listRequests) != 0 {
		t.Fatalf("builtin schema resolution introspected downstreams: %v", lister.listRequests)
	}
}

// mustJSONString encodes s as a JSON string literal.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return string(raw)
}

// rpcErrorOrResultText flattens the two ways handleToolsCall reports a problem
// (an RPC fault, or an isError tool result) into one string to assert on.
func rpcErrorOrResultText(t *testing.T, result json.RawMessage, rpcErr *RPCError) string {
	t.Helper()
	if rpcErr != nil {
		return rpcErr.Message
	}
	if len(result) == 0 {
		return ""
	}
	var parsed CallToolResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return string(result)
	}
	parts := make([]string, 0, len(parsed.Content))
	for _, c := range parsed.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}
