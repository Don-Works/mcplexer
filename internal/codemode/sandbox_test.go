package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockToolCaller records calls and returns canned responses. CallTool is
// invoked concurrently by the parallel() helper, so the calls slice is
// guarded by a mutex; responses/errors are only mutated during test setup
// (before any concurrent dispatch) so they need no lock.
type mockToolCaller struct {
	mu        sync.Mutex
	calls     []mockCall
	responses map[string]json.RawMessage
	errors    map[string]error
	// delay, when set for a tool name, sleeps before returning — used to
	// force genuine goroutine overlap in concurrency tests.
	delay map[string]time.Duration
}

type mockCall struct {
	Name string
	Args json.RawMessage
}

func newMockCaller() *mockToolCaller {
	return &mockToolCaller{
		responses: make(map[string]json.RawMessage),
		errors:    make(map[string]error),
		delay:     make(map[string]time.Duration),
	}
}

func (m *mockToolCaller) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	d := m.delay[name]
	m.mu.Unlock()

	if d > 0 {
		time.Sleep(d)
	}
	if err, ok := m.errors[name]; ok {
		return nil, err
	}
	if resp, ok := m.responses[name]; ok {
		return resp, nil
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
}

// callCount returns the number of recorded calls under the lock.
func (m *mockToolCaller) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func TestSandbox_Print(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `print("hello world");`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello world\n" {
		t.Errorf("expected 'hello world\\n', got %q", result.Output)
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestSandbox_ConsoleLog(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `console.log("test");`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "test\n" {
		t.Errorf("expected 'test\\n', got %q", result.Output)
	}
}

func TestSandbox_Base64Helpers(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `
const text = atob("SGVsbG8h");
print(text);
print(btoa(text));
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Output != "Hello!\nSGVsbG8h\n" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestSandbox_AtobInvalidInputThrowsError(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `atob("not valid base64!!!");`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "atob: invalid base64") {
		t.Fatalf("expected atob error, got %q", result.Error)
	}
}

func TestSandbox_PrintOutputCapTruncatesDuringCapture(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)
	sandbox.maxOutputBytes = 20

	result, err := sandbox.Execute(context.Background(), `
print("x".repeat(100));
print("after cap");
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OutputTruncated {
		t.Fatal("expected output_truncated=true")
	}
	if result.OutputMaxBytes != 20 {
		t.Fatalf("OutputMaxBytes = %d, want 20", result.OutputMaxBytes)
	}
	if result.OutputBytesOmitted <= 0 {
		t.Fatalf("OutputBytesOmitted = %d, want > 0", result.OutputBytesOmitted)
	}
	if !strings.Contains(result.Output, "[truncated: code-mode print output exceeded 20 bytes") {
		t.Fatalf("missing truncation marker: %q", result.Output)
	}
	if strings.Contains(result.Output, "after cap") {
		t.Fatalf("output after cap should be omitted, got %q", result.Output)
	}
	if len(result.Output) > 320 {
		t.Fatalf("truncated output still too large: %d bytes", len(result.Output))
	}
}

func TestSandbox_PrintOutputCapCanBeRaised(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)
	sandbox.maxOutputBytes = 128

	result, err := sandbox.Execute(context.Background(), `print("x".repeat(100));`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputTruncated {
		t.Fatalf("unexpected truncation: %q", result.Output)
	}
	if result.Output != strings.Repeat("x", 100)+"\n" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestSandbox_ToolCall(t *testing.T) {
	caller := newMockCaller()
	caller.responses["github__list_issues"] = json.RawMessage(
		`{"content":[{"type":"text","text":"[{\"id\":1,\"title\":\"bug\"}]"}]}`,
	)

	tools := []ToolDef{{
		Name: "github__list_issues",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"owner": {"type": "string"}},
			"required": ["owner"]
		}`),
	}}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`const issues = github.list_issues({ owner: "org" }); print(issues.length);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(caller.calls))
	}
	if caller.calls[0].Name != "github__list_issues" {
		t.Errorf("expected github__list_issues, got %s", caller.calls[0].Name)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call record, got %d", len(result.ToolCalls))
	}
	if result.Output != "1\n" {
		t.Errorf("expected '1\\n', got %q", result.Output)
	}
}

