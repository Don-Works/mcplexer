package models

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGeminiCLISendParsesJSONEnvelope(t *testing.T) {
	t.Parallel()
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return []byte(`{"response":"hello from gemini","stop_reason":"stop","usage":{"input_tokens":10,"output_tokens":20}}`), nil, nil
	}
	resp, err := a.Send(context.Background(), SendRequest{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello from gemini" {
		t.Fatalf("Text = %q, want hello from gemini", resp.Text)
	}
	if resp.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want 10", resp.InputTokens)
	}
	if resp.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", resp.OutputTokens)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, StopEndTurn)
	}
}

func TestGeminiCLISendBuildsExpectedArgs(t *testing.T) {
	t.Parallel()
	var gotArgs []string
	var gotPrompt string
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, args []string, prompt string, _ string) ([]byte, []byte, error) {
		gotArgs = append([]string(nil), args...)
		gotPrompt = prompt
		return []byte(`{"response":"ok"}`), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{
		System:        "system text",
		Messages:      []Message{{Role: RoleUser, Content: "the prompt"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	wantArgs := []string{
		"--output-format", "json",
		"--sandbox", "false",
		"--directory", "/tmp/project",
		"--model", "gemini-2.5-pro",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %v\nwant   %v", gotArgs, wantArgs)
	}
	if gotPrompt == "" || !strings.Contains(gotPrompt, "the prompt") || !strings.Contains(gotPrompt, "system text") {
		t.Fatalf("prompt did not include rendered content: %q", gotPrompt)
	}
}

func TestGeminiCLIHTTPSEndpointUsesBinaryPath(t *testing.T) {
	t.Parallel()
	var gotBinary string
	a := newGeminiCLIAdapter("/custom/bin/gemini", "gemini-2.5-flash")
	a.runner = func(_ context.Context, binary string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		gotBinary = binary
		return []byte(`{"response":"ok"}`), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBinary != "/custom/bin/gemini" {
		t.Fatalf("binary = %q, want /custom/bin/gemini", gotBinary)
	}
}

func TestGeminiCLISendBinaryFailureWrapsStderr(t *testing.T) {
	t.Parallel()
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return nil, []byte("API key invalid"), errors.New("exit status 1")
	}
	_, err := a.Send(context.Background(), SendRequest{})
	if err == nil || !strings.Contains(err.Error(), "API key invalid") {
		t.Fatalf("err = %v, want stderr", err)
	}
}

func TestGeminiCLISendMalformedJSONIsError(t *testing.T) {
	t.Parallel()
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return []byte("not json"), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{})
	if err == nil || !strings.Contains(err.Error(), "parse output") {
		t.Fatalf("err = %v, want parse output", err)
	}
}

func TestGeminiCLISendErrorEnvelopeBecomesError(t *testing.T) {
	t.Parallel()
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return []byte(`{"is_error":true,"error":"auth expired"}`), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("err = %v, want auth expired", err)
	}
}

func TestGeminiCLIContextCancelledAddsCtxReason(t *testing.T) {
	t.Parallel()
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
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

func TestGeminiCLISystemPromptNotInArgs(t *testing.T) {
	t.Parallel()
	const sentinel = "GEMINI_SYSTEM_CONTENT_MUST_NOT_LEAK"
	a := newGeminiCLIAdapter("/usr/bin/gemini", "gemini-2.5-pro")
	a.runner = func(_ context.Context, _ string, args []string, prompt string, _ string) ([]byte, []byte, error) {
		for i, arg := range args {
			if strings.Contains(arg, sentinel) {
				t.Errorf("system prompt leaked into args[%d]=%q", i, arg)
			}
		}
		if !strings.Contains(prompt, sentinel) {
			t.Errorf("system prompt missing from prompt payload")
		}
		return []byte(`{"response":"ok"}`), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{
		System:   sentinel,
		Messages: []Message{{Role: RoleUser, Content: "user prompt"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestParseGeminiJSONToleratesTrailingBannerLines(t *testing.T) {
	raw := []byte("banner\n{\"response\":\"nested ok\",\"tokens\":{\"input\":2,\"output\":3}}\n")
	resp, err := parseGeminiJSON(raw)
	if err != nil {
		t.Fatalf("parseGeminiJSON: %v", err)
	}
	if resp.Text != "nested ok" {
		t.Fatalf("Text = %q, want nested ok", resp.Text)
	}
	if resp.InputTokens != 2 || resp.OutputTokens != 3 {
		t.Fatalf("tokens = %d/%d, want 2/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestParseGeminiJSONWithoutUsageReturnsZero(t *testing.T) {
	raw := []byte(`{"response":"hello","stop_reason":"stop"}`)
	resp, err := parseGeminiJSON(raw)
	if err != nil {
		t.Fatalf("parseGeminiJSON: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("Text = %q, want hello", resp.Text)
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Fatalf("tokens = %d/%d, want 0/0", resp.InputTokens, resp.OutputTokens)
	}
	if resp.CostUSD != 0 {
		t.Fatalf("CostUSD = %v, want 0", resp.CostUSD)
	}
}

func TestParseGeminiJSONWithPromptTokens(t *testing.T) {
	raw := []byte(`{"response":"ok","usage":{"prompt_tokens":100,"completion_tokens":50}}`)
	resp, err := parseGeminiJSON(raw)
	if err != nil {
		t.Fatalf("parseGeminiJSON: %v", err)
	}
	if resp.InputTokens != 100 || resp.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 100/50", resp.InputTokens, resp.OutputTokens)
	}
}
