package runner

import (
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestParseHookVerdict(t *testing.T) {
	cases := []struct {
		name        string
		res         ToolCallResult
		wantBlocked bool
		wantReason  string // substring
	}{
		{
			name:        "clean run proceeds",
			res:         ToolCallResult{OutputJSON: `{"content":[{"type":"text","text":"ok"}],"isError":false}`},
			wantBlocked: false,
		},
		{
			name:        "explicit abort sentinel blocks with clean reason",
			res:         ToolCallResult{OutputJSON: `{"content":[{"type":"text","text":"` + hookVerdictSentinel + `{\"action\":\"abort\",\"reason\":\"gate said no\"}"}],"isError":true}`, IsError: true},
			wantBlocked: true,
			wantReason:  "gate said no",
		},
		{
			name:        "bare throw blocks fail-closed via isError",
			res:         ToolCallResult{OutputJSON: `{"content":[{"type":"text","text":"Error: boom at <anonymous>"}],"isError":true}`, IsError: true},
			wantBlocked: true,
			wantReason:  "boom",
		},
		{
			name:        "transport-error envelope blocks",
			res:         ToolCallResult{OutputJSON: `{"error":"sandbox down"}`, IsError: true},
			wantBlocked: true,
			wantReason:  "sandbox down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHookVerdict(hookPhasePre, tc.res)
			if got.blocked != tc.wantBlocked {
				t.Fatalf("blocked = %v, want %v (reason=%q)", got.blocked, tc.wantBlocked, got.reason)
			}
			if tc.wantReason != "" && !strings.Contains(got.reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", got.reason, tc.wantReason)
			}
		})
	}
}

func TestExtractHookSentinel(t *testing.T) {
	text := "some print output\n" + hookVerdictSentinel + `{"action":"abort","reason":"nope"}` + "\nmore"
	reason, ok := extractHookSentinel(text)
	if !ok || reason != "nope" {
		t.Fatalf("got (%q, %v), want (\"nope\", true)", reason, ok)
	}
	if _, ok := extractHookSentinel("no sentinel here"); ok {
		t.Fatalf("expected no sentinel match")
	}
}

func TestComposeHookCode_BindsContextAndHelpers(t *testing.T) {
	w := &store.Worker{ID: "wkr-1", Name: "demo", WorkspaceID: "ws-1", ExecMode: "autonomous", ParametersJSON: `{"k":"v"}`}
	code := composeHookCode(hookPhasePre, w, hookRunCtx{ID: "run-1", TriggerKind: "manual"}, `if (hook.run.id) abort("x");`)
	for _, want := range []string{"var hook = ", `"wkr-1"`, `"run-1"`, "function abort(reason)", hookVerdictSentinel, "pre_execute_script", `if (hook.run.id) abort("x");`} {
		if !strings.Contains(code, want) {
			t.Fatalf("composed code missing %q\n---\n%s", want, code)
		}
	}
}

func TestJsSafeJSON_EscapesLineSeparators(t *testing.T) {
	in := "a" + string(rune(0x2028)) + "b" + string(rune(0x2029)) + "c"
	// Expectation is pure ASCII: each separator becomes its six-char escape.
	want := "a\\u2028b\\u2029c"
	if got := jsSafeJSON(in); got != want {
		t.Fatalf("jsSafeJSON(%q) = %q, want %q", in, got, want)
	}
	// ASCII-only input is returned untouched.
	if got := jsSafeJSON("plain"); got != "plain" {
		t.Fatalf("plain input mutated to %q", got)
	}
}
