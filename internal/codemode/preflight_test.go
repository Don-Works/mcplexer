package codemode

import (
	"strings"
	"testing"
)

func TestPreflight_SyntaxError(t *testing.T) {
	issues := Preflight(`github.create_issue({title: "bug");`)
	if len(issues) == 0 {
		t.Fatal("expected syntax preflight issue")
	}
	if issues[0].Severity != "error" {
		t.Fatalf("severity = %q, want error", issues[0].Severity)
	}
	if !strings.Contains(issues[0].Message, "syntax error") {
		t.Fatalf("expected syntax error, got %+v", issues[0])
	}
}

func TestPreflight_DisallowsDynamicCodeConstructs(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{name: "eval", code: `eval("print(1)")`, want: "eval"},
		{name: "Function", code: `Function("return 1")()`, want: "Function"},
		{name: "new Function", code: `new Function("return 1")`, want: "Function"},
		{name: "global eval", code: `globalThis.eval("print(1)")`, want: "globalThis.eval"},
		{name: "require", code: `require("fs")`, want: "require"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			issues := Preflight(tc.code)
			if len(issues) == 0 {
				t.Fatal("expected preflight issue")
			}
			if !strings.Contains(issues[0].Message, tc.want) {
				t.Fatalf("issue %q does not mention %q", issues[0].Message, tc.want)
			}
		})
	}
}

func TestPreflight_AllowsPlainToolCallsAndStringMentions(t *testing.T) {
	code := `
print("eval('x') and require('fs') are text here");
github.create_issue({title: "bug"});
`
	if issues := Preflight(code); len(issues) > 0 {
		t.Fatalf("unexpected preflight issues: %+v", issues)
	}
}
