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
