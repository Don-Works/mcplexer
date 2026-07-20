package gateway

import (
	"encoding/json"
	"testing"
)

func TestCoerceStringifiedArgs_RespectsStringSchema(t *testing.T) {
	// Excalidraw create_view declares `elements: string` (JSON-array-string).
	// Without schema awareness the coercer would parse it back into an array
	// before forwarding, and the downstream MCP would reject the call.
	in := `{"elements":"[{\"type\":\"text\"}]"}`
	stringFields := map[string]bool{"elements": true}
	got := coerceStringifiedArgs(json.RawMessage(in), stringFields)

	var gotMap map[string]any
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if s, ok := gotMap["elements"].(string); !ok || s != `[{"type":"text"}]` {
		t.Errorf("elements should remain a string; got %T %v", gotMap["elements"], gotMap["elements"])
	}
}

func TestCoerceStringifiedArgs_LeadingWhitespacePreservesJSONString(t *testing.T) {
	in := `{"tool_allowlist_json":" [\"mcpx__execute_code\"]"}`
	got := coerceStringifiedArgs(json.RawMessage(in), nil)

	var gotMap map[string]any
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	value, ok := gotMap["tool_allowlist_json"].(string)
	if !ok || value != ` ["mcpx__execute_code"]` {
		t.Fatalf("JSON-string allowlist should remain a string; got %T %v", gotMap["tool_allowlist_json"], gotMap["tool_allowlist_json"])
	}
}

func TestStringFieldsFromInputSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   map[string]bool
	}{
		{
			name:   "single string field",
			schema: `{"type":"object","properties":{"elements":{"type":"string"}}}`,
			want:   map[string]bool{"elements": true},
		},
		{
			name:   "mixed types",
			schema: `{"properties":{"name":{"type":"string"},"count":{"type":"integer"},"filters":{"type":"object"}}}`,
			want:   map[string]bool{"name": true},
		},
		{
			name:   "nullable string",
			schema: `{"properties":{"id":{"type":["string","null"]}}}`,
			want:   map[string]bool{"id": true},
		},
		{
			name:   "union with non-null other",
			schema: `{"properties":{"id":{"type":["string","number"]}}}`,
			want:   nil,
		},
		{
			name:   "no properties",
			schema: `{}`,
			want:   nil,
		},
		{
			name:   "invalid schema",
			schema: ``,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringFieldsFromInputSchema(json.RawMessage(tc.schema))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// TestCoerceStringifiedArgs_SchemaTypeMatrix is the regression matrix for the
// delegate_worker allowlist bug: a field declared type: "string" must survive
// coercion verbatim, while genuinely array/object-typed fields receiving
// stringified JSON must still be parsed. Whitespace variants are included
// because Go's json.Unmarshal skips leading whitespace, which is why a single
// leading space accidentally made the broken call succeed.
func TestCoerceStringifiedArgs_SchemaTypeMatrix(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"tool_allowlist_json":{"type":"string"},
			"blob":{"type":"string"},
			"note":{"type":"string"},
			"ids":{"type":"array","items":{"type":"string"}},
			"filters":{"type":"object"},
			"count":{"type":"integer"}
		}
	}`)
	stringFields := stringFieldsFromInputSchema(schema)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "string-typed field with array value stays a string",
			in:   `{"tool_allowlist_json":"[\"mcpx__execute_code\",\"index__symbols\"]"}`,
			want: `{"tool_allowlist_json":"[\"mcpx__execute_code\",\"index__symbols\"]"}`,
		},
		{
			name: "string-typed field with object value stays a string",
			in:   `{"blob":"{\"nested\":true}"}`,
			want: `{"blob":"{\"nested\":true}"}`,
		},
		{
			name: "string-typed field with leading whitespace stays a string",
			in:   `{"tool_allowlist_json":" [\"mcpx__execute_code\"]"}`,
			want: `{"tool_allowlist_json":" [\"mcpx__execute_code\"]"}`,
		},
		{
			name: "array-typed field with stringified array still coerces",
			in:   `{"ids":"[\"a\",\"b\"]"}`,
			want: `{"ids":["a","b"]}`,
		},
		{
			name: "object-typed field with stringified object still coerces",
			in:   `{"filters":"{\"asset_types\":[\"chat_message\"]}"}`,
			want: `{"filters":{"asset_types":["chat_message"]}}`,
		},
		{
			name: "array-typed field with leading whitespace is not coerced",
			in:   `{"ids":" [\"a\"]"}`,
			want: `{"ids":" [\"a\"]"}`,
		},
		{
			name: "non-JSON string on a string-typed field is untouched",
			in:   `{"note":"hello world"}`,
			want: `{"note":"hello world"}`,
		},
		{
			name: "non-JSON bracketed string on an array-typed field is untouched",
			in:   `{"ids":"[not valid json"}`,
			want: `{"ids":"[not valid json"}`,
		},
		{
			name: "mixed payload coerces only the non-string fields",
			in:   `{"tool_allowlist_json":"[\"a\"]","ids":"[1,2]","count":3}`,
			want: `{"tool_allowlist_json":"[\"a\"]","ids":[1,2],"count":3}`,
		},
		{
			name: "field absent from schema keeps legacy coercion",
			in:   `{"unknown":"[1,2]"}`,
			want: `{"unknown":[1,2]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceStringifiedArgs(json.RawMessage(tt.in), stringFields)
			assertJSONEqual(t, got, tt.want)
		})
	}
}

// assertJSONEqual compares two JSON payloads structurally, so key ordering
// from map marshalling never makes a test flaky.
func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("unmarshal got %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotJSON, _ := json.Marshal(gotVal)
	wantJSON, _ := json.Marshal(wantVal)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestCoerceStringifiedArgs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "object string to object",
			in:   `{"workspace_id":"123","filters":"{\"asset_types\":[\"chat_message\"]}"}`,
			want: `{"filters":{"asset_types":["chat_message"]},"workspace_id":"123"}`,
		},
		{
			name: "array string to array",
			in:   `{"ids":"[1,2,3]"}`,
			want: `{"ids":[1,2,3]}`,
		},
		{
			name: "no change for plain strings",
			in:   `{"name":"hello","count":42}`,
			want: `{"count":42,"name":"hello"}`,
		},
		{
			name: "no change for already-object values",
			in:   `{"filters":{"key":"val"}}`,
			want: `{"filters":{"key":"val"}}`,
		},
		{
			name: "invalid json string left alone",
			in:   `{"data":"{not valid json}"}`,
			want: `{"data":"{not valid json}"}`,
		},
		{
			name: "empty args",
			in:   `{}`,
			want: `{}`,
		},
		{
			name: "mixed coercion",
			in:   `{"a":"plain","b":"{\"nested\":true}","c":42}`,
			want: `{"a":"plain","b":{"nested":true},"c":42}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceStringifiedArgs(json.RawMessage(tt.in), nil)

			// Compare as parsed JSON to avoid key-order issues.
			var gotMap, wantMap any
			if err := json.Unmarshal(got, &gotMap); err != nil {
				t.Fatalf("unmarshal got: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantMap); err != nil {
				t.Fatalf("unmarshal want: %v", err)
			}

			gotJSON, _ := json.Marshal(gotMap)
			wantJSON, _ := json.Marshal(wantMap)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}
