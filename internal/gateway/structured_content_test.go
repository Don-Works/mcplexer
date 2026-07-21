package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/codemode"
)

// TestSurfaceStructuredContent_JSONObjectLifted is the headline H3 case:
// a single text content block whose body is JSON.stringify(obj) — exactly
// what a downstream MCP tool returns when shipping a structured payload —
// gets the parsed object lifted into the top-level structuredContent slot.
// The text stays intact for backward-compat with clients that haven't
// implemented the structuredContent field.
func TestSurfaceStructuredContent_JSONObjectLifted(t *testing.T) {
	in := json.RawMessage(`{"content":[{"type":"text","text":"{\"url\":\"http://x\",\"rowCount\":42}"}],"isError":false}`)
	out := surfaceStructuredContent(in)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal lifted result: %v", err)
	}
	raw, ok := envelope["structuredContent"]
	if !ok {
		t.Fatalf("structuredContent missing; got %s", string(out))
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("structuredContent not an object: %v (raw=%s)", err, string(raw))
	}
	if got["url"] != "http://x" {
		t.Errorf("structuredContent.url = %v, want http://x", got["url"])
	}
	if got["rowCount"].(float64) != 42 {
		t.Errorf("structuredContent.rowCount = %v, want 42", got["rowCount"])
	}
}

// TestSurfaceStructuredContent_BigIntsExact is the F1 number-safety
// regression: int64 values beyond float64's 2^53 integer precision must
// survive the lift with their exact digits. A float64 round-trip would turn
// 9223372036854775807 into 9223372036854776000 — and on harnesses that
// forward only structuredContent to the model (Claude Code CLI), the lifted
// copy is the ONLY copy the model reads.
func TestSurfaceStructuredContent_BigIntsExact(t *testing.T) {
	in := json.RawMessage(`{"content":[{"type":"text","text":"{\"id\":9223372036854775807,\"snowflake\":1234567890123456789,\"small\":42}"}],"isError":false}`)
	out := surfaceStructuredContent(in)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal lifted result: %v", err)
	}
	raw, ok := envelope["structuredContent"]
	if !ok {
		t.Fatalf("structuredContent missing; got %s", string(out))
	}
	for _, digits := range []string{"9223372036854775807", "1234567890123456789"} {
		if !strings.Contains(string(raw), digits) {
			t.Errorf("structuredContent lost exact integer %s: %s", digits, string(raw))
		}
	}
}

// TestSurfaceStructuredContent_TrailingGarbageNotLifted guards the Decoder
// switch: "{} extra" is not a clean JSON document and must not be lifted.
func TestSurfaceStructuredContent_TrailingGarbageNotLifted(t *testing.T) {
	in := json.RawMessage(`{"content":[{"type":"text","text":"{\"a\":1} trailing prose"}],"isError":false}`)
	out := surfaceStructuredContent(in)
	if strings.Contains(string(out), "structuredContent") {
		t.Fatalf("text with trailing garbage was lifted: %s", string(out))
	}
}

// TestSurfaceStructuredContent_JSONArrayLifted covers the array variant
// — many task__list / mesh__list_* shapes return a JSON array, and
// those should be liftable too.
func TestSurfaceStructuredContent_JSONArrayLifted(t *testing.T) {
	in := json.RawMessage(`{"content":[{"type":"text","text":"[1,2,3]"}],"isError":false}`)
	out := surfaceStructuredContent(in)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("unmarshal lifted result: %v", err)
	}
	raw, ok := envelope["structuredContent"]
	if !ok {
		t.Fatalf("structuredContent missing; got %s", string(out))
	}
	if string(raw) != `[1,2,3]` {
		t.Errorf("structuredContent = %s, want [1,2,3]", string(raw))
	}
}

// TestSurfaceStructuredContent_TextPreserved verifies backward-compat:
// after lifting, the original text content block is byte-identical to
// what came in. Clients that haven't implemented structuredContent
// fall back to JSON.parse(content[0].text) as before.
func TestSurfaceStructuredContent_TextPreserved(t *testing.T) {
	original := `{"key":"value"}`
	in := mustMarshal(t, CallToolResult{
		Content: []ToolContent{{Type: "text", Text: original}},
	})
	out := surfaceStructuredContent(in)

	var result CallToolResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(result.Content))
	}
	if result.Content[0].Text != original {
		t.Errorf("text mutated: got %q, want %q", result.Content[0].Text, original)
	}
}