func TestSandbox_MultiNamespace(t *testing.T) {
	caller := newMockCaller()
	caller.responses["github__list_issues"] = json.RawMessage(
		`{"content":[{"type":"text","text":"[{\"title\":\"fix\"}]"}]}`,
	)
	caller.responses["linear__create_issue"] = json.RawMessage(
		`{"content":[{"type":"text","text":"{\"id\":\"LIN-1\"}"}]}`,
	)

	tools := []ToolDef{
		{Name: "github__list_issues", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "linear__create_issue", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	code := `
const issues = github.list_issues();
for (const issue of issues) {
  linear.create_issue({ title: issue.title });
}
print("synced " + issues.length);
`
	result, err := sandbox.Execute(context.Background(), code, tools)
	if err != nil {
		t.Fatal(err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(caller.calls))
	}
	if result.Output != "synced 1\n" {
		t.Errorf("expected 'synced 1\\n', got %q", result.Output)
	}
}

func TestSandbox_Timeout(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 100*time.Millisecond)

	result, err := sandbox.Execute(context.Background(), `while(true) {}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Error, "execution timed out") {
		t.Errorf("expected timeout error prefix, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "sleep(ms) is clamped to 60s per call") {
		t.Errorf("timeout error should mention sleep clamp, got %q", result.Error)
	}
}

func TestSandbox_MemoryLimit(t *testing.T) {
	caller := newMockCaller()
	// 30s wall-clock so we genuinely measure heap, not timeout.
	sandbox := NewSandbox(caller, 30*time.Second)
	sandbox.maxHeapGrowthMB = 4
	sandbox.watchdogPeriod = 10 * time.Millisecond

	// Allocate aggressively. Each iteration grows the heap; the watchdog
	// must interrupt before the wall-clock timeout fires.
	code := `
		var a = [];
		while (true) {
			a.push(new Array(10000).fill('x'));
		}
	`
	start := time.Now()
	result, err := sandbox.Execute(context.Background(), code, nil)
	dur := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "execution exceeded memory limit" {
		t.Errorf("expected memory-limit error, got %q (after %s)", result.Error, dur)
	}
	if dur > 10*time.Second {
		t.Errorf("memory watchdog took too long: %s", dur)
	}
}

func TestSandbox_RecursionLimit(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	// Deep recursion that exceeds the call-stack guard.
	code := `function r(n) { return r(n+1); } r(0);`
	result, err := sandbox.Execute(context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatalf("expected recursion error, got success")
	}
}

func TestSandbox_ToolError(t *testing.T) {
	caller := newMockCaller()
	caller.errors["github__delete_repo"] = fmt.Errorf("permission denied")

	tools := []ToolDef{
		{Name: "github__delete_repo", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`github.delete_repo();`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Error("expected error from failed tool call")
	}
	// Error should include tool name and the downstream error.
	if !strings.Contains(result.Error, "github__delete_repo") {
		t.Errorf("error should include tool name, got: %s", result.Error)
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Errorf("error should include downstream error, got: %s", result.Error)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call record, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Error == "" {
		t.Error("expected error in tool call record")
	}
}

func TestSandbox_ToolIsErrorResult(t *testing.T) {
	caller := newMockCaller()
	// Simulate a downstream server returning isError: true with a generic message.
	caller.responses["postgres__query"] = json.RawMessage(
		`{"content":[{"type":"text","text":"query execution failed — check syntax and try again"}],"isError":true}`,
	)

	tools := []ToolDef{
		{Name: "postgres__query", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`postgres.query({ query: "SELECT * FROM users WHERE created > NOW() - INTERVAL '1 day'" });`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected error from isError tool result")
	}

	// Error should include tool name so the LLM knows which call failed.
	if !strings.Contains(result.Error, "postgres__query") {
		t.Errorf("error should include tool name, got: %s", result.Error)
	}
	// Error should include the arguments so the LLM can inspect them.
	if !strings.Contains(result.Error, "SELECT * FROM users") {
		t.Errorf("error should include arguments, got: %s", result.Error)
	}
	// Error should include the downstream error text.
	if !strings.Contains(result.Error, "query execution failed") {
		t.Errorf("error should include downstream error, got: %s", result.Error)
	}

	// ToolCallRecord should also capture the error.
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call record, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Error == "" {
		t.Error("expected error in tool call record for isError result")
	}
	if !strings.Contains(result.ToolCalls[0].Error, "query execution failed") {
		t.Errorf("tool call record error should include downstream text, got: %s", result.ToolCalls[0].Error)
	}
}

func TestSandbox_ToolIsErrorDoesNotLosePartialOutput(t *testing.T) {
	caller := newMockCaller()
	caller.responses["db__count"] = json.RawMessage(
		`{"content":[{"type":"text","text":"42"}]}`,
	)
	caller.responses["db__bad_query"] = json.RawMessage(
		`{"content":[{"type":"text","text":"column not found"}],"isError":true}`,
	)

	tools := []ToolDef{
		{Name: "db__count", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "db__bad_query", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`const n = db.count(); print("count=" + n); db.bad_query();`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}

	// The first call's output should be preserved even though the second failed.
	if !strings.Contains(result.Output, "count=42") {
		t.Errorf("partial output should be preserved, got: %q", result.Output)
	}
	// The error should reference the failing tool.
	if !strings.Contains(result.Error, "db__bad_query") {
		t.Errorf("error should reference failing tool, got: %s", result.Error)
	}
	// Both tool calls should be recorded.
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool call records, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Error != "" {
		t.Errorf("first call should have no error, got: %s", result.ToolCalls[0].Error)
	}
	if result.ToolCalls[1].Error == "" {
		t.Error("second call should have error recorded")
	}
}

func TestSandbox_DataFiltering(t *testing.T) {
	caller := newMockCaller()
	// Simulate a large result that gets filtered in code.
	activities := make([]map[string]any, 100)
	for i := range activities {
		activities[i] = map[string]any{
			"id":   i,
			"type": "run",
		}
		if i%10 == 0 {
			activities[i]["type"] = "ride"
		}
	}
	data, _ := json.Marshal(activities)
	caller.responses["intervals__list_activities"] = json.RawMessage(
		fmt.Sprintf(`{"content":[{"type":"text","text":%s}]}`, string(mustMarshal(string(data)))),
	)

	tools := []ToolDef{
		{Name: "intervals__list_activities", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	code := `
const all = intervals.list_activities();
const rides = all.filter(a => a.type === "ride");
print("rides: " + rides.length);
`
	result, err := sandbox.Execute(context.Background(), code, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "rides: 10\n" {
		t.Errorf("expected 'rides: 10\\n', got %q", result.Output)
	}
}

func TestSandbox_PrintAutoSerializesObjects(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	code := `
const obj = { name: "alice", count: 3 };
print(obj);
const arr = [1, 2, 3];
print(arr);
print("string", 42, true);
`
	result, err := sandbox.Execute(context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Objects should be JSON-serialized, not "[object Object]".
	if strings.Contains(result.Output, "[object Object]") {
		t.Errorf("print should auto-serialize objects, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, `"name"`) {
		t.Errorf("expected JSON key in output, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, `"alice"`) {
		t.Errorf("expected JSON value in output, got:\n%s", result.Output)
	}
	// Primitives should still work normally.
	if !strings.Contains(result.Output, "string 42 true") {
		t.Errorf("expected primitives on one line, got:\n%s", result.Output)
	}
}

func TestSandbox_PrintToolResultWithoutStringify(t *testing.T) {
	caller := newMockCaller()
	caller.responses["api__get_user"] = json.RawMessage(
		`{"content":[{"type":"text","text":"{\"id\":1,\"name\":\"bob\"}"}]}`,
	)

	tools := []ToolDef{
		{Name: "api__get_user", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	// The key test: print(result) should show JSON, not [object Object].
	result, err := sandbox.Execute(context.Background(),
		`const user = api.get_user(); print(user);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "[object Object]") {
		t.Errorf("print(toolResult) should auto-serialize, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, `"bob"`) {
		t.Errorf("expected user data in output, got:\n%s", result.Output)
	}
}

func TestSandbox_SyntaxError(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `const x = ;`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Error("expected syntax error")
	}
}

// TestSandbox_BuiltinResultWithMeshFooterAutoUnwraps reproduces the
// task__create bug filed as 01KSGKJDHZ2HS87TRK8BTQQ89S. The gateway
// piggybacks a mesh-notice block onto every non-mesh builtin's result
// when there are pending mesh messages, which used to defeat the
// auto-unwrap (it only fired on `len(content) == 1`). The fix scans
// for the first JSON-parseable text block and unwraps that, so the
// natural `task.create({...}).id` pattern keeps working regardless of
// mesh queue state.
func TestSandbox_BuiltinResultWithMeshFooterAutoUnwraps(t *testing.T) {
	caller := newMockCaller()
	// Simulate the exact envelope a task__create returns when there's a
	// pending mesh message — first block is the canonical JSON payload,
	// second block is the appended `[mesh: N pending …]` decorator.
	caller.responses["task__create"] = json.RawMessage(
		`{"content":[` +
			`{"type":"text","text":"{\"task\":{\"id\":\"01TASKID\",\"title\":\"epic\"}}"},` +
			`{"type":"text","text":"[mesh: 3 pending message(s) — call mesh__receive to read]"}` +
			`]}`,
	)

	tools := []ToolDef{
		{Name: "task__create", InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	// The canonical chained pattern — would be broken pre-fix:
	// `r.task.id` is undefined when the envelope leaks through.
	result, err := sandbox.Execute(context.Background(),
		`const r = task.create({title:"epic"}); print(r.task.id);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected execution error: %s", result.Error)
	}
	if result.Output != "01TASKID\n" {
		t.Fatalf("expected auto-unwrapped task.id, got %q", result.Output)
	}
}

// TestSandbox_StructuredContentPreferredOverText proves the sandbox
// honors the MCP-spec `structuredContent` field when present, even if
// the text content array contains a stale or differing payload.
func TestSandbox_StructuredContentPreferredOverText(t *testing.T) {
	caller := newMockCaller()
	caller.responses["task__get"] = json.RawMessage(
		`{"content":[{"type":"text","text":"{\"task\":{\"id\":\"OLD\"}}"}],` +
			`"structuredContent":{"task":{"id":"NEW"}}}`,
	)

	tools := []ToolDef{
		{Name: "task__get", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`const r = task.get(); print(r.task.id);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "NEW\n" {
		t.Fatalf("expected structuredContent payload, got %q", result.Output)
	}
}

// TestSandbox_MultiContentReturnsFirstBlock asserts the post-fix policy:
// content[0] is the canonical payload, any later blocks are decorations
// (mesh notice, audit hints) transparent to user code. Pre-fix behavior
// returned the full envelope for any multi-block response, which made
// every chained builtin pattern (`task.create({...}).id`) silently break
// whenever the gateway piggybacked a mesh notice — the bug filed as
// 01KSGKJDHZ2HS87TRK8BTQQ89S.
func TestSandbox_MultiContentReturnsFirstBlock(t *testing.T) {
	caller := newMockCaller()
	caller.responses["github__get_bundle"] = json.RawMessage(
		`{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`,
	)

	tools := []ToolDef{
		{Name: "github__get_bundle", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}

	sandbox := NewSandbox(caller, 5*time.Second)
	result, err := sandbox.Execute(
		context.Background(),
		`const bundle = github.get_bundle(); print(bundle.text);`,
		tools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "first\n" {
		t.Fatalf("expected first content block, got %q", result.Output)
	}
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

// TestParseToolResultValue_UnwrapTable exercises the auto-unwrap helper
// directly against the table of cases listed in the
// "auto-unwrap MCP tool-result envelopes" task. Keeping this as a pure
// Go test (rather than going through Goja) makes regressions obvious
// — every row corresponds to a real shape we get from the MCP wire.
func TestParseToolResultValue_UnwrapTable(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantValue  any // exact shape we expect back in JS land
		wantErrSub string
	}{
		{
			name:      "json text content unwraps to object",
			raw:       `{"content":[{"type":"text","text":"{\"id\":\"T1\",\"title\":\"hi\"}"}]}`,
			wantValue: map[string]any{"id": "T1", "title": "hi"},
		},
		{
			name:      "json array text content unwraps to slice",
			raw:       `{"content":[{"type":"text","text":"[1,2,3]"}]}`,
			wantValue: []any{float64(1), float64(2), float64(3)},
		},
		{
			name: "plain (non-json) text content projects to text object",
			raw:  `{"content":[{"type":"text","text":"hello world"}]}`,
			wantValue: map[string]any{
				"kind": TextProjectionKind, "text": "hello world", "bytes": 11,
			},
		},
		{
			name:       "isError surfaces error text",
			raw:        `{"content":[{"type":"text","text":"boom"}],"isError":true}`,
			wantErrSub: "boom",
		},
		{
			// Post-fix policy: content[0] is canonical. Subsequent blocks
			// are decorations (mesh notice etc.) transparent to user code.
			name: "multi-block content projects first text block",
			raw:  `{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`,
			wantValue: map[string]any{
				"kind": TextProjectionKind, "text": "first", "bytes": 5,
			},
		},
		{
			name:      "missing content array returns raw object",
			raw:       `{"isError":false}`,
			wantValue: map[string]any{"isError": false},
		},
		{
			name:      "non-envelope shape returns parsed object as-is",
			raw:       `{"id":42,"name":"bob"}`,
			wantValue: map[string]any{"id": float64(42), "name": "bob"},
		},
		{
			name:      "malformed json falls back to raw string",
			raw:       `not valid json {`,
			wantValue: "not valid json {",
		},
		{
			name: "resource content with application/json unwraps",
			raw: `{"content":[{"type":"resource","resource":{` +
				`"mimeType":"application/json","text":"{\"x\":1}"}}]}`,
			wantValue: map[string]any{"x": float64(1)},
		},
		{
			// Plain-text payload + mesh footer (the memory__save shape) —
			// the text block survives the mesh-notice piggyback and is
			// projected so Object.keys never yields numeric indexes.
			name: "plain-text first block + mesh footer projects text object",
			raw: `{"content":[` +
				`{"type":"text","text":"Saved memory shape-test (01XYZ) in scope=ws-1."},` +
				`{"type":"text","text":"[mesh: 5 pending message(s) — call mesh__receive to read]"}` +
				`]}`,
			wantValue: map[string]any{
				"kind":  TextProjectionKind,
				"text":  "Saved memory shape-test (01XYZ) in scope=ws-1.",
				"bytes": 46,
			},
		},
		{
			// The core bug fix: builtin task__*/memory__*/etc. results
			// receive a mesh-notice footer block when there are pending
			// mesh messages. The sandbox MUST still auto-unwrap the JSON
			// payload from the first content block so chained patterns
			// (`task.create({...}).id`) keep working.
			name: "json-first multi-block envelope unwraps the JSON block",
			raw: `{"content":[` +
				`{"type":"text","text":"{\"task\":{\"id\":\"01H\",\"title\":\"hi\"}}"},` +
				`{"type":"text","text":"[mesh: 5 pending message(s) — call mesh__receive to read]"}` +
				`]}`,
			wantValue: map[string]any{
				"task": map[string]any{"id": "01H", "title": "hi"},
			},
		},
		{
			// structuredContent (MCP-spec parsed payload) is preferred
			// when present — bypasses any content-block ambiguity.
			name: "structuredContent takes precedence over content array",
			raw: `{"content":[{"type":"text","text":"{\"old\":true}"}],` +
				`"structuredContent":{"id":"X1","kind":"new"}}`,
			wantValue: map[string]any{"id": "X1", "kind": "new"},
		},
		{
			// structuredContent containing an array is fine too.
			name:      "structuredContent array unwraps to slice",
			raw:       `{"content":[{"type":"text","text":"ignored"}],"structuredContent":[1,2,3]}`,
			wantValue: []any{float64(1), float64(2), float64(3)},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotErr := parseToolResultValue(json.RawMessage(tc.raw))
			if tc.wantErrSub != "" {
				if !strings.Contains(gotErr, tc.wantErrSub) {
					t.Fatalf("want error containing %q, got %q", tc.wantErrSub, gotErr)
				}
				return
			}
			if gotErr != "" {
				t.Fatalf("unexpected error: %s", gotErr)
			}
			gotJSON, _ := json.Marshal(gotValue)
			wantJSON, _ := json.Marshal(tc.wantValue)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("unwrap mismatch:\n want: %s\n got:  %s", wantJSON, gotJSON)
			}
		})
	}
}

// TestSandbox_FailedToolCallThrowsRealError verifies that a failed tool call
// throws a real JavaScript Error: catch(e){e.message} must be populated and
// e instanceof Error must be true. Bare-string throws left e.message
// undefined, silently breaking idiomatic error handling in agent code.
func TestSandbox_FailedToolCallThrowsRealError(t *testing.T) {
	caller := newMockCaller()
	caller.errors["db__query"] = fmt.Errorf("permission denied")
	sandbox := NewSandbox(caller, 5*time.Second)

	tools := []ToolDef{{Name: "db__query"}}
	result, err := sandbox.Execute(context.Background(), `
try {
	db.query({sql: "SELECT 1"});
} catch (e) {
	print("isError:", e instanceof Error);
	print("message:", e.message);
}
`, tools)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("caught error should not surface as execution error, got: %s", result.Error)
	}
	if !strings.Contains(result.Output, "isError: true") {
		t.Errorf("thrown value should be instanceof Error, output: %s", result.Output)
	}
	if !strings.Contains(result.Output, "message: Tool call failed: db__query") {
		t.Errorf("e.message should carry the tool error text, output: %s", result.Output)
	}
	if !strings.Contains(result.Output, "permission denied") {
		t.Errorf("e.message should include the downstream error, output: %s", result.Output)
	}
}

// TestSchemaParamHint_ExtractsProperties verifies that schemaParamHint
// extracts field names, types, and required status from a JSON schema.
func TestSchemaParamHint_ExtractsProperties(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"owner": {"type": "string", "description": "Repository owner"},
			"repo": {"type": "string"},
			"private": {"type": "boolean"}
		},
		"required": ["owner", "repo"]
	}`)

	hint := schemaParamHint(schema, nil)
	if !strings.Contains(hint, "owner (string, required)") {
		t.Errorf("expected owner param hint, got: %s", hint)
	}
	if !strings.Contains(hint, "repo (string, required)") {
		t.Errorf("expected repo param hint, got: %s", hint)
	}
	if !strings.Contains(hint, "private (boolean, optional)") {
		t.Errorf("expected private param hint, got: %s", hint)
	}
	// Description for "owner" should appear if short enough.
	if !strings.Contains(hint, "Repository owner") {
		t.Errorf("expected owner description in hint, got: %s", hint)
	}
}

// TestSchemaParamHint_IncludesExample verifies that schemaParamHint
// includes a usage example when provided.
func TestSchemaParamHint_IncludesExample(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"org": {"type": "string"}
		},
		"required": ["org"]
	}`)

	hint := schemaParamHint(schema, []string{`github.list_repos({org: "acme"})`})
	if !strings.Contains(hint, "Example:") {
		t.Errorf("expected example in hint, got: %s", hint)
	}
	if !strings.Contains(hint, `github.list_repos({org: "acme"})`) {
		t.Errorf("expected example content in hint, got: %s", hint)
	}
}

