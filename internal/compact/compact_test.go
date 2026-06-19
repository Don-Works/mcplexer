package compact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactToolResult(t *testing.T) {
	c := New()
	tests := []struct {
		name    string
		input   string
		checkFn func(t *testing.T, result json.RawMessage)
	}{
		{
			name:  "error result untouched",
			input: `{"isError":true,"content":[{"type":"text","text":"[{\"id\":1},{\"id\":2},{\"id\":3}]"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				var env struct {
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(result, &env); err != nil {
					t.Fatal(err)
				}
				if !env.IsError {
					t.Error("isError must be preserved")
				}
				// Content should be identical (not compacted).
				if !strings.Contains(string(result), `"id\":1`) {
					t.Error("error content should be untouched")
				}
			},
		},
		{
			name:  "error result with isError false explicit",
			input: `{"isError":false,"content":[{"type":"text","text":"{\"a\":null}"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if strings.Contains(text, "null") {
					t.Error("null should be pruned when isError is false")
				}
			},
		},
		{
			name:  "non-JSON text passthrough",
			input: `{"content":[{"type":"text","text":"hello world, this is plain text"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if text != "hello world, this is plain text" {
					t.Errorf("plain text modified: %s", text)
				}
			},
		},
		{
			name:  "JSON number in text passthrough",
			input: `{"content":[{"type":"text","text":"42"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if text != "42" {
					t.Errorf("number text modified: %s", text)
				}
			},
		},
		{
			name:  "JSON string in text passthrough",
			input: `{"content":[{"type":"text","text":"\"just a string\""}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if text != `"just a string"` {
					t.Errorf("string text modified: %s", text)
				}
			},
		},
		{
			name:  "JSON array compacted to columnar",
			input: `{"content":[{"type":"text","text":"[{\"id\":1,\"name\":\"a\",\"x\":null},{\"id\":2,\"name\":\"b\",\"x\":null},{\"id\":3,\"name\":\"c\",\"x\":null}]"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				var parsed map[string]any
				if err := json.Unmarshal([]byte(text), &parsed); err != nil {
					t.Fatalf("compacted text not valid JSON: %v\n%s", err, text)
				}
				if _, ok := parsed["_cols"]; !ok {
					t.Error("expected _cols in columnar output")
				}
				if _, ok := parsed["_rows"]; !ok {
					t.Error("expected _rows in columnar output")
				}
				if strings.Contains(text, `"x"`) {
					t.Error("null-only column 'x' should be pruned")
				}
			},
		},
		{
			name:  "JSON object pruned",
			input: `{"content":[{"type":"text","text":"{\"id\":1,\"name\":\"test\",\"empty\":\"\",\"nil_field\":null,\"nested\":{\"a\":1,\"b\":null}}"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if strings.Contains(text, "empty") {
					t.Error("empty string field should be pruned")
				}
				if strings.Contains(text, "nil_field") {
					t.Error("null field should be pruned")
				}
				if strings.Contains(text, `"b"`) {
					t.Error("nested null should be pruned")
				}
				// Preserved fields.
				if !strings.Contains(text, `"id"`) || !strings.Contains(text, `"name"`) {
					t.Error("non-empty fields must be preserved")
				}
			},
		},
		{
			name:  "image content untouched",
			input: `{"content":[{"type":"image","data":"base64data=="}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				if !strings.Contains(string(result), "base64data==") {
					t.Error("image content modified")
				}
			},
		},
		{
			name:  "mixed content text plus image",
			input: `{"content":[{"type":"text","text":"{\"id\":1,\"junk\":null}"},{"type":"image","data":"abc"}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				var env struct {
					Content []map[string]any `json:"content"`
				}
				if err := json.Unmarshal(result, &env); err != nil {
					t.Fatal(err)
				}
				if len(env.Content) != 2 {
					t.Fatalf("expected 2 content items, got %d", len(env.Content))
				}
				// Text item should be compacted.
				text := env.Content[0]["text"].(string)
				if strings.Contains(text, "junk") {
					t.Error("null field in text should be pruned")
				}
				// Image item untouched.
				if env.Content[1]["data"] != "abc" {
					t.Error("image data modified")
				}
			},
		},
		{
			name:  "empty text field skipped",
			input: `{"content":[{"type":"text","text":""}]}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				text := extractText(t, result)
				if text != "" {
					t.Error("empty text should remain empty")
				}
			},
		},
		{
			name:  "extra envelope fields preserved",
			input: `{"content":[{"type":"text","text":"{\"a\":null}"}],"_meta":{"cache_hit":true}}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				if !strings.Contains(string(result), `"cache_hit"`) {
					t.Error("_meta envelope field should be preserved")
				}
			},
		},
		{
			name:  "no content field passthrough",
			input: `{"something":"else"}`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				if string(result) != `{"something":"else"}` {
					t.Errorf("no-content result modified: %s", result)
				}
			},
		},
		{
			name:  "invalid JSON passthrough",
			input: `not json at all`,
			checkFn: func(t *testing.T, result json.RawMessage) {
				if string(result) != "not json at all" {
					t.Error("invalid JSON modified")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.CompactToolResult(json.RawMessage(tt.input))
			tt.checkFn(t, result)
		})
	}
}

func TestCompactJSON(t *testing.T) {
	c := New()
	tests := []struct {
		name    string
		input   string
		checkFn func(t *testing.T, output []byte)
	}{
		{
			name:  "primitive number",
			input: `42`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "42" {
					t.Errorf("got %s", out)
				}
			},
		},
		{
			name:  "primitive string",
			input: `"hello"`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != `"hello"` {
					t.Errorf("got %s", out)
				}
			},
		},
		{
			name:  "primitive bool",
			input: `true`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "true" {
					t.Errorf("got %s", out)
				}
			},
		},
		{
			name:  "primitive null",
			input: `null`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "null" {
					t.Errorf("got %s", out)
				}
			},
		},
		{
			name:  "invalid JSON",
			input: `{broken`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "{broken" {
					t.Error("invalid JSON should pass through")
				}
			},
		},
		{
			name:  "object pruning",
			input: `{"id":1,"x":null,"y":""}`,
			checkFn: func(t *testing.T, out []byte) {
				if strings.Contains(string(out), "x") || strings.Contains(string(out), "y") {
					t.Errorf("null/empty not pruned: %s", out)
				}
			},
		},
		{
			name:  "array of primitives unchanged",
			input: `[1,2,3]`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "[1,2,3]" {
					t.Errorf("primitive array modified: %s", out)
				}
			},
		},
		{
			name:  "array of strings unchanged",
			input: `["a","b","c"]`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != `["a","b","c"]` {
					t.Errorf("string array modified: %s", out)
				}
			},
		},
		{
			name:  "mixed array objects and primitives",
			input: `[1,{"a":null},true]`,
			checkFn: func(t *testing.T, out []byte) {
				// Object should be pruned, rest unchanged.
				var arr []any
				if err := json.Unmarshal(out, &arr); err != nil {
					t.Fatal(err)
				}
				if len(arr) != 3 {
					t.Fatalf("expected 3 items, got %d", len(arr))
				}
			},
		},
		{
			name:  "small object array no columnar",
			input: `[{"id":1},{"id":2}]`,
			checkFn: func(t *testing.T, out []byte) {
				// <3 items, should stay as array.
				var arr []any
				if err := json.Unmarshal(out, &arr); err != nil {
					t.Fatalf("should be array: %v", err)
				}
			},
		},
		{
			name:  "empty object",
			input: `{}`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "{}" {
					t.Errorf("empty object modified: %s", out)
				}
			},
		},
		{
			name:  "empty array",
			input: `[]`,
			checkFn: func(t *testing.T, out []byte) {
				if string(out) != "[]" {
					t.Errorf("empty array modified: %s", out)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := c.CompactJSON([]byte(tt.input))
			tt.checkFn(t, out)
		})
	}
}

func TestCompactJSONIdempotent(t *testing.T) {
	c := New()
	input := `[{"id":1,"name":"a","x":null},{"id":2,"name":"b","x":null},{"id":3,"name":"c","x":null}]`
	first := c.CompactJSON([]byte(input))
	second := c.CompactJSON(first)
	if string(first) != string(second) {
		t.Errorf("double compaction changed output:\n  first:  %s\n  second: %s", first, second)
	}
}

func extractText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var env struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Content) == 0 {
		t.Fatal("no content items")
	}
	text, _ := env.Content[0]["text"].(string)
	return text
}

func assertJSONEqual(t *testing.T, want, got any) {
	t.Helper()
	wj, _ := json.Marshal(want)
	gj, _ := json.Marshal(got)
	if string(wj) != string(gj) {
		t.Errorf("mismatch:\n  want: %s\n  got:  %s", wj, gj)
	}
}
