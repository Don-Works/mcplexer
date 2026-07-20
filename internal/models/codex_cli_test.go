package models

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCodexCLISendParsesJSONEnvelope(t *testing.T) {
	t.Parallel()
	fake := &fakeCodexRunner{
		stdout: []byte(`{"text":"hello from codex","stop_reason":"stop","cost_usd":0.0123,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`),
	}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)

	resp, err := a.Send(context.Background(), SendRequest{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello from codex" {
		t.Fatalf("Text = %q, want hello from codex", resp.Text)
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

func TestCodexCLISendBuildsExpectedArgs(t *testing.T) {
	t.Parallel()
	fake := &fakeCodexRunner{stdout: []byte(`{"text":"ok","usage":{"input_tokens":1,"output_tokens":2}}`)}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:        "system text",
		Messages:      []Message{{Role: RoleUser, Content: "the prompt"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	wantArgs := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--cd", "/tmp/project",
		"--model", "o3",
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

func TestCodexCLISystemPromptNotInArgs(t *testing.T) {
	t.Parallel()
	const sentinel = "CODEX_SYSTEM_CONTENT_MUST_NOT_LEAK"
	fake := &fakeCodexRunner{stdout: []byte(`{"text":"ok"}`)}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)

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

func TestCodexCLISendErrorEnvelopeBecomesError(t *testing.T) {
	t.Parallel()
	fake := &fakeCodexRunner{stdout: []byte(`{"is_error":true,"error":"auth expired"}`)}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("err = %v, want auth expired", err)
	}
}

func TestCodexCLISendBinaryFailureWrapsStderr(t *testing.T) {
	t.Parallel()
	fake := &fakeCodexRunner{err: errors.New("exit status 1"), stderr: []byte("OPENAI_API_KEY not set")}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY not set") {
		t.Fatalf("err = %v, want stderr", err)
	}
}

func TestCodexCLISendMalformedJSONIsError(t *testing.T) {
	t.Parallel()
	fake := &fakeCodexRunner{stdout: []byte("not json")}
	a := newCodexAdapterWithFakeRunner(t, "o3", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "parse output") {
		t.Fatalf("err = %v, want parse output", err)
	}
}

func TestParseCodexJSONToleratesTrailingBannerLines(t *testing.T) {
	t.Parallel()
	raw := []byte("banner\n{\"text\":\"nested ok\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n")
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "nested ok" {
		t.Fatalf("Text = %q, want nested ok", resp.Text)
	}
	if resp.InputTokens != 2 || resp.OutputTokens != 3 {
		t.Fatalf("tokens = %d/%d, want 2/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestParseCodexJSONWithoutUsageReturnsZero(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"text":"hello","stop_reason":"stop"}`)
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("Text = %q, want hello", resp.Text)
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Fatalf("tokens = %d/%d, want 0/0 (codex headless omits usage)", resp.InputTokens, resp.OutputTokens)
	}
	if resp.CostUSD != 0 {
		t.Fatalf("CostUSD = %v, want 0", resp.CostUSD)
	}
}

func TestParseCodexJSONWithTokensKey(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"result":"ok","tokens":{"input":100,"output":50}}`)
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
	if resp.InputTokens != 100 || resp.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 100/50", resp.InputTokens, resp.OutputTokens)
	}
}

func TestParseCodexJSONWithNestedMessage(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"message":{"content":"nested content","stop_reason":"end_turn"}}`)
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "nested content" {
		t.Fatalf("Text = %q, want nested content", resp.Text)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, StopEndTurn)
	}
}

func TestParseCodexJSONFlatOutputTokens(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"text":"flat","output_tokens":42}`)
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.OutputTokens != 42 {
		t.Fatalf("OutputTokens = %d, want 42", resp.OutputTokens)
	}
}

func TestParseCodexJSONEmptyIsError(t *testing.T) {
	t.Parallel()
	_, err := parseCodexJSON([]byte{})
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestCodexCLIRespectsContextCancel(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	a := &codexCLIAdapter{
		runner: func(ctx context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-gate:
				return []byte(`{"text":"ok"}`), nil, nil
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

type fakeCodexRunner struct {
	stdout       []byte
	stderr       []byte
	err          error
	gotBinary    string
	gotArgs      []string
	gotPrompt    string
	gotWorkspace string
}

func (f *fakeCodexRunner) run(_ context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	f.gotBinary = binary
	f.gotArgs = append([]string(nil), args...)
	f.gotPrompt = prompt
	f.gotWorkspace = workspacePath
	return f.stdout, f.stderr, f.err
}

func newCodexAdapterWithFakeRunner(t *testing.T, modelID string, fake *fakeCodexRunner) *codexCLIAdapter {
	t.Helper()
	a := newCodexCLIAdapter("", modelID)
	a.runner = fake.run
	return a
}

// TestParseCodexRealEventStream locks in the ACTUAL `codex exec --json` wire
// format, captured verbatim from a live codex-cli 0.144.5 run on 2026-07-20
// (thread/item ids redacted). This is the regression test for the adapter
// being wired to a format codex no longer emits: parseCodexJSON previously
// expected a flat {"text":…,"usage":{…}} envelope and returned
// "no JSON envelope in output" for every real run, so even the auth error
// was swallowed.
func TestParseCodexRealEventStream(t *testing.T) {
	t.Parallel()
	raw := []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"REDACTED-THREAD-ID"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"OK"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":13635,"cached_input_tokens":9984,"output_tokens":5,"reasoning_output_tokens":0}}`,
	}, "\n"))
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "OK" {
		t.Fatalf("Text = %q, want OK", resp.Text)
	}
	// cached_input_tokens (9984) is the cache-hit PORTION of input_tokens,
	// not an addition to it (documented OpenAI usage semantics: cached ⊆
	// input). The pre-0.144 code summed them, reporting 23619 — input
	// inflated ~73%, and every derived cost figure with it.
	if resp.InputTokens != 13635 {
		t.Fatalf("InputTokens = %d, want 13635 (cached is a subset, never added)", resp.InputTokens)
	}
	if resp.OutputTokens != 5 {
		t.Fatalf("OutputTokens = %d, want 5", resp.OutputTokens)
	}
}

