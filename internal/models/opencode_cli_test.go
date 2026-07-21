package models

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeCLIHTTPSEndpointAttachesToServer(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"server mode ok"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":3},"cost":0.001}}`,
	}, "\n")

	var gotBinary string
	var gotArgs []string
	var gotWorkspace string
	a := newOpenCodeCLIAdapter("http://127.0.0.1:4098", "minimax/MiniMax-M3")
	a.runner = func(_ context.Context, binary string, args []string, _ string, workspacePath string) ([]byte, []byte, error) {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		gotWorkspace = workspacePath
		return []byte(good), nil, nil
	}
	resp, err := a.Send(context.Background(), SendRequest{
		Messages:      []Message{{Role: RoleUser, Content: "ping"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "server mode ok" {
		t.Fatalf("Text=%q, want server mode ok", resp.Text)
	}
	if gotBinary == "" || gotBinary == "http://127.0.0.1:4098" {
		t.Fatalf("binary = %q, want resolved opencode binary/client", gotBinary)
	}
	wantArgs := []string{
		"run", "--format", "json",
		"--attach", "http://127.0.0.1:4098",
		"--dir", "/tmp/project",
		"--model", "minimax/MiniMax-M3",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotWorkspace != "/tmp/project" {
		t.Fatalf("workspace = %q, want /tmp/project", gotWorkspace)
	}
}

func TestOpenCodeCLIBinaryEndpointDoesNotAttach(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"binary mode ok"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":3}}}`,
	}, "\n")

	var gotBinary string
	var gotArgs []string
	a := newOpenCodeCLIAdapter("/custom/bin/opencode", "minimax/MiniMax-M3")
	a.runner = func(_ context.Context, binary string, args []string, _ string, _ string) ([]byte, []byte, error) {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		return []byte(good), nil, nil
	}
	if _, err := a.Send(context.Background(), SendRequest{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBinary != "/custom/bin/opencode" {
		t.Fatalf("binary = %q, want /custom/bin/opencode", gotBinary)
	}
	for _, arg := range gotArgs {
		if arg == "--attach" {
			t.Fatalf("binary endpoint unexpectedly used --attach: %#v", gotArgs)
		}
	}
}

func TestOpenCodeCLIRespectsContextCancel(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	a := &opencodeCLIAdapter{
		runner: func(ctx context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-gate:
				return []byte(`{"type":"step_finish","part":{"reason":"stop"}}`), nil, nil
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.Send(ctx, SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "block forever"}},
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected context-cancel error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Send took %v, expected prompt return on context cancel", elapsed)
	}
}

// TestOpenCodeCLIRetriesOnEmptyText covers the intermittent opencode
// teardown bug behind the stuck Telegram "💭" bubble: opencode (≤1.15.12)
// sometimes exits after a tool call BEFORE flushing the model's final
// assistant text, so a successful run parses to empty Text (delivered as a
// blank/missing reply). The adapter must retry ONCE and surface the
// recovered reply on the second, clean attempt.
func TestOpenCodeCLIRetriesOnEmptyText(t *testing.T) {
	t.Parallel()
	// Attempt 1: truncated after a tool call — only whitespace text, ends on
	// a reasoning part with no terminal "stop". Parses to empty Text with
	// output tokens > 0 (so it is NOT a parse error, just a blank reply).
	truncated := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"\n\n"}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"output":54}}}`,
		`{"type":"step_start"}`,
		`{"type":"reasoning","part":{"type":"reasoning","text":"about to answer"}}`,
	}, "\n")
	// Attempt 2: clean completion with a terminal stop + real text.
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"It printed hello."}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"output":5}}}`,
	}, "\n")

	var calls int
	a := &opencodeCLIAdapter{
		modelID: "lmstudio/qwen3.6-35b-a3b",
		runner: func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			calls++
			if calls == 1 {
				return []byte(truncated), nil, nil
			}
			return []byte(good), nil, nil
		},
	}
	resp, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "run echo hello"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 attempts (1 retry on empty text), got %d", calls)
	}
	if resp.Text != "It printed hello." {
		t.Errorf("retry did not recover the reply: Text=%q", resp.Text)
	}
}

