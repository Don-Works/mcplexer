package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// MCP Spec Conformance — Code Mode Synchronous Contract
//
// Code Mode (mcpx__execute_code) binds downstream MCP tools as synchronous
// JavaScript function calls inside a Goja VM. These tests pin the CURRENT
// synchronous contract:
//
//   • ToolCaller.CallTool returns exactly ONE result (json.RawMessage) — no
//     streaming, no partial results, no progress callbacks.
//   • ExecutionResult is a complete snapshot — all tool calls are recorded
//     after execution finishes, not incrementally.
//   • There is no mechanism for the sandbox to surface progress notifications,
//     logging messages, or partial results from downstream tools to the
//     calling LLM mid-execution.
//
// These tests exist so that when async/event-aware transport support is added,
// the synchronous contract is explicitly updated rather than silently broken.
// ---------------------------------------------------------------------------

// fakeToolCaller is a test double for ToolCaller that records calls and
// returns a canned result. It demonstrates the synchronous single-result
// contract: CallTool returns one json.RawMessage, blocking until the
// downstream responds.
type fakeToolCaller struct {
	mu     sync.Mutex
	calls  []recordedCall
	result json.RawMessage
	err    error
	delay  time.Duration
}

type recordedCall struct {
	Name string
	Args json.RawMessage
}

func newFakeToolCaller(result json.RawMessage) *fakeToolCaller {
	return &fakeToolCaller{result: result}
}

func (f *fakeToolCaller) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{Name: name, Args: args})
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *fakeToolCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- ToolCaller contract: single result, no streaming ----------------------

// TestToolCaller_SingleResultContract verifies that CallTool returns exactly
// one result. The interface itself enforces this (returns json.RawMessage,
// not a channel or iterator), but this test makes the contract explicit and
// documents the gap: there is no way for a downstream tool to stream
// partial results or progress notifications back through Code Mode.
func TestToolCaller_SingleResultContract(t *testing.T) {
	cannedResult := json.RawMessage(`{"content":[{"type":"text","text":"hello"}]}`)
	caller := newFakeToolCaller(cannedResult)

	result, err := caller.CallTool(context.Background(), "test__tool", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if string(result) != string(cannedResult) {
		t.Errorf("result mismatch: got %s, want %s", result, cannedResult)
	}

	// The interface returns (json.RawMessage, error) — a single complete
	// result. There is no channel, iterator, or callback for streaming.
	// This is the synchronous contract that Code Mode relies on.
}

// --- ExecutionResult is a snapshot -----------------------------------------

// TestExecutionResult_IsCompleteSnapshot verifies that ExecutionResult
// captures ALL tool calls after execution finishes — not incrementally
// during execution. This means a script cannot observe partial progress
// from a long-running tool call.
func TestExecutionResult_IsCompleteSnapshot(t *testing.T) {
	cannedResult := json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`)
	caller := newFakeToolCaller(cannedResult)
	caller.delay = 50 * time.Millisecond // simulate a slow downstream

	sandbox := NewSandbox(caller, 5*time.Second)

	tools := []ToolDef{
		{Name: "ns__slow", Description: "slow tool"},
	}

	result, err := sandbox.Execute(context.Background(), `ns.slow({})`, tools)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ExecutionResult")
	}

	// The result is a snapshot: all tool calls are recorded.
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call in snapshot, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "ns__slow" {
		t.Errorf("tool call name = %q, want ns__slow", result.ToolCalls[0].Name)
	}
	// Duration should reflect the simulated delay.
	if result.ToolCalls[0].Duration < 40*time.Millisecond {
		t.Errorf("duration %v too short for 50ms simulated delay", result.ToolCalls[0].Duration)
	}
}

// --- Multiple tool calls are all captured in one snapshot ------------------

// TestExecutionResult_MultipleToolCalls verifies that parallel or sequential
// tool calls are all captured in the ExecutionResult snapshot.
func TestExecutionResult_MultipleToolCalls(t *testing.T) {
	cannedResult := json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
	caller := newFakeToolCaller(cannedResult)

	sandbox := NewSandbox(caller, 5*time.Second)
	tools := []ToolDef{
		{Name: "ns__a"},
		{Name: "ns__b"},
	}

	code := `ns.a({}); ns.b({});`
	result, err := sandbox.Execute(context.Background(), code, tools)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if caller.callCount() != 2 {
		t.Errorf("expected 2 tool calls, got %d", caller.callCount())
	}
	if len(result.ToolCalls) != 2 {
		t.Errorf("expected 2 tool call records, got %d", len(result.ToolCalls))
	}
}

// --- Tool call error propagation is synchronous ----------------------------

// TestExecutionResult_ErrorPropagation verifies that when a tool returns an
// error, the sandbox throws a JS exception (synchronous), and the error is
// captured in the ExecutionResult.
func TestExecutionResult_ErrorPropagation(t *testing.T) {
	caller := newFakeToolCaller(nil)
	caller.err = context.DeadlineExceeded

	sandbox := NewSandbox(caller, 5*time.Second)
	tools := []ToolDef{{Name: "ns__fail"}}

	result, err := sandbox.Execute(context.Background(), `
		try {
			ns.fail({});
		} catch(e) {
			print("caught: " + e.message);
		}
	`, tools)
	if err != nil {
		t.Fatalf("Execute returned error (should be nil, error in result): %v", err)
	}
	// The script caught the error, so result.Error should be empty.
	if result.Error != "" {
		t.Errorf("expected empty error (caught by script), got %q", result.Error)
	}
	if !strings.Contains(result.Output, "caught:") {
		t.Errorf("expected output to contain caught error, got %q", result.Output)
	}
}

// --- ExecutionResult has no progress/streaming fields ----------------------

// TestExecutionResult_NoStreamingFields verifies the ExecutionResult struct
// has no fields for progress, partial results, or streaming — it is a
// purely synchronous snapshot type.
func TestExecutionResult_NoStreamingFields(t *testing.T) {
	// Marshal an ExecutionResult and verify the JSON keys are all
	// snapshot-style (output, tool_calls, error) — no progress/streaming.
	result := &ExecutionResult{
		Output:    "test",
		ToolCalls: []ToolCallRecord{{Name: "test", Duration: time.Millisecond}},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify no streaming-related fields exist.
	streamingKeys := []string{"progress", "partial", "stream", "chunk", "delta", "incremental"}
	for _, key := range streamingKeys {
		if _, ok := m[key]; ok {
			t.Errorf("ExecutionResult should not have %q field", key)
		}
	}
}
