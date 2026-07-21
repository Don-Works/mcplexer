package models

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGrokRunner struct {
	stdout       []byte
	stderr       []byte
	err          error
	gotBinary    string
	gotArgs      []string
	gotPrompt    string
	gotWorkspace string
}

func (f *fakeGrokRunner) run(_ context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	f.gotBinary = binary
	f.gotArgs = append([]string(nil), args...)
	f.gotPrompt = prompt
	f.gotWorkspace = workspacePath
	return f.stdout, f.stderr, f.err
}

func newGrokAdapterWithFakeRunner(t *testing.T, modelID string, fake *fakeGrokRunner) *grokCLIAdapter {
	t.Helper()
	a := newGrokCLIAdapter("", modelID)
	a.runner = fake.run
	return a
}

func TestGrokCLISendParsesJSONEnvelope(t *testing.T) {
	fake := &fakeGrokRunner{
		stdout: []byte(`{"text":"hello from grok","stop_reason":"stop","cost_usd":0.0123,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`),
	}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)

	resp, err := a.Send(context.Background(), SendRequest{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello from grok" {
		t.Fatalf("Text = %q, want hello from grok", resp.Text)
	}
	if resp.InputTokens != 17 {
		t.Fatalf("InputTokens = %d, want 17", resp.InputTokens)
	}
	if resp.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", resp.OutputTokens)
	}
	if resp.CostUSD != 0.0123 {
		t.Fatalf("CostUSD = %v, want 0.0123", resp.CostUSD)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, StopEndTurn)
	}
}

func TestGrokCLISendBuildsExpectedArgs(t *testing.T) {
	fake := &fakeGrokRunner{stdout: []byte(`{"result":"ok","tokens":{"input":1,"output":2}}`)}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:        "system text",
		Messages:      []Message{{Role: RoleUser, Content: "the prompt"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.gotBinary != "grok" && !strings.HasSuffix(fake.gotBinary, "/grok") &&
		!strings.HasSuffix(fake.gotBinary, "/grok-macos-aarch64") {
		t.Fatalf("binary = %q, want grok-ish binary", fake.gotBinary)
	}
	wantArgs := []string{
		"--no-auto-update",
		"--output-format", "json",
		"--always-approve",
		"--no-alt-screen",
		"--no-memory",
		"--cwd", "/tmp/project",
		"--model", "grok-build",
	}
	if !equalArgs(fake.gotArgs, wantArgs) {
		t.Fatalf("args = %v\nwant   %v", fake.gotArgs, wantArgs)
	}
	if fake.gotPrompt != "SYSTEM: system text\n\nthe prompt" {
		t.Fatalf("prompt = %q", fake.gotPrompt)
	}
	if fake.gotWorkspace != "/tmp/project" {
		t.Fatalf("workspace = %q, want /tmp/project", fake.gotWorkspace)
	}
}

func TestGrokCLISystemPromptNotInArgs(t *testing.T) {
	const sentinel = "GROK_SYSTEM_CONTENT_MUST_NOT_LEAK"
	fake := &fakeGrokRunner{stdout: []byte(`{"text":"ok"}`)}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:   sentinel,
		Messages: []Message{{Role: RoleUser, Content: "user prompt"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for i, arg := range fake.gotArgs {
		if strings.Contains(arg, sentinel) {
			t.Fatalf("system prompt leaked into args[%d]=%q", i, arg)
		}
	}
	if !strings.Contains(fake.gotPrompt, sentinel) {
		t.Fatalf("system prompt missing from prompt payload")
	}
}

func TestGrokCLISendErrorEnvelopeBecomesError(t *testing.T) {
	fake := &fakeGrokRunner{stdout: []byte(`{"is_error":true,"error":"auth expired"}`)}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("err = %v, want auth expired", err)
	}
}

func TestGrokCLISendBinaryFailureWrapsStderr(t *testing.T) {
	fake := &fakeGrokRunner{err: errors.New("exit status 1"), stderr: []byte("login required")}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "login required") {
		t.Fatalf("err = %v, want stderr", err)
	}
}

func TestGrokCLISendMalformedJSONIsError(t *testing.T) {
	fake := &fakeGrokRunner{stdout: []byte("not json")}
	a := newGrokAdapterWithFakeRunner(t, "grok-build", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "parse output") {
		t.Fatalf("err = %v, want parse output", err)
	}
}

func TestParseGrokJSONToleratesTrailingBannerLines(t *testing.T) {
	raw := []byte("banner\n{\"message\":{\"content\":\"nested ok\"},\"tokens\":{\"input\":2,\"output\":3}}\n")
	resp, err := parseGrokJSON(raw)
	if err != nil {
		t.Fatalf("parseGrokJSON: %v", err)
	}
	if resp.Text != "nested ok" {
		t.Fatalf("Text = %q, want nested ok", resp.Text)
	}
	if resp.InputTokens != 2 || resp.OutputTokens != 3 {
		t.Fatalf("tokens = %d/%d, want 2/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestParseGrokJSONWithoutUsageReturnsZero(t *testing.T) {
	raw := []byte(`{"text":"hello","stop_reason":"stop"}`)
	resp, err := parseGrokJSON(raw)
	if err != nil {
		t.Fatalf("parseGrokJSON: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("Text = %q, want hello", resp.Text)
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Fatalf("tokens = %d/%d, want 0/0 (grok headless omits usage)", resp.InputTokens, resp.OutputTokens)
	}
	if resp.CostUSD != 0 {
		t.Fatalf("CostUSD = %v, want 0", resp.CostUSD)
	}
}

func TestParseGrokJSONWithTokensKey(t *testing.T) {
	raw := []byte(`{"result":"ok","tokens":{"input":100,"output":50}}`)
	resp, err := parseGrokJSON(raw)
	if err != nil {
		t.Fatalf("parseGrokJSON: %v", err)
	}
	if resp.InputTokens != 100 || resp.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 100/50", resp.InputTokens, resp.OutputTokens)
	}
}
