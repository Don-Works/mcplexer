package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSandbox_UndefinedPropertyAccessSuggestsFlatForm reproduces the exact
// mistake a model makes when it assumes namespaces nest: `mcpx.memory.recall()`
// where `mcpx` exists but `mcpx.memory` is undefined, so reading `.recall`
// off undefined throws a TypeError. The annotation must steer it to the flat
// top-level call `memory.recall(...)` and point at help().
func TestSandbox_UndefinedPropertyAccessSuggestsFlatForm(t *testing.T) {
	caller := newMockCaller()
	tools := []ToolDef{
		{Name: "mcpx__search_tools", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "memory__recall", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "memory__save", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `mcpx.memory.recall({});`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected TypeError from nested undefined access")
	}
	if !strings.Contains(result.Error, "memory.recall(...)") {
		t.Fatalf("expected flat-form suggestion memory.recall(...), got %q", result.Error)
	}
	if !strings.Contains(result.Error, "top-level") {
		t.Fatalf("expected top-level explanation, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "help()") {
		t.Fatalf("expected help() pointer, got %q", result.Error)
	}
}

// TestAnnotateRuntimeError_UndefinedProperty drives the annotation directly
// over the exact goja string (and the modern V8 phrasing) so the behaviour
// is pinned independent of the engine's runtime panic path.
func TestAnnotateRuntimeError_UndefinedProperty(t *testing.T) {
	tools := []string{"memory__recall", "memory__save", "mcpx__search_tools", "github__list_issues"}

	cases := []struct {
		name       string
		msg        string
		wantCall   string // empty => no call form expected
		wantNsList bool
	}{
		{
			name:     "goja phrasing, known member",
			msg:      "TypeError: Cannot read property 'recall' of undefined or null at <eval>:1:6(2)",
			wantCall: "memory.recall(...)",
		},
		{
			name:     "V8 phrasing, known member",
			msg:      "TypeError: Cannot read properties of undefined (reading 'recall')",
			wantCall: "memory.recall(...)",
		},
		{
			name:       "unknown member falls back to namespace list",
			msg:        "TypeError: Cannot read property 'zzz' of undefined or null at <eval>:1:6(2)",
			wantCall:   "",
			wantNsList: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := annotateRuntimeError(tc.msg, tools)
			if tc.wantCall != "" {
				if !strings.Contains(got, tc.wantCall) {
					t.Fatalf("expected call form %q, got %q", tc.wantCall, got)
				}
			} else {
				if strings.Contains(got, "(...)") {
					t.Fatalf("must not fabricate a call form for an unknown member, got %q", got)
				}
			}
			if tc.wantNsList && !strings.Contains(got, "Available namespaces:") {
				t.Fatalf("expected namespace inventory for unknown member, got %q", got)
			}
			// help( matches both the full helpHint (help()) and the shorter
			// helpNsHint (help('namespace')) used after a namespace list.
			if !strings.Contains(got, "help(") {
				t.Fatalf("expected help() pointer, got %q", got)
			}
		})
	}
}

// TestAnnotateRuntimeError_UndefinedPropertyNoTools is the safety case: with
// no registered tools there is nothing to suggest, so the original goja
// diagnostic passes through unchanged (no help() hint, no namespace list).
func TestAnnotateRuntimeError_UndefinedPropertyNoTools(t *testing.T) {
	msg := "TypeError: Cannot read property 'recall' of undefined or null at <eval>:1:6(2)"
	if got := annotateRuntimeError(msg, nil); got != msg {
		t.Fatalf("no tools => pass through unchanged, got %q", got)
	}
}

// TestAnnotateRuntimeError_UnrelatedTypeErrorUnchanged asserts the
// undefined-property annotation does NOT fire on unrelated errors — only the
// "Cannot read property/properties of undefined/null" shapes get rewritten;
// everything else (including TypeErrors mentioning a real tool name) passes
// through verbatim so the model still sees the genuine diagnostic.
func TestAnnotateRuntimeError_UnrelatedTypeErrorUnchanged(t *testing.T) {
	tools := []string{"memory__recall", "github__list_issues"}
	cases := []string{
		"TypeError: x is not a function at <eval>:1:1(0)",
		"TypeError: Cannot convert undefined to object",
		"RangeError: Maximum call stack size exceeded",
		"TypeError: recall is not a function", // mentions a real member, but not the property-access shape
	}
	for _, msg := range cases {
		msg := msg
		t.Run(msg, func(t *testing.T) {
			if got := annotateRuntimeError(msg, tools); got != msg {
				t.Fatalf("unrelated error must pass through unchanged:\n in:  %q\n out: %q", msg, got)
			}
		})
	}
}

// TestAnnotateRuntimeError_TypoBranchesPointToHelp asserts the pre-existing
// namespace-typo (ReferenceError) and member-typo (Object has no member)
// branches now also point a confused model at help().
func TestAnnotateRuntimeError_TypoBranchesPointToHelp(t *testing.T) {
	tools := []string{"github__list_issues", "github__get_repo"}

	nsTypo := annotateRuntimeError("ReferenceError: gihub is not defined at <eval>:1:1(0)", tools)
	if !strings.Contains(nsTypo, "help(") {
		t.Fatalf("expected help() hint on namespace typo, got %q", nsTypo)
	}

	memberTypo := annotateRuntimeError("TypeError: Object has no member 'list_isues' at <eval>:1:13(2)", tools)
	if !strings.Contains(memberTypo, "github.list_issues") {
		t.Fatalf("expected member did-you-mean, got %q", memberTypo)
	}
	if !strings.Contains(memberTypo, "help(") {
		t.Fatalf("expected help() hint on member typo, got %q", memberTypo)
	}
}
