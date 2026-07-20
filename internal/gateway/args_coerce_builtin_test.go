package gateway

import (
	"encoding/json"
	"testing"
)

// TestStringFieldsForBuiltinTool_DelegateWorker pins the actual live failure:
// mcpx__delegate_worker is a builtin, so its schema is never in the downstream
// catalog. Resolving it against the in-process definitions is what keeps
// tool_allowlist_json a string through dispatch.
func TestStringFieldsForBuiltinTool_DelegateWorker(t *testing.T) {
	fields := stringFieldsForBuiltinTool(delegationToolDefinitions(), "mcpx__delegate_worker")
	if !fields["tool_allowlist_json"] {
		t.Fatalf("tool_allowlist_json must resolve as a string field; got %v", fields)
	}
	if fields["touches_files"] {
		t.Errorf("touches_files is type array and must remain coercible")
	}

	in := `{"tool_allowlist_json":"[\"mcpx__execute_code\",\"index__symbols\"]"}`
	got := coerceStringifiedArgs(json.RawMessage(in), fields)
	assertJSONEqual(t, got, in)
}

func TestStringFieldsForBuiltinTool_Lookup(t *testing.T) {
	tools := []Tool{
		{Name: "mcpx__delegate_worker", InputSchema: json.RawMessage(`{"properties":{"a":{"type":"string"}}}`)},
		{Name: "mesh__send", InputSchema: json.RawMessage(`{"properties":{"body":{"type":"string"}}}`)},
		{Name: "email__send", InputSchema: json.RawMessage(`{"properties":{"attachments":{"type":"array"}}}`)},
	}
	cases := []struct {
		name     string
		toolName string
		wantKey  string
		wantNil  bool
	}{
		{name: "exact match", toolName: "mesh__send", wantKey: "body"},
		{name: "unknown tool", toolName: "mcpx__nope", wantNil: true},
		{name: "empty name", toolName: "", wantNil: true},
		{name: "bare suffix never cross-matches", toolName: "send", wantNil: true},
		{name: "no string fields", toolName: "email__send", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringFieldsForBuiltinTool(tools, tc.toolName)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !got[tc.wantKey] {
				t.Fatalf("got %v, want key %q", got, tc.wantKey)
			}
		})
	}
}

func TestBuiltinToolFullName(t *testing.T) {
	cases := []struct {
		name     string
		serverID string
		tool     string
		want     string
	}{
		{name: "mcpx builtin", serverID: "mcpx-builtin", tool: "delegate_worker", want: "mcpx__delegate_worker"},
		{name: "mesh builtin", serverID: "mesh-builtin", tool: "send", want: "mesh__send"},
		{name: "already namespaced", serverID: "mcpx-builtin", tool: "mcpx__delegate_worker", want: "mcpx__delegate_worker"},
		{name: "non-builtin server", serverID: "excalidraw", tool: "create_view", want: ""},
		{name: "empty tool", serverID: "mcpx-builtin", tool: "", want: ""},
		{name: "bare suffix only", serverID: "-builtin", tool: "x", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := builtinToolFullName(tc.serverID, tc.tool); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