// TestSchemaParamHint_EnumType verifies that enum types are rendered
// correctly in the parameter hint.
func TestSchemaParamHint_EnumType(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"state": {"type": "string", "enum": ["open", "closed", "all"]}
		}
	}`)

	hint := schemaParamHint(schema, nil)
	if !strings.Contains(hint, "state (enum(open|closed|all), optional)") {
		t.Errorf("expected enum param hint, got: %s", hint)
	}
}

// TestSchemaParamHint_NumberAndInteger verifies number and integer types.
func TestSchemaParamHint_NumberAndInteger(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer"},
			"score": {"type": "number"}
		}
	}`)

	hint := schemaParamHint(schema, nil)
	if !strings.Contains(hint, "limit (number, optional)") {
		t.Errorf("expected limit param hint, got: %s", hint)
	}
	if !strings.Contains(hint, "score (number, optional)") {
		t.Errorf("expected score param hint, got: %s", hint)
	}
}

// TestSchemaParamHint_EmptySchema verifies that empty/missing schemas
// produce no hint.
func TestSchemaParamHint_EmptySchema(t *testing.T) {
	if hint := schemaParamHint(nil, nil); hint != "" {
		t.Errorf("expected empty hint for nil schema, got: %s", hint)
	}
	if hint := schemaParamHint(json.RawMessage("{}"), nil); hint != "" {
		t.Errorf("expected empty hint for empty properties, got: %s", hint)
	}
	if hint := schemaParamHint(json.RawMessage(`{"type": "object", "properties": {}}`), nil); hint != "" {
		t.Errorf("expected empty hint for no properties, got: %s", hint)
	}
}

