package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSandbox_Parallel_OrderAndErrors drives parallel() through Execute and
// asserts result ordering, per-item error handling, and isError mapping.
// Run with -race to validate the records mutex and the per-index writes in
// executeParallel.
func TestSandbox_Parallel_OrderAndErrors(t *testing.T) {
	caller := newMockCaller()
	caller.responses["svc__a"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"a\"}"}]}`)
	caller.responses["svc__b"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"b\"}"}]}`)
	caller.responses["svc__c"] = json.RawMessage(`{"content":[{"type":"text","text":"{\"v\":\"c\"}"}]}`)
	// svc__b fails at the transport layer → null at its index.
	caller.errors["svc__fail"] = fmt.Errorf("boom")
	// svc__err returns an isError envelope → null at its index.
	caller.responses["svc__err"] = json.RawMessage(`{"content":[{"type":"text","text":"nope"}],"isError":true}`)
	// Force overlap so the goroutines genuinely run concurrently.
	caller.delay["svc__a"] = 20 * time.Millisecond
	caller.delay["svc__b"] = 20 * time.Millisecond
	caller.delay["svc__c"] = 20 * time.Millisecond

	tools := []ToolDef{
		{Name: "svc__a", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "svc__b", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "svc__c", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "svc__fail", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "svc__err", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	cases := []struct {
		name string
		code string
		want string
	}{
		{
			name: "results come back in descriptor order",
			code: `
const r = parallel([
  { tool: "svc__a" },
  { tool: "svc__b" },
  { tool: "svc__c" },
]);
print(r.map(x => x.v).join(","));`,
			want: "a,b,c\n",
		},
		{
			name: "failed call yields null at its index while siblings succeed",
			code: `
const r = parallel([
  { tool: "svc__a" },
  { tool: "svc__fail" },
  { tool: "svc__c" },
]);
print((r[0] && r[0].v) + "," + (r[1] === null) + "," + (r[2] && r[2].v));`,
			want: "a,true,c\n",
		},
		{
			name: "isError result maps to null and siblings survive",
			code: `
const r = parallel([
  { tool: "svc__a" },
  { tool: "svc__err" },
]);
print((r[0] && r[0].v) + "," + (r[1] === null));`,
			want: "a,true\n",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sandbox := NewSandbox(caller, 5*time.Second)
			result, err := sandbox.Execute(context.Background(), tc.code, tools)
			if err != nil {
				t.Fatal(err)
			}
			if result.Error != "" {
				t.Fatalf("unexpected execution error: %s", result.Error)
			}
			if result.Output != tc.want {
				t.Fatalf("want output %q, got %q", tc.want, result.Output)
			}
		})
	}
}

// TestSandbox_Parallel_Validation covers the descriptor-validation panics
// surfaced through parseParallelArgs: the cap, missing tool, non-array arg,
// and non-object items. A panic inside the tool function is recovered by the
// VM and surfaced as result.Error.
func TestSandbox_Parallel_Validation(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		wantSub string
	}{
		{
			name:    "over the cap panics with the cap message",
			code:    buildParallelOverCap(maxParallelCalls + 1),
			wantSub: fmt.Sprintf("max %d calls", maxParallelCalls),
		},
		{
			name:    "non-array argument panics",
			code:    `parallel("not an array");`,
			wantSub: "must be an array",
		},
		{
			name:    "non-object item panics",
			code:    `parallel([42]);`,
			wantSub: "must be an object",
		},
		{
			name:    "missing tool field panics",
			code:    `parallel([{ args: {} }]);`,
			wantSub: "missing 'tool' field",
		},
		{
			name:    "no arguments panics",
			code:    `parallel();`,
			wantSub: "requires an array",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			caller := newMockCaller()
			sandbox := NewSandbox(caller, 5*time.Second)
			result, err := sandbox.Execute(context.Background(), tc.code, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Error, tc.wantSub) {
				t.Fatalf("want error containing %q, got %q", tc.wantSub, result.Error)
			}
			// Exactly at-cap should NOT panic on count (sanity for the boundary).
			if tc.name == "over the cap panics with the cap message" && caller.callCount() != 0 {
				t.Fatalf("no tool call should have fired when validation panics, got %d", caller.callCount())
			}
		})
	}
}

// TestSandbox_Parallel_AtCapSucceeds asserts the boundary: exactly
// maxParallelCalls descriptors are accepted and all dispatch.
func TestSandbox_Parallel_AtCapSucceeds(t *testing.T) {
	caller := newMockCaller()
	tools := []ToolDef{{Name: "svc__ping", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}

	var b strings.Builder
	b.WriteString("const r = parallel([")
	for i := 0; i < maxParallelCalls; i++ {
		b.WriteString(`{ tool: "svc__ping" },`)
	}
	b.WriteString("]); print(r.length);")

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(), b.String(), tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("at-cap parallel should succeed, got error: %s", result.Error)
	}
	if result.Output != fmt.Sprintf("%d\n", maxParallelCalls) {
		t.Fatalf("want %d results, got %q", maxParallelCalls, result.Output)
	}
	if caller.callCount() != maxParallelCalls {
		t.Fatalf("want %d dispatched calls, got %d", maxParallelCalls, caller.callCount())
	}
	if len(result.ToolCalls) != maxParallelCalls {
		t.Fatalf("want %d tool-call records, got %d", maxParallelCalls, len(result.ToolCalls))
	}
}

func buildParallelOverCap(n int) string {
	var b strings.Builder
	b.WriteString("parallel([")
	for i := 0; i < n; i++ {
		b.WriteString(`{ tool: "svc__x" },`)
	}
	b.WriteString("]);")
	return b.String()
}
