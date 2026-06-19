package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProjectTextValue_Shape(t *testing.T) {
	got := projectTextValue("hello world")
	if got["kind"] != TextProjectionKind {
		t.Fatalf("kind = %v, want %q", got["kind"], TextProjectionKind)
	}
	if got["text"] != "hello world" {
		t.Fatalf("text = %v", got["text"])
	}
	if got["bytes"] != 11 {
		t.Fatalf("bytes = %v, want 11", got["bytes"])
	}
}

func TestParseToolResultValue_ProjectsPlainText(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"Found 2 match(es).\n\n1. foo"}]}`
	got, errText := parseToolResultValue(json.RawMessage(raw))
	if errText != "" {
		t.Fatalf("unexpected error: %s", errText)
	}
	m, ok := got.(map[string]any)
	if !ok || !isTextProjection(m) {
		t.Fatalf("want text projection map, got %T %#v", got, got)
	}
	if m["bytes"] != len("Found 2 match(es).\n\n1. foo") {
		t.Fatalf("bytes mismatch: %v", m["bytes"])
	}
}

func TestShapeHint_TextProjection(t *testing.T) {
	hint := shapeHint(projectTextValue(strings.Repeat("x", 50)))
	if !strings.Contains(hint, "bytes=50") {
		t.Fatalf("want bytes in hint, got %q", hint)
	}
	if strings.Contains(hint, "0,") {
		t.Fatalf("numeric string indexes leaked into hint: %q", hint)
	}
}

func TestCompactValue_WrapsString(t *testing.T) {
	got := compactValue("plain prose")
	m, ok := got.(map[string]any)
	if !ok || !isTextProjection(m) {
		t.Fatalf("want projected map, got %T %#v", got, got)
	}
}

func TestSandbox_TextToolPrintSmallIsPlain(t *testing.T) {
	// Sub-1KB text projections should print as plain text, not wrapped in
	// the {kind:text, bytes:N, preview:"..."} envelope.
	small := strings.Repeat("a", 400) + "\nline2"
	caller := newMockCaller()
	caller.responses["mesh__hydrate"] = json.RawMessage(
		`{"content":[{"type":"text","text":` + string(mustMarshal(small)) + `}]}`,
	)
	tools := []ToolDef{{Name: "mesh__hydrate", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}

	sb := NewSandbox(caller, 5*time.Second)
	result, err := sb.Execute(context.Background(), `
const msg = mesh.hydrate({message_id: "m1"});
print("keys", Object.keys(msg).join(","));
print(msg);
`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected execution error: %s", result.Error)
	}
	// Small text should NOT be wrapped in the envelope
	if strings.Contains(result.Output, "kind:text") {
		t.Fatalf("small text projection should not use envelope, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "keys kind,text,bytes") {
		t.Fatalf("expected projected object keys before print formatting, got:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "keys 0") {
		t.Fatalf("numeric string indexes leaked:\n%s", result.Output)
	}
	// Small text should include the full content
	if !strings.Contains(result.Output, strings.Repeat("a", 350)) {
		t.Fatalf("small text projection should include full content, got:\n%s", result.Output)
	}
}

func TestSandbox_TextToolPrintLargeUsesEnvelope(t *testing.T) {
	// Text projections over textProjectionEnvelopeMin should use the envelope
	// so the agent sees a bounded preview and can hydrate for more.
	large := strings.Repeat("b", 1200) + "\nline2"
	caller := newMockCaller()
	caller.responses["mesh__hydrate"] = json.RawMessage(
		`{"content":[{"type":"text","text":` + string(mustMarshal(large)) + `}]}`,
	)
	tools := []ToolDef{{Name: "mesh__hydrate", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}

	sb := NewSandbox(caller, 5*time.Second)
	result, err := sb.Execute(context.Background(), `
const msg = mesh.hydrate({message_id: "m1"});
print(msg);
`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected execution error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "kind:text") {
		t.Fatalf("large text projection should use envelope, got:\n%s", result.Output)
	}
	if strings.Contains(result.Output, strings.Repeat("b", 1100)) {
		t.Fatalf("full payload should not flood print output:\n%s", result.Output)
	}
}

func TestSandbox_SkillSearchStructuredHits(t *testing.T) {
	payload := map[string]any{
		"query": "pdf extract",
		"count": 1,
		"hits": []map[string]any{{
			"name": "pdf-text", "version": 2, "description": "extract pdf",
			"score": 0.91, "scope": "global",
		}},
		"hint": "Fetch a full body with mcpx__skill_get({name}).",
	}
	data, _ := json.Marshal(payload)
	caller := newMockCaller()
	caller.responses["mcpx__skill_search"] = json.RawMessage(
		`{"content":[{"type":"text","text":` + string(mustMarshal(string(data))) + `}]}`,
	)
	tools := []ToolDef{{Name: "mcpx__skill_search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}

	sb := NewSandbox(caller, 5*time.Second)
	result, err := sb.Execute(context.Background(), `
const hits = mcpx.skill_search({query: "pdf extract"});
print("keys", Object.keys(hits).join(","));
print("first", hits.hits[0].name, hits.count);
`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected execution error: %s", result.Error)
	}
	for _, key := range []string{"query", "count", "hits", "hint"} {
		if !strings.Contains(result.Output, key) {
			t.Fatalf("expected key %q in output:\n%s", key, result.Output)
		}
	}
	if !strings.Contains(result.Output, "first pdf-text 1") {
		t.Fatalf("expected hit drill-down, got:\n%s", result.Output)
	}
}