// TestBuildToolErrorMessage_IncludesSchemaHint verifies that
// buildToolErrorMessage appends the schema parameter hint when
// a schema is provided.
func TestBuildToolErrorMessage_IncludesSchemaHint(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Task name"},
			"priority": {"type": "integer"}
		},
		"required": ["name"]
	}`)

	msg := buildToolErrorMessage("task__create", json.RawMessage(`{}`), "name is required", schema, nil, nil)
	if !strings.Contains(msg, "Tool call failed: task__create") {
		t.Errorf("expected tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "Expected parameters:") {
		t.Errorf("expected parameter hint in error, got: %s", msg)
	}
	if !strings.Contains(msg, "name (string, required)") {
		t.Errorf("expected required field hint, got: %s", msg)
	}
	if !strings.Contains(msg, "priority (number, optional)") {
		t.Errorf("expected optional field hint, got: %s", msg)
	}
}

// TestBuildToolErrorMessage_NoSchemaNoHint verifies that
// buildToolErrorMessage omits the parameter hint section when
// no schema is provided (backward compatible).
func TestBuildToolErrorMessage_NoSchemaNoHint(t *testing.T) {
	msg := buildToolErrorMessage("api__ping", json.RawMessage(`{}`), "something broke", nil, nil, nil)
	if strings.Contains(msg, "Expected parameters:") {
		t.Errorf("should not include hint without schema, got: %s", msg)
	}
	if !strings.Contains(msg, "Tool call failed: api__ping") {
		t.Errorf("expected tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "something broke") {
		t.Errorf("expected error text, got: %s", msg)
	}
}

// TestSynthesizeExample_FromStringProperties generates an example from
// a schema with string + required fields when no explicit examples exist.
func TestSynthesizeExample_FromStringProperties(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"owner": {"type": "string"},
			"repo": {"type": "string"}
		},
		"required": ["owner", "repo"]
	}`)

	ex := synthesizeExample("github__get_repo", schema)
	if ex == "" {
		t.Fatal("expected synthesized example, got empty")
	}
	if !strings.Contains(ex, "github.get_repo({") {
		t.Errorf("expected namespace-dotted call, got: %s", ex)
	}
	if !strings.Contains(ex, "owner: ") {
		t.Errorf("expected owner param in example, got: %s", ex)
	}
	if !strings.Contains(ex, "repo: ") {
		t.Errorf("expected repo param in example, got: %s", ex)
	}
}

