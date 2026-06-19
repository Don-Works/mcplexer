package models

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeClaudeRunner records the args + stdin it was called with and
// returns canned output. Lets us assert that the adapter wires the
// CLI flags correctly without touching a real `claude` binary.
type fakeClaudeRunner struct {
	stdout    []byte
	stderr    []byte
	err       error
	gotBinary string
	gotArgs   []string
	gotStdin  string
}

func (f *fakeClaudeRunner) run(_ context.Context, binary string, args []string, stdin string, _ string) ([]byte, []byte, error) {
	f.gotBinary = binary
	f.gotArgs = args
	f.gotStdin = stdin
	return f.stdout, f.stderr, f.err
}

func newAdapterWithFakeRunner(t *testing.T, modelID string, fake *fakeClaudeRunner) *claudeCLIAdapter {
	t.Helper()
	a := newClaudeCLIAdapter("", modelID)
	a.runner = fake.run
	return a
}

func TestClaudeCLISendParsesResultEnvelope(t *testing.T) {
	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","subtype":"success","is_error":false,"result":"hello back","stop_reason":"end_turn","total_cost_usd":0.0421,"usage":{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5,"cache_creation_input_tokens":7}}`),
	}
	a := newAdapterWithFakeRunner(t, "claude-haiku-4-5", fake)

	resp, err := a.Send(context.Background(), SendRequest{
		System:   "you are terse",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello back" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello back")
	}
	// InputTokens folds cache_read + cache_creation in (mirrors anthropic adapter).
	if resp.InputTokens != 24 {
		t.Errorf("InputTokens = %d, want 24", resp.InputTokens)
	}
	if resp.OutputTokens != 34 {
		t.Errorf("OutputTokens = %d, want 34", resp.OutputTokens)
	}
	if resp.CostUSD != 0.0421 {
		t.Errorf("CostUSD = %v, want 0.0421", resp.CostUSD)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, StopEndTurn)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty (cli orchestrates its own tools)", resp.ToolCalls)
	}
}

func TestClaudeCLISendBuildsExpectedArgs(t *testing.T) {
	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":false,"result":"ok","stop_reason":"end_turn","total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1}}`),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:   "system text",
		Messages: []Message{{Role: RoleUser, Content: "the prompt"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// newClaudeCLIAdapter now resolves bare names via exec.LookPath so
	// sandbox-exec (which ignores parent PATH) can locate the binary.
	// Accept either the bare name (LookPath miss) or an absolute path
	// ending in /claude (LookPath hit) — both are correct behaviour.
	if fake.gotBinary != "claude" && !strings.HasSuffix(fake.gotBinary, "/claude") {
		t.Errorf("binary = %q, want claude or absolute path ending in /claude", fake.gotBinary)
	}
	// System prompt is now prepended onto stdin (L7 fix: do not leak
	// system prompt via `ps auxww`). Argv no longer carries it.
	wantStdin := "SYSTEM: system text\n\nthe prompt"
	if fake.gotStdin != wantStdin {
		t.Errorf("stdin = %q, want %q", fake.gotStdin, wantStdin)
	}
	want := []string{"-p", "--output-format", "json", "--tools", "default", "--no-session-persistence", "--dangerously-skip-permissions", "--model", "sonnet"}
	if !equalArgs(fake.gotArgs, want) {
		t.Errorf("args = %v\nwant   %v", fake.gotArgs, want)
	}
}

// TestClaudeCLISystemPromptNotInArgs asserts that the system prompt
// content never lands in argv. argv is world-readable via `ps auxww`
// on multi-tenant boxes; the system prompt may contain skill bodies or
// operator instructions, so it must travel via stdin only. Regression
// guard for L7 in the security audit.
func TestClaudeCLISystemPromptNotInArgs(t *testing.T) {
	const secretSentinel = "OPERATOR_ONLY_SYSTEM_CONTENT_SHOULD_NOT_LEAK"
	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":false,"result":"ok","stop_reason":"end_turn","total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1}}`),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:   secretSentinel,
		Messages: []Message{{Role: RoleUser, Content: "user prompt"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for i, a := range fake.gotArgs {
		if strings.Contains(a, secretSentinel) {
			t.Errorf("system prompt leaked into args[%d] = %q", i, a)
		}
		if a == "--append-system-prompt" {
			t.Errorf("--append-system-prompt must not appear in argv (leaks via ps); args = %v", fake.gotArgs)
		}
	}
	// Sanity: stdin should still carry it.
	if !strings.Contains(fake.gotStdin, secretSentinel) {
		t.Errorf("system prompt missing from stdin; got %q", fake.gotStdin)
	}
}

func TestClaudeCLISendErrorEnvelopeBecomesError(t *testing.T) {
	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":true,"result":"rate limited","total_cost_usd":0,"usage":{}}`),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %v, want one containing rate-limit text", err)
	}
}

func TestClaudeCLISendBinaryFailureWrapsStderr(t *testing.T) {
	fake := &fakeClaudeRunner{
		err:    errors.New("exit status 1"),
		stderr: []byte("oauth credentials missing"),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "oauth credentials missing") {
		t.Errorf("err %q does not include stderr", err.Error())
	}
}

func TestClaudeCLISendMalformedJSONIsError(t *testing.T) {
	fake := &fakeClaudeRunner{stdout: []byte(`not json at all`)}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "decode result envelope") {
		t.Errorf("err %q does not mention decode failure", err.Error())
	}
}

func TestFlattenMessagesSingleUserIsRaw(t *testing.T) {
	got := flattenMessagesForCLI([]Message{{Role: RoleUser, Content: "just this"}})
	if got != "just this" {
		t.Errorf("got %q, want %q", got, "just this")
	}
}

func TestFlattenMessagesMultiTurnUsesRolePrefix(t *testing.T) {
	got := flattenMessagesForCLI([]Message{
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "reply"},
		{Role: RoleUser, Content: "follow up"},
	})
	want := "USER: first\n\nASSISTANT: reply\n\nUSER: follow up"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