// TestOpenCodeCLINoRetryWhenTextPresent guards the happy path: a first
// attempt that yields text must NOT trigger a second (wasteful, and on a
// write-capable worker, potentially double-acting) invocation.
func TestOpenCodeCLINoRetryWhenTextPresent(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"pong"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"output":1}}}`,
	}, "\n")
	var calls int
	a := &opencodeCLIAdapter{
		runner: func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			calls++
			return []byte(good), nil, nil
		},
	}
	resp, err := a.Send(context.Background(), SendRequest{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if calls != 1 {
		t.Errorf("happy path must not retry, got %d calls", calls)
	}
	if resp.Text != "pong" {
		t.Errorf("Text=%q, want pong", resp.Text)
	}
}

func TestOpenCodeCLIRetriesTransientServerError(t *testing.T) {
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"recovered"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"output":1}}}`,
	}, "\n")

	var calls int
	a := &opencodeCLIAdapter{
		modelID: "minimax/MiniMax-M3",
		runner: func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			calls++
			if calls == 1 {
				return nil, []byte("Error: Session not found"), errors.New("exit status 1")
			}
			return []byte(good), nil, nil
		},
	}
	resp, err := a.Send(context.Background(), SendRequest{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if resp.Text != "recovered" {
		t.Fatalf("Text = %q, want recovered", resp.Text)
	}
}

// TestParseOpenCodeNDJSONDropsToolUseStepText covers the prelude-noise
// fix: opencode emits OpenAI-style "tool-calls" (hyphen) as the reason
// when a step ended in tool invocation. Text emitted in those steps is
// mid-run narration ("Now I have all the data, let me write the
// report.") and must be discarded from output_text; only the terminal
// step's text is the final answer the user / mesh consumer sees.
//
// The accumulation half also matters: tokens must sum across every
// step_finish, not just the last (the earlier overwrite behaviour
// silently under-counted multi-step runs).
func TestParseOpenCodeNDJSONDropsToolUseStepText(t *testing.T) {
	t.Parallel()
	// Synthetic three-step NDJSON: two tool-use steps (each with a
	// "thinking aloud" text fragment) followed by the terminal step
	// whose text is the actual reply.
	raw := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"Now let me pull the data."}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":100,"output":10,"cache":{"read":50,"write":0}},"cost":0.001}}`,
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"All data collected. Now write the report."}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":200,"output":20,"cache":{"read":0,"write":0}},"cost":0.002}}`,
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"Prod Pulse 2026-05-21\nIncidents: 0\nFull report: /tmp/x.md"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":50,"output":30,"cache":{"read":0,"write":0}},"cost":0.003}}`,
	}, "\n")

	resp, err := parseOpenCodeNDJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parseOpenCodeNDJSON: %v", err)
	}

	if strings.Contains(resp.Text, "Now let me pull") || strings.Contains(resp.Text, "All data collected") {
		t.Errorf("output_text leaks tool-use-step narration:\n%s", resp.Text)
	}
	if !strings.Contains(resp.Text, "Prod Pulse 2026-05-21") {
		t.Errorf("output_text dropped terminal step content:\n%s", resp.Text)
	}

	// Tokens must accumulate across all 3 steps:
	// input  = (100+50) + (200+0) + (50+0) = 400
	// output = 10 + 20 + 30 = 60
	// cost   = 0.001 + 0.002 + 0.003 = 0.006
	if resp.InputTokens != 400 {
		t.Errorf("InputTokens = %d, want 400 (accumulated across 3 steps)", resp.InputTokens)
	}
	if resp.OutputTokens != 60 {
		t.Errorf("OutputTokens = %d, want 60 (accumulated across 3 steps)", resp.OutputTokens)
	}
	if resp.CostUSD < 0.0059 || resp.CostUSD > 0.0061 {
		t.Errorf("CostUSD = %v, want ~0.006", resp.CostUSD)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("StopReason = %q, want %q (terminal step ended in 'stop')", resp.StopReason, StopEndTurn)
	}
}

// TestParseOpenCodeNDJSONTerminalToolStepFallsBack covers the empty-reply
// bug behind "Telegram bot not getting replies": when the run TERMINATES on
// a tool-call step (the model put its final answer in the same turn as its
// last tool call, then opencode ended without a closing "stop" step), the
// dedupe logic would drop that text and return output_text="" — which the
// concierge delivered to Telegram as a blank (i.e. missing) reply. The
// parser must fall back to the last tool-step text rather than return empty.
func TestParseOpenCodeNDJSONTerminalToolStepFallsBack(t *testing.T) {
	t.Parallel()
	// Two tool-use steps, no terminal "stop" step. The second carries the
	// model's actual answer alongside its final tool call.
	raw := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"Let me check the CRM."}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":100,"output":10,"cache":{"read":0,"write":0}},"cost":0.001}}`,
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"Done — added Chris Houston to the CRM."}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":200,"output":20,"cache":{"read":0,"write":0}},"cost":0.002}}`,
	}, "\n")

	resp, err := parseOpenCodeNDJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parseOpenCodeNDJSON: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("output_text is empty — run terminated on a tool-call step and the answer was dropped (the no-reply bug)")
	}
	if !strings.Contains(resp.Text, "added Chris Houston") {
		t.Errorf("fallback should surface the last tool-step text, got: %q", resp.Text)
	}
	// The earlier narration is NOT the terminal step, so it must not leak.
	if strings.Contains(resp.Text, "Let me check the CRM") {
		t.Errorf("fallback leaked earlier prelude narration: %q", resp.Text)
	}
}