// TestSynthesizeExample_EnumUsesFirstValue verifies that the first enum
// value is used as the placeholder for enum types.
func TestSynthesizeExample_EnumUsesFirstValue(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"state": {"type": "string", "enum": ["open", "closed"]}
		}
	}`)

	ex := synthesizeExample("github__list_issues", schema)
	if !strings.Contains(ex, `state: "open"`) {
		t.Errorf("expected first enum value in placeholder, got: %s", ex)
	}
}

// TestSynthesizeExample_BooleanAndNumber uses type-appropriate placeholders.
func TestSynthesizeExample_BooleanAndNumber(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"private": {"type": "boolean"},
			"limit": {"type": "integer"},
			"score": {"type": "number"}
		}
	}`)

	ex := synthesizeExample("api__search", schema)
	if !strings.Contains(ex, "private: true") {
		t.Errorf("expected boolean placeholder 'true', got: %s", ex)
	}
	if !strings.Contains(ex, "limit: 0") {
		t.Errorf("expected number placeholder '0', got: %s", ex)
	}
	if !strings.Contains(ex, "score: 0") {
		t.Errorf("expected number placeholder '0', got: %s", ex)
	}
}

// TestSynthesizeExample_EmptySchema returns empty.
func TestSynthesizeExample_EmptySchema(t *testing.T) {
	if ex := synthesizeExample("tool__x", nil); ex != "" {
		t.Errorf("expected empty for nil schema, got: %s", ex)
	}
	if ex := synthesizeExample("tool__x", json.RawMessage("{}")); ex != "" {
		t.Errorf("expected empty for empty schema, got: %s", ex)
	}
	if ex := synthesizeExample("tool__x", json.RawMessage(`{"type":"object","properties":{}}`)); ex != "" {
		t.Errorf("expected empty for no properties, got: %s", ex)
	}
}

