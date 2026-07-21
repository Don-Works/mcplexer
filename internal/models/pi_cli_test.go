package models

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakePiRunner records the binary, args, prompt and workspace it was
// called with and returns canned output. Lets us assert that the adapter
// wires Pi's flags correctly and routes the prompt via stdin (NOT argv),
// all WITHOUT touching a real `pi` binary or the network.
type fakePiRunner struct {
	stdout       []byte
	stderr       []byte
	err          error
	gotBinary    string
	gotArgs      []string
	gotPrompt    string
	gotWorkspace string
}

func (f *fakePiRunner) run(_ context.Context, binary string, args []string, prompt string, workspacePath string) ([]byte, []byte, error) {
	f.gotBinary = binary
	f.gotArgs = append([]string(nil), args...)
	f.gotPrompt = prompt
	f.gotWorkspace = workspacePath
	return f.stdout, f.stderr, f.err
}

func newPiAdapterWithFakeRunner(t *testing.T, modelID string, fake *fakePiRunner) *piCLIAdapter {
	t.Helper()
	a := newPiCLIAdapter("/usr/bin/pi", modelID)
	a.runner = fake.run
	return a
}

func TestPiCLISendParsesJSONLEnvelope(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{
		stdout: []byte(`{"role":"assistant","content":[{"type":"text","text":"hello from pi"}],"usage":{"input":10,"output":20,"cacheRead":3,"cacheWrite":4,"totalTokens":37},"stopReason":"stop","responseId":"chatcmpl-1","model":"local-model","provider":"local"}`),
	}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)

	resp, err := a.Send(context.Background(), SendRequest{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello from pi" {
		t.Fatalf("Text = %q, want hello from pi", resp.Text)
	}
	if resp.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want 10", resp.InputTokens)
	}
	if resp.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", resp.OutputTokens)
	}
	if resp.CostUSD != 0 {
		t.Fatalf("CostUSD = %v, want 0 (local/unknown)", resp.CostUSD)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, StopEndTurn)
	}
}

func TestPiCLISendBuildsExpectedArgsAndPromptViaStdin(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{
		stdout: []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","responseId":"chatcmpl-1"}`),
	}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:        "system text",
		Messages:      []Message{{Role: RoleUser, Content: "the prompt"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	wantFlags := []string{
		"-p", "--mode", "json", "--no-session", "--approve",
		"--thinking", "off", "--model", "local-model",
	}
	// argv is flags ONLY — the prompt rides on stdin, never argv.
	if !equalArgs(fake.gotArgs, wantFlags) {
		t.Fatalf("args = %v\nwant   %v", fake.gotArgs, wantFlags)
	}
	if !strings.Contains(fake.gotPrompt, "the prompt") || !strings.Contains(fake.gotPrompt, "system text") {
		t.Fatalf("stdin prompt = %q, want composed prompt", fake.gotPrompt)
	}
	if fake.gotWorkspace != "/tmp/project" {
		t.Fatalf("workspace = %q, want /tmp/project", fake.gotWorkspace)
	}
}

// TestPiCLIThinkingOffMandatory guards the empirically-learned invariant:
// Pi loops forever in its reasoning trace against local models without
// `--thinking off`. The flag must always be present.
func TestPiCLIThinkingOffMandatory(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{stdout: []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","responseId":"r1"}`)}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)
	if _, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var sawThinking, sawOff bool
	for i, arg := range fake.gotArgs {
		if arg == "--thinking" {
			sawThinking = true
			if i+1 < len(fake.gotArgs) && fake.gotArgs[i+1] == "off" {
				sawOff = true
			}
		}
	}
	if !sawThinking || !sawOff {
		t.Fatalf("args %v missing mandatory --thinking off", fake.gotArgs)
	}
}

// TestPiCLISystemPromptNotInArgv is the L7 regression guard: the system
// (and user) prompt must NEVER appear in argv — argv is world-readable via
// `ps`. It must ride on stdin only, matching claude_cli/codex_cli.
func TestPiCLISystemPromptNotInArgv(t *testing.T) {
	t.Parallel()
	const sysSentinel = "PI_SYSTEM_CONTENT_MUST_NOT_LEAK"
	const userSentinel = "PI_USER_CONTENT_MUST_NOT_LEAK"
	fake := &fakePiRunner{stdout: []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","responseId":"r1"}`)}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)

	_, err := a.Send(context.Background(), SendRequest{
		System:   sysSentinel,
		Messages: []Message{{Role: RoleUser, Content: userSentinel}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// No part of the prompt — system OR user — may appear in ANY argv element.
	for i, arg := range fake.gotArgs {
		if strings.Contains(arg, sysSentinel) {
			t.Fatalf("system prompt leaked into argv[%d]=%q (L7 regression)", i, arg)
		}
		if strings.Contains(arg, userSentinel) {
			t.Fatalf("user prompt leaked into argv[%d]=%q (L7 regression)", i, arg)
		}
	}
	// Both must be present in the stdin payload.
	if !strings.Contains(fake.gotPrompt, sysSentinel) || !strings.Contains(fake.gotPrompt, userSentinel) {
		t.Fatalf("prompt missing from stdin payload: %q", fake.gotPrompt)
	}
}

func TestPiCLIHTTPSEndpointUsesBinaryPath(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{stdout: []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","responseId":"r1"}`)}
	a := newPiCLIAdapter("/custom/bin/pi", "local-model")
	a.runner = fake.run
	if _, err := a.Send(context.Background(), SendRequest{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.gotBinary != "/custom/bin/pi" {
		t.Fatalf("binary = %q, want /custom/bin/pi", fake.gotBinary)
	}
}

func TestPiCLISendBinaryFailureWrapsStderr(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{err: errors.New("exit status 1"), stderr: []byte("model not configured in ~/.pi")}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "model not configured") {
		t.Fatalf("err = %v, want stderr", err)
	}
}

func TestPiCLISendMalformedJSONIsError(t *testing.T) {
	t.Parallel()
	fake := &fakePiRunner{stdout: []byte("not json")}
	a := newPiAdapterWithFakeRunner(t, "local-model", fake)
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "parse output") {
		t.Fatalf("err = %v, want parse output", err)
	}
}

func TestPiCLIContextCancelledAddsCtxReason(t *testing.T) {
	t.Parallel()
	a := newPiAdapterWithFakeRunner(t, "local-model", &fakePiRunner{})
	a.runner = func(ctx context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return nil, []byte("killed"), ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Send(ctx, SendRequest{})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v, want context canceled in message", err)
	}
}

func TestPiCLIRespectsContextCancel(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	a := &piCLIAdapter{
		runner: func(ctx context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-gate:
				return []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop","responseId":"r1"}`), nil, nil
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Send(ctx, SendRequest{Messages: []Message{{Role: RoleUser, Content: "block forever"}}})
	if err == nil {
		t.Fatal("expected context-cancel error")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("Send took too long, expected prompt return on context cancel")
	}
}