// TestParseOpenCodeNDJSONEmptyReturnsEmptyNotError covers the
// opencode-returned-nothing case (no final text, no token data) — an
// aborted/truncated run. The parser must NOT treat this as a hard error:
// it returns an empty response so Send can retry and, failing that, the
// output layer can surface a visible fallback. (Previously this errored,
// which on a failed run skipped the placeholder-resolution path and left a
// Telegram "💭" bubble hung forever.)
func TestParseOpenCodeNDJSONEmptyReturnsEmptyNotError(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"step_finish","part":{"reason":"tool-calls"}}`,
	}, "\n")
	resp, err := parseOpenCodeNDJSON([]byte(raw))
	if err != nil {
		t.Fatalf("empty stream must not error, got: %v", err)
	}
	if resp == nil || resp.Text != "" {
		t.Errorf("expected empty Text, got %+v", resp)
	}
}

// TestParseOpenCodeNDJSONSingleStepKeepsText guards the trivial case:
// one step ending in "stop" with text content. Output must round-trip
// the text verbatim — the dedupe logic must not be over-eager.
func TestParseOpenCodeNDJSONSingleStepKeepsText(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"pong"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":1,"cache":{"read":0,"write":0}}}}`,
	}, "\n")

	resp, err := parseOpenCodeNDJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parseOpenCodeNDJSON: %v", err)
	}
	if resp.Text != "pong" {
		t.Errorf("Text = %q, want %q", resp.Text, "pong")
	}
}

// TestParseOpenCodeNDJSONTrailingTextWithoutStepFinish covers the
// truncated-stream edge case: text emitted but never closed by a
// step_finish (e.g. opencode killed mid-stream). The parser must still
// surface that text rather than silently drop it — partial output is
// more useful than empty output for failure-diagnostic purposes.
func TestParseOpenCodeNDJSONTrailingTextWithoutStepFinish(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"partial reply before kill"}}`,
		// no step_finish — stream was cut
	}, "\n")

	resp, err := parseOpenCodeNDJSON([]byte(raw))
	if err == nil && resp.Text != "partial reply before kill" {
		t.Errorf("Text = %q, want partial text to be preserved", resp.Text)
	}
	// We accept either: (a) the partial text round-trips, or (b) an
	// error with "no text or token data" — the contract is "don't
	// silently drop trailing text". Both observed behaviours satisfy
	// that. The (a) path is what the current implementation does.
}

// TestIsTransientOpenCodeError classifies which adapter errors are worth
// retrying (server crashed / session vanished) versus fatal.
func TestIsTransientOpenCodeError(t *testing.T) {
	t.Parallel()
	transient := []string{
		"opencode_cli: run: exit status 1 (stderr: Error: Session not found)",
		"opencode_cli: run: dial tcp 127.0.0.1:4096: connect: connection refused",
		"opencode_cli: run: unexpected EOF",
		"opencode_cli: run: exit status 1 (stderr: Error: Unexpected error database is locked)",
		"post: connection reset by peer",
	}
	for _, msg := range transient {
		if !isTransientOpenCodeError(errors.New(msg)) {
			t.Errorf("expected transient: %q", msg)
		}
	}
	fatal := []string{
		"opencode_cli: parse output: invalid json",
		"opencode_cli: run: exit status 1 (stderr: model not found: glm-99)",
		"context canceled",
	}
	for _, msg := range fatal {
		if isTransientOpenCodeError(errors.New(msg)) {
			t.Errorf("expected fatal (no retry): %q", msg)
		}
	}
	if isTransientOpenCodeError(nil) {
		t.Error("nil error must not be transient")
	}
}

// TestOpenCodeCLIRecoversFromSessionNotFound proves a single mid-run
// "Session not found" (the managed server crashed + was restarted by its
// supervisor) is retried and recovers, instead of killing the worker run.
func TestOpenCodeCLIRecoversFromSessionNotFound(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"recovered"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"output":2}}}`,
	}, "\n")
	var calls int
	a := &opencodeCLIAdapter{
		modelID: "zai-coding-plan/glm-5.1",
		runner: func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			calls++
			if calls == 1 {
				return nil, []byte("Error: Session not found"), errors.New("exit status 1")
			}
			return []byte(good), nil, nil
		},
	}
	resp, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Send should recover after transient session error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 1 transient retry (2 calls), got %d", calls)
	}
	if resp.Text != "recovered" {
		t.Errorf("Text=%q, want recovered", resp.Text)
	}
}