// TestSynthesizeExample_NonNamespacedToolDoesNotAddDot.
func TestSynthesizeExample_NonNamespacedTool(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"x": {"type": "string"}}
	}`)

	ex := synthesizeExample("no_ns", schema)
	if strings.Contains(ex, "no.ns") {
		t.Errorf("should not add dot for non-namespaced tools, got: %s", ex)
	}
	if !strings.Contains(ex, "no_ns({") {
		t.Errorf("expected original name in call, got: %s", ex)
	}
}

// TestBuildToolErrorMessage_SynthesizesExample verifies that when no
// explicit examples are provided, buildToolErrorMessage auto-generates
// a usage example from the schema properties.
func TestBuildToolErrorMessage_SynthesizesExample(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"org": {"type": "string", "description": "Organization name"},
			"limit": {"type": "integer"}
		},
		"required": ["org"]
	}`)

	msg := buildToolErrorMessage("github__list_repos",
		json.RawMessage(`{}`),
		"org is required",
		schema,
		nil,
		nil)

	if !strings.Contains(msg, "Example: github.list_repos({") {
		t.Errorf("expected synthesized example, got: %s", msg)
	}
}

// TestBuildToolErrorMessage_ExplicitExamplePreventsSynthesis verifies
// that explicit examples take precedence over auto-synthesis.
func TestBuildToolErrorMessage_ExplicitExamplePreventsSynthesis(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"org": {"type": "string"}},
		"required": ["org"]
	}`)

	msg := buildToolErrorMessage("github__list_repos",
		json.RawMessage(`{}`),
		"org is required",
		schema,
		[]string{`github.list_repos({org: "my-org"})`},
		nil)

	if !strings.Contains(msg, `Example: github.list_repos({org: "my-org"})`) {
		t.Errorf("expected explicit example, got: %s", msg)
	}
	if strings.Contains(msg, `"github.list_repos({org: "`) && strings.Contains(msg, `...`) {
		t.Errorf("explicit example should prevent synthesized fallback, got: %s", msg)
	}
}

// TestBuildToolErrorMessage_ExampleIncluded tests that when a schema
// with examples is provided, the usage example appears in the error.
func TestBuildToolErrorMessage_ExampleIncluded(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"org": {"type": "string"}
		},
		"required": ["org"]
	}`)

	msg := buildToolErrorMessage("github__list_repos",
		json.RawMessage(`{}`),
		"org is required",
		schema,
		[]string{`github.list_repos({org: "acme"})`},
		nil)

	if !strings.Contains(msg, "Example: github.list_repos") {
		t.Errorf("expected example in error message, got: %s", msg)
	}
}

