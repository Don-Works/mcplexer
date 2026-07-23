package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// parallelRefTools is the tool set used by the calling-convention tests: three
// svc tools whose canned responses echo a distinguishing value so result
// ordering can be asserted.
func parallelRefTools() (*mockToolCaller, []ToolDef) {
	caller := newMockCaller()
	caller.responses["svc__a"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"a\"}"}]}`)
	caller.responses["svc__b"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"b\"}"}]}`)
	caller.responses["svc__c"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"c\"}"}]}`)
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	tools := []ToolDef{
		{Name: "svc__a", InputSchema: schema},
		{Name: "svc__b", InputSchema: schema},
		{Name: "svc__c", InputSchema: schema},
	}
	return caller, tools
}

// argsFor returns the JSON args recorded for the first call to the named tool.
func argsFor(t *testing.T, caller *mockToolCaller, name string) string {
	t.Helper()
	caller.mu.Lock()
	defer caller.mu.Unlock()
	for _, c := range caller.calls {
		if c.Name == name {
			return string(c.Args)
		}
	}
	t.Fatalf("no recorded call to %s (calls: %v)", name, caller.calls)
	return ""
}

// TestParallel_BoundReferenceForms drives parallel() through the natural
// calling forms unified with the sequential path: [fn, args] tuples,
// {tool: fn, args} objects, and mixed batches — asserting both ordering and
// that args actually reach the right downstream tool.
func TestParallel_BoundReferenceForms(t *testing.T) {
	cases := []struct {
		name       string
		code       string
		wantOutput string
		wantArgs   map[string]string // tool name -> expected recorded args JSON
	}{
		{
			name: "tuple form [fn, args] preserves order and passes args",
			code: `
const r = parallel([
  [svc.a, {q: "one"}],
  [svc.b, {q: "two"}],
  [svc.c, {q: "three"}],
]);
print(r.map(x => x.v).join(","));`,
			wantOutput: "a,b,c\n",
			wantArgs: map[string]string{
				"svc__a": `{"q":"one"}`,
				"svc__c": `{"q":"three"}`,
			},
		},
		{
			name: "object form with bound function reference",
			code: `
const r = parallel([
  {tool: svc.a, args: {q: "x"}},
  {tool: svc.b, args: {q: "y"}},
]);
print(r.map(x => x.v).join(","));`,
			wantOutput: "a,b\n",
			wantArgs:   map[string]string{"svc__b": `{"q":"y"}`},
		},
		{
			name: "mixed batch: bound tuple, bound object, and legacy string",
			code: `
const r = parallel([
  [svc.a, {q: "1"}],
  {tool: svc.b, args: {q: "2"}},
  {tool: "svc__c", args: {q: "3"}},
]);
print(r.map(x => x.v).join(","));`,
			wantOutput: "a,b,c\n",
			wantArgs: map[string]string{
				"svc__a": `{"q":"1"}`,
				"svc__b": `{"q":"2"}`,
				"svc__c": `{"q":"3"}`,
			},
		},
		{
			name: "tuple with string tool name (symmetry) and missing args is allowed",
			code: `
const r = parallel([
  ["svc__a"],
  [svc.b, {q: "z"}],
]);
print(r.map(x => x.v).join(","));`,
			wantOutput: "a,b\n",
			wantArgs:   map[string]string{"svc__a": `{}`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller, tools := parallelRefTools()
			sandbox := NewSandbox(caller, 5*time.Second)
			result, err := sandbox.Execute(context.Background(), tc.code, tools)
			if err != nil {
				t.Fatal(err)
			}
			if result.Error != "" {
				t.Fatalf("unexpected execution error: %s", result.Error)
			}
			if result.Output != tc.wantOutput {
				t.Fatalf("want output %q, got %q", tc.wantOutput, result.Output)
			}
			for tool, wantArgs := range tc.wantArgs {
				if got := argsFor(t, caller, tool); got != wantArgs {
					t.Fatalf("args for %s: want %s, got %s", tool, wantArgs, got)
				}
			}
		})
	}
}

// TestParallel_RefValidation covers the new-form validation errors: a non-tool
// closure, a non-object args value, and a tuple whose tool slot is empty. Each
// must surface a clear, actionable message rather than a bare transport error.
func TestParallel_RefValidation(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		wantSub string
	}{
		{
			name:    "an agent's own closure is not a tool",
			code:    `parallel([[() => 1, {}]]);`,
			wantSub: "not a namespace tool",
		},
		{
			name:    "non-object args in tuple form is rejected",
			code:    `parallel([[svc.a, 42]]);`,
			wantSub: "'args' must be an object",
		},
		{
			name:    "tuple with empty tool slot is rejected",
			code:    `parallel([[undefined, {q: "x"}]]);`,
			wantSub: "element 0 must be a tool reference",
		},
		{
			name:    "scalar item is neither object nor tuple",
			code:    `parallel([42]);`,
			wantSub: "must be an object",
		},
	}

	_, tools := parallelRefTools()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := newMockCaller()
			sandbox := NewSandbox(caller, 5*time.Second)
			result, err := sandbox.Execute(context.Background(), tc.code, tools)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Error, tc.wantSub) {
				t.Fatalf("want error containing %q, got %q", tc.wantSub, result.Error)
			}
		})
	}
}

// TestParallel_TaggedFuncHiddenFromKeys guards the ergonomic contract that the
// hidden __tool tag never leaks into Object.keys() of a namespace tool — an
// agent enumerating a function must not see sandbox bookkeeping.
func TestParallel_TaggedFuncHiddenFromKeys(t *testing.T) {
	caller, tools := parallelRefTools()
	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`print(JSON.stringify(Object.keys(svc.a)));`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if strings.TrimSpace(result.Output) != "[]" {
		t.Fatalf("tool function should expose no enumerable keys, got %q", result.Output)
	}
}