// TestSurfaceStructuredContent_PassThroughCases pins the negative paths
// — surfaceStructuredContent must NEVER touch a result that doesn't
// match the "exactly one text block, parseable as a JSON container,
// not enveloped, not an error" pattern.
func TestSurfaceStructuredContent_PassThroughCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "error envelope preserved",
			in:   `{"content":[{"type":"text","text":"{\"a\":1}"}],"isError":true}`,
		},
		{
			name: "plain prose text not lifted",
			in:   `{"content":[{"type":"text","text":"hello world"}],"isError":false}`,
		},
		{
			name: "JSON scalar (number) not lifted",
			in:   `{"content":[{"type":"text","text":"42"}],"isError":false}`,
		},
		{
			name: "JSON scalar (string) not lifted",
			in:   `{"content":[{"type":"text","text":"\"hello\""}],"isError":false}`,
		},
		{
			name: "envelope-wrapped content not lifted",
			in:   `{"content":[{"type":"text","text":"<untrusted-content source=\"x\" trust=\"low\">{\"a\":1}</untrusted-content>"}],"isError":false}`,
		},
		{
			name: "multiple content blocks not lifted",
			in:   `{"content":[{"type":"text","text":"{\"a\":1}"},{"type":"text","text":"notice"}],"isError":false}`,
		},
		{
			name: "non-text content not lifted",
			in:   `{"content":[{"type":"image","data":"…"}],"isError":false}`,
		},
		{
			name: "already-set structuredContent not overwritten",
			in:   `{"content":[{"type":"text","text":"{\"a\":2}"}],"structuredContent":{"a":1},"isError":false}`,
		},
		{
			name: "empty content array not lifted",
			in:   `{"content":[],"isError":false}`,
		},
		{
			name: "malformed envelope ignored",
			in:   `not json`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := json.RawMessage(c.in)
			out := surfaceStructuredContent(before)

			// For the already-set case, the input value must survive verbatim.
			if c.name == "already-set structuredContent not overwritten" {
				var env map[string]json.RawMessage
				if err := json.Unmarshal(out, &env); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				var sc map[string]any
				if err := json.Unmarshal(env["structuredContent"], &sc); err != nil {
					t.Fatalf("unmarshal structuredContent: %v", err)
				}
				if sc["a"].(float64) != 1 {
					t.Errorf("structuredContent overwritten: got %v, want {a:1}", sc)
				}
				return
			}

			// For every other pass-through case, the output must NOT contain
			// a structuredContent field.
			if strings.Contains(string(out), `"structuredContent"`) {
				t.Errorf("expected pass-through (no structuredContent), got %s", string(out))
			}
		})
	}
}

// TestSurfaceStructuredContent_NoHTMLEntitiesInLiftedValue is the H1
// regression pin: lifted structuredContent must contain raw JSON, never
// HTML-entity-encoded strings. The whole point of the H1 + H3 combo is
// that the calling LLM reads `result.structuredContent.url` and gets
// the actual URL — not `&#34;http://...&#34;` it has to decode twice.
func TestSurfaceStructuredContent_NoHTMLEntitiesInLiftedValue(t *testing.T) {
	in := json.RawMessage(`{"content":[{"type":"text","text":"{\"url\":\"http://example.com/?q=a&b\",\"quote\":\"she said \\\"hi\\\"\"}"}],"isError":false}`)
	out := surfaceStructuredContent(in)

	// Lifted bytes must not contain HTML entities — they're raw JSON.
	if strings.Contains(string(out), "&#") || strings.Contains(string(out), "&amp;") ||
		strings.Contains(string(out), "&lt;") || strings.Contains(string(out), "&gt;") {
		t.Errorf("lifted value contains HTML entities: %s", string(out))
	}

	var env struct {
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.StructuredContent["quote"] != `she said "hi"` {
		t.Errorf("quote not preserved: %v", env.StructuredContent["quote"])
	}
	if env.StructuredContent["url"] != "http://example.com/?q=a&b" {
		t.Errorf("url not preserved: %v", env.StructuredContent["url"])
	}
}

func TestSurfaceStructuredContent_LargeJSONNotLifted(t *testing.T) {
	text := `{"payload":"` + strings.Repeat("x", codemode.DefaultMaxOutputBytes) + `"}`
	in := mustMarshal(t, CallToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	})

	out := surfaceStructuredContent(in)
	if strings.Contains(string(out), `"structuredContent"`) {
		t.Fatalf("large payload was lifted into structuredContent")
	}
}