// TestParseCodexRealFailureStream — captured verbatim from a live run with no
// credentials. The transient "Reconnecting…" error events must NOT abort the
// parse on their own; turn.failed is the authoritative outcome, and its
// message must reach the caller instead of a generic envelope complaint.
func TestParseCodexRealFailureStream(t *testing.T) {
	t.Parallel()
	raw := []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"REDACTED-THREAD-ID"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized)"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Falling back from WebSockets to HTTPS transport."}}`,
		`{"type":"error","message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"}`,
		`{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"}}`,
	}, "\n"))
	_, err := parseCodexJSON(raw)
	if err == nil {
		t.Fatal("expected an error for a failed turn")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("err = %v, want the real 401 surfaced", err)
	}
}

// TestParseCodexTransientErrorsDoNotFailSuccessfulTurn — a run that recovers
// after transient reconnects must still be parsed as a success.
func TestParseCodexTransientErrorsDoNotFailSuccessfulTurn(t *testing.T) {
	t.Parallel()
	raw := []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"REDACTED-THREAD-ID"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 1/5 (transient)"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"recovered"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
	}, "\n"))
	resp, err := parseCodexJSON(raw)
	if err != nil {
		t.Fatalf("transient error must not fail a completed turn: %v", err)
	}
	if resp.Text != "recovered" {
		t.Fatalf("Text = %q, want recovered", resp.Text)
	}
}

// TestParseCodexLegacyEnvelopeStillWorks — older codex builds emit a flat
// envelope; the stream parser must not have broken that path.
func TestParseCodexLegacyEnvelopeStillWorks(t *testing.T) {
	t.Parallel()
	resp, err := parseCodexJSON([]byte(`{"text":"hello","stop_reason":"stop","usage":{"input_tokens":10,"output_tokens":20}}`))
	if err != nil {
		t.Fatalf("parseCodexJSON: %v", err)
	}
	if resp.Text != "hello" || resp.InputTokens != 10 || resp.OutputTokens != 20 {
		t.Fatalf("legacy envelope regressed: %+v", resp)
	}
}
