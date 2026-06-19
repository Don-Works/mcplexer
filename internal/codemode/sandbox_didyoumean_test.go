package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSandbox_NamespaceTypoRaisesDidYouMean asserts a top-level
// `gihub.list_issues({})` typo (namespace doesn't exist) dies as a
// ReferenceError at runtime and the error message is annotated with a
// did-you-mean over the registered namespaces. Without this annotation,
// cheap models see a bare `gihub is not defined` and have no clue what
// the closest real namespace is.
func TestSandbox_NamespaceTypoRaisesDidYouMean(t *testing.T) {
	caller := newMockCaller()
	tools := []ToolDef{
		{Name: "github__list_issues", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "github__get_repo", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "linear__create_issue", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `gihub.list_issues({});`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected ReferenceError")
	}
	if !strings.Contains(result.Error, "Did you mean") {
		t.Fatalf("did-you-mean missing from runtime error: %q", result.Error)
	}
	if !strings.Contains(result.Error, "github") {
		t.Fatalf("expected suggestion of 'github' namespace, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "Available namespaces:") {
		t.Fatalf("expected namespace inventory, got %q", result.Error)
	}
}

// TestSandbox_MemberTypoRaisesDidYouMean asserts that a real namespace
// with a typo'd member (`github.list_isues`) surfaces a TypeError-style
// 'Object has no member' error annotated with a did-you-mean over the
// post-`__` short names rendered as `ns.member`.
func TestSandbox_MemberTypoRaisesDidYouMean(t *testing.T) {
	caller := newMockCaller()
	tools := []ToolDef{
		{Name: "github__list_issues", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "github__get_repo", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `github.list_isues({});`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected member error")
	}
	if !strings.Contains(result.Error, "Did you mean") {
		t.Fatalf("did-you-mean missing from runtime error: %q", result.Error)
	}
	if !strings.Contains(result.Error, "github.list_issues") {
		t.Fatalf("expected dotted suggestion 'github.list_issues', got %q", result.Error)
	}
}

// TestSandbox_PermissionDeniedNotAnnotated is the negative case: a
// downstream "permission denied" is a real failure, NOT a typo, and
// must not have a did-you-mean tacked on (that would mislead the model
// into retrying a different tool when the real fix is upstream).
func TestSandbox_PermissionDeniedNotAnnotated(t *testing.T) {
	caller := newMockCaller()
	caller.errors["secrets__read"] = errors.New("permission denied: write access required")
	tools := []ToolDef{
		{Name: "secrets__read", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "secrets__write", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `secrets.read({});`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected error from failed tool")
	}
	if strings.Contains(result.Error, "Did you mean") {
		t.Fatalf("did-you-mean must not fire on permission denied, got %q", result.Error)
	}
}

// TestSandbox_NoToolsRegistered exercises the empty-toolNames safety
// path: even when the typo regex matches, there's nothing to suggest,
// so the original Goja diagnostic must pass through unchanged.
func TestSandbox_NoToolsRegistered(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `gihub.list_issues({});`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected ReferenceError")
	}
	if strings.Contains(result.Error, "Did you mean") {
		t.Fatalf("no namespaces registered — should not propose suggestion, got %q", result.Error)
	}
}

// TestAnnotateRuntimeError_AvailableNamespacesCap asserts the namespace
// inventory is capped at the first 20 names and announces the overflow.
func TestAnnotateRuntimeError_AvailableNamespacesCap(t *testing.T) {
	tools := make([]string, 30)
	for i := range tools {
		// Unique namespace per entry; the helper sorts + dedupes.
		tools[i] = "ns" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + "__op"
	}

	msg := "ReferenceError: bogus is not defined at <eval>:1:1(0)"
	annotated := annotateRuntimeError(msg, tools)
	if !strings.Contains(annotated, "Available namespaces:") {
		t.Fatalf("expected namespace list, got %q", annotated)
	}
	if !strings.Contains(annotated, "(+10 more)") {
		t.Fatalf("expected overflow note (+10 more), got %q", annotated)
	}
}

// TestBuildToolErrorMessage_DidYouMeanOnNotFound asserts that a tool
// not-found error string activates the did-you-mean over the registered
// tool names. This is the buildToolErrorMessage public contract that
// the parallel() path and the makeToolFunc panic path both rely on.
func TestBuildToolErrorMessage_DidYouMeanOnNotFound(t *testing.T) {
	tools := []string{
		"github__list_issues",
		"github__get_repo",
		"linear__list_issues",
	}

	cases := []struct {
		name     string
		toolName string
		errText  string
		want     string
	}{
		{
			name:     "not found surfaces suggestion",
			toolName: "github__list_issue",
			errText:  "tool not found: github__list_issue",
			want:     "github__list_issues",
		},
		{
			name:     "no matching route surfaces suggestion",
			toolName: "github__list_isue",
			errText:  "no matching route for github__list_isue",
			want:     "github__list_issues",
		},
		{
			name:     "unknown tool surfaces suggestion",
			toolName: "linear__list_issue",
			errText:  "unknown tool: linear__list_issue",
			want:     "linear__list_issues",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			msg := buildToolErrorMessage(
				tc.toolName, json.RawMessage(`{}`), tc.errText, nil, nil, tools,
			)
			if !strings.Contains(msg, "Did you mean") {
				t.Fatalf("expected did-you-mean, got %q", msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("expected suggestion %q in %q", tc.want, msg)
			}
		})
	}
}

// TestBuildToolErrorMessage_NoDidYouMeanForOpaqueErrors is the negative:
// a downstream returning a real domain error like "permission denied"
// or "rate limit exceeded" must NOT be annotated with a did-you-mean —
// the call dispatched fine, the failure is upstream.
func TestBuildToolErrorMessage_NoDidYouMeanForOpaqueErrors(t *testing.T) {
	tools := []string{"github__list_issues", "github__get_repo"}

	cases := []string{
		"permission denied",
		"rate limit exceeded",
		"upstream api 500 internal server error",
		"query failed: column does not exist", // contains "exist" but not a not-found phrase
	}
	for _, errText := range cases {
		t.Run(errText, func(t *testing.T) {
			msg := buildToolErrorMessage(
				"github__list_issues", json.RawMessage(`{}`), errText, nil, nil, tools,
			)
			if strings.Contains(msg, "Did you mean") {
				t.Fatalf("must not propose suggestion on opaque error %q, got %q", errText, msg)
			}
		})
	}
}

// TestLooksLikeToolNotFound is a small table driver pinning the exact
// phrase set we route to did-you-mean. The most important property
// pinned here: 'no matching route' fires, but the bare word 'route'
// does NOT — earlier drafts of the heuristic matched too widely and
// glued did-you-mean suggestions onto unrelated "route table" errors.
func TestLooksLikeToolNotFound(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "no matching route", text: "no matching route for github__list_issue", want: true},
		{name: "no route", text: "router: no route", want: true},
		{name: "not found suffix", text: "tool not found: foo", want: true},
		{name: "unknown tool", text: "unknown tool: foo", want: true},
		{name: "no tool", text: "no tool registered for foo", want: true},
		{name: "unrecognized", text: "unrecognized command", want: true},
		{name: "bare route does not match", text: "router internal route table corrupted", want: false},
		{name: "permission denied", text: "permission denied", want: false},
		{name: "rate limit", text: "rate limit exceeded", want: false},
		{name: "validation error", text: "field foo is required", want: false},
		{name: "empty string", text: "", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeToolNotFound(tc.text)
			if got != tc.want {
				t.Fatalf("looksLikeToolNotFound(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestSandbox_ParallelFailedEntryCarriesSuggestion asserts that a
// parallel() entry whose dispatch fails records a per-call error
// message annotated with a did-you-mean. The JS array still surfaces
// a null at the failing index — parallel() never throws.
func TestSandbox_ParallelFailedEntryCarriesSuggestion(t *testing.T) {
	caller := newMockCaller()
	caller.errors["github__list_issue"] = errors.New("tool not found: github__list_issue")
	caller.responses["github__list_issues"] = json.RawMessage(`{"content":[{"type":"text","text":"[]"}]}`)
	tools := []ToolDef{
		{Name: "github__list_issues", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "github__get_repo", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	code := `
const r = parallel([
  { tool: "github__list_issues" },
  { tool: "github__list_issue" },
]);
print(r[1] === null);
`
	result, err := sandbox.Execute(context.Background(), code, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("parallel() must not throw, got Error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "true") {
		t.Fatalf("failed entry should surface null, got output %q", result.Output)
	}

	// The failed call's record carries the did-you-mean.
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 records, got %d", len(result.ToolCalls))
	}
	var failed *ToolCallRecord
	for i := range result.ToolCalls {
		if result.ToolCalls[i].Name == "github__list_issue" {
			failed = &result.ToolCalls[i]
		}
	}
	if failed == nil {
		t.Fatalf("expected a record for github__list_issue, got %+v", result.ToolCalls)
	}
	if !strings.Contains(failed.Error, "Did you mean") {
		t.Fatalf("failed parallel entry missing did-you-mean: %q", failed.Error)
	}
	if !strings.Contains(failed.Error, "github__list_issues") {
		t.Fatalf("expected suggestion of github__list_issues, got %q", failed.Error)
	}
}

// TestSynthesizeExample_RequiredFieldsFirst asserts that required keys
// land first in the synthesized example so a model copy-pasting it gets
// the minimum viable call shape before optional knobs.
func TestSynthesizeExample_RequiredFieldsFirst(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"owner": {"type": "string"},
			"limit": {"type": "integer"},
			"repo":  {"type": "string"}
		},
		"required": ["owner", "repo"]
	}`)

	ex := synthesizeExample("github__get_repo", schema)
	idxOwner := strings.Index(ex, "owner:")
	idxRepo := strings.Index(ex, "repo:")
	idxLimit := strings.Index(ex, "limit:")
	if idxOwner < 0 || idxRepo < 0 || idxLimit < 0 {
		t.Fatalf("expected all 3 fields, got %s", ex)
	}
	if idxOwner > idxLimit || idxRepo > idxLimit {
		t.Fatalf("required fields must precede optional ones, got %s", ex)
	}
}

// TestSynthesizeExample_SplitOnFirstUnderscoreOnly asserts that
// `ns__tool__sub` renders as `ns.tool__sub` (split on the FIRST __
// only) — splitting on every `__` would generate `ns.tool.sub`, which
// is a different identifier in JS and breaks copy-paste.
func TestSynthesizeExample_SplitOnFirstUnderscoreOnly(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"x": {"type":"string"}},
		"required": ["x"]
	}`)

	ex := synthesizeExample("ns__tool__sub", schema)
	if !strings.HasPrefix(ex, "ns.tool__sub(") {
		t.Fatalf("expected ns.tool__sub call form, got %s", ex)
	}
	if strings.Contains(ex, "ns.tool.sub") {
		t.Fatalf("must not split on every __, got %s", ex)
	}
}

// TestSynthesizeExample_GlobalBucketForUnnamespaced asserts that a
// non-namespaced tool name (no `__`) renders with the `_global.` prefix
// so the model knows it's a top-level binding, matching typegen.go's
// `_global` namespace bucket.
func TestSynthesizeExample_GlobalBucketForUnnamespaced(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"x": {"type":"string"}},
		"required": ["x"]
	}`)

	ex := synthesizeExample("no_ns", schema)
	if !strings.HasPrefix(ex, "_global.no_ns(") {
		t.Fatalf("expected _global.no_ns call form, got %s", ex)
	}
}

// TestSchemaParamHint_ExampleSurvivesNoisySchema is the regression guard
// for the budget reservation: even when a schema has many properties,
// the synthesized/provided example must still appear because we
// reserve room for it before walking properties.
func TestSchemaParamHint_ExampleSurvivesNoisySchema(t *testing.T) {
	// Build a schema with many properties to push against the cap.
	var props strings.Builder
	props.WriteString(`{`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			props.WriteString(",")
		}
		props.WriteString(`"field_`)
		props.WriteByte(byte('a' + i%26))
		props.WriteString(`": {"type":"string","description":"a noisy field description that uses bytes"}`)
	}
	props.WriteString(`}`)
	schema := []byte(`{"type":"object","properties":` + props.String() + `}`)

	hint := schemaParamHint(json.RawMessage(schema), []string{`example.call({x: "y"})`})
	if !strings.Contains(hint, "Example: example.call") {
		t.Fatalf("example must survive crowded schema, got hint of len %d:\n%s", len(hint), hint)
	}
}

// TestSchemaParamHint_UTF8Safe asserts that truncation never splits a
// multi-byte UTF-8 codepoint — otherwise the rendered hint can contain
// invalid replacement characters that confuse downstream tooling.
func TestSchemaParamHint_UTF8Safe(t *testing.T) {
	// Build a schema large enough to force truncation, with multi-byte
	// runes in the description so a naive byte slice would split them.
	var props strings.Builder
	props.WriteString(`{`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			props.WriteString(",")
		}
		props.WriteString(`"field_`)
		props.WriteByte(byte('a' + i%26))
		props.WriteString(`": {"type":"string","description":"a — em-dash bullet — €€€ to push utf8 boundaries"}`)
	}
	props.WriteString(`}`)
	schema := []byte(`{"type":"object","properties":` + props.String() + `}`)

	hint := schemaParamHint(json.RawMessage(schema), nil)
	if hint == "" {
		t.Fatal("expected hint")
	}
	// The hint must remain valid UTF-8 even after truncation.
	for i, r := range hint {
		_ = i
		_ = r
	}
	if !utf8ValidString(hint) {
		t.Fatalf("hint is not valid utf8 after truncation: %q", hint)
	}
}

// utf8ValidString is a tiny stdlib-equivalent helper kept inline to
// avoid touching go.mod / dependency lists in this test file.
func utf8ValidString(s string) bool {
	for i := 0; i < len(s); {
		r, size := decodeRune(s[i:])
		if r == 0xFFFD && size == 1 {
			return false
		}
		i += size
	}
	return true
}

// decodeRune decodes the first rune from s. Returns (utf8.RuneError, 1)
// for invalid sequences. Inlined to keep this test self-contained.
func decodeRune(s string) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	b := s[0]
	switch {
	case b < 0x80:
		return rune(b), 1
	case b < 0xC0:
		return 0xFFFD, 1
	case b < 0xE0:
		if len(s) < 2 || s[1]&0xC0 != 0x80 {
			return 0xFFFD, 1
		}
		return rune(b&0x1F)<<6 | rune(s[1]&0x3F), 2
	case b < 0xF0:
		if len(s) < 3 || s[1]&0xC0 != 0x80 || s[2]&0xC0 != 0x80 {
			return 0xFFFD, 1
		}
		return rune(b&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	case b < 0xF8:
		if len(s) < 4 || s[1]&0xC0 != 0x80 || s[2]&0xC0 != 0x80 || s[3]&0xC0 != 0x80 {
			return 0xFFFD, 1
		}
		return rune(b&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	default:
		return 0xFFFD, 1
	}
}

// TestOutputTruncationNotice_ShapeHintAppended asserts the lost-value
// shape hint shows up in the truncation notice when the last printed
// arg was a map. Saves a model the round-trip of guessing what fields
// to drill into after a print overflow.
func TestOutputTruncationNotice_ShapeHintAppended(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)
	sandbox.maxOutputBytes = 60

	result, err := sandbox.Execute(context.Background(), `
const big = { name: "x".repeat(80), count: 1, status: "ok" };
print(big);
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OutputTruncated {
		t.Fatalf("expected truncation, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "print only the fields you need") {
		t.Fatalf("missing reworded advice, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "Lost value top-level shape:") {
		t.Fatalf("missing shape hint, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "count") || !strings.Contains(result.Output, "name") {
		t.Fatalf("shape hint missing keys, got %q", result.Output)
	}
}