// TestSchemaParamHint_MaxLength verifies that the hint is truncated
// when the schema has many properties that would overflow the cap.
func TestSchemaParamHint_MaxLength(t *testing.T) {
	props := make(map[string]json.RawMessage)
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("field_%d", i)
		props[name] = json.RawMessage(`{"type": "string", "description": "` + name + ` description that is quite long to eat up the byte budget quickly"}`)
	}
	data, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{},
	})

	hint := schemaParamHint(data, nil)
	if len(hint) > maxSchemaHintLen+10 {
		t.Errorf("hint exceeds maxSchemaHintLen: %d > %d", len(hint), maxSchemaHintLen)
	}
	if !strings.HasSuffix(hint, "...") {
		t.Errorf("expected truncation suffix, got: %s", hint)
	}
}

// TestSandbox_ParallelValidationThrowsRealError verifies parallel() argument
// validation also throws Error objects, not bare strings.
func TestSandbox_ParallelValidationThrowsRealError(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	result, err := sandbox.Execute(context.Background(), `
try {
	parallel("not an array");
} catch (e) {
	print("isError:", e instanceof Error);
	print("message:", e.message);
}
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "isError: true") {
		t.Errorf("thrown value should be instanceof Error, output: %s", result.Output)
	}
	if !strings.Contains(result.Output, "message: parallel() argument must be an array") {
		t.Errorf("e.message should carry validation text, output: %s", result.Output)
	}
}

func TestSandbox_Sleep(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	start := time.Now()
	result, err := sandbox.Execute(context.Background(), `sleep(100); print("woke");`, nil)
	duration := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 90*time.Millisecond {
		t.Fatalf("sleep returned too early: %s", duration)
	}
	if duration > 2*time.Second {
		t.Fatalf("sleep took too long: %s", duration)
	}
	if result.Output != "woke\n" {
		t.Fatalf("expected woke output, got %q", result.Output)
	}
}

func TestSandbox_SleepNoopForNonPositiveDurations(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 5*time.Second)

	start := time.Now()
	result, err := sandbox.Execute(context.Background(), `sleep(0); sleep(-50); print("ok");`, nil)
	duration := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if duration > 500*time.Millisecond {
		t.Fatalf("non-positive sleeps should return immediately, took %s", duration)
	}
	if result.Output != "ok\n" {
		t.Fatalf("expected ok output, got %q", result.Output)
	}
}

func TestSandbox_SleepRespectsExecutionTimeout(t *testing.T) {
	caller := newMockCaller()
	sandbox := NewSandbox(caller, 100*time.Millisecond)

	start := time.Now()
	result, err := sandbox.Execute(context.Background(), `sleep(120000); print("done");`, nil)
	duration := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if duration > 2*time.Second {
		t.Fatalf("sleep should have been interrupted by execution timeout, took %s", duration)
	}
	if !strings.HasPrefix(result.Error, "execution timed out") {
		t.Fatalf("expected execution timeout prefix, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "sleep(ms) is clamped to 60s per call") {
		t.Fatalf("expected sleep clamp note in timeout error, got %q", result.Error)
	}
	if strings.Contains(result.Output, "done") {
		t.Fatalf("script continued after timed-out sleep: %q", result.Output)
	}
}

func TestExtractCallMetaIncludesFuzzyCorrection(t *testing.T) {
	raw := json.RawMessage(`{
		"content": [{"type":"text","text":"{}"}],
		"_meta": {
			"cache": {"cached": true, "age_seconds": 12},
			"fuzzy_correction": {"original": "task.lsit", "corrected": "task.list"}
		}
	}`)

	meta := extractCallMeta(raw)
	if meta["cached"] != true {
		t.Fatalf("expected cached=true, got %#v", meta["cached"])
	}
	if meta["age_seconds"] != 12 {
		t.Fatalf("expected age_seconds=12, got %#v", meta["age_seconds"])
	}
	var correction struct {
		Original  string `json:"original"`
		Corrected string `json:"corrected"`
	}
	data, err := json.Marshal(meta["fuzzy_correction"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &correction); err != nil {
		t.Fatal(err)
	}
	if correction.Original != "task.lsit" || correction.Corrected != "task.list" {
		t.Fatalf("unexpected fuzzy correction: %+v", correction)
	}
}
