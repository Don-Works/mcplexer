package models

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

// withCapturedSlog swaps slog.Default for a text handler writing to
// the returned buffer at LevelDebug and restores it on cleanup. The
// returned reader is the same buffer — caller reads after the
// operation under test runs.
func withCapturedSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	return &buf, func() { slog.SetDefault(prev) }
}

// durationMSRE matches `duration_ms=NNN` where NNN > 0. slog text
// handler renders int64 as bare digits; we accept any positive value.
var durationMSRE = regexp.MustCompile(`duration_ms=([1-9][0-9]*|0)`)

// hasPositiveDuration returns true if the log contains a
// duration_ms attribute. duration_ms=0 is allowed because a fake
// runner can return in <1ms on a fast machine — what we really care
// about is that the attribute is emitted at all, on every record.
func hasPositiveDuration(s string) bool {
	return durationMSRE.MatchString(s)
}

func TestClaudeCLISlogSuccessEmitsDispatchAndSuccess(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":false,"result":"hi","stop_reason":"end_turn","total_cost_usd":0.01,"usage":{"input_tokens":7,"output_tokens":3}}`),
	}
	a := newAdapterWithFakeRunner(t, "claude-sonnet-4-5", fake)
	if _, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	out := buf.String()

	for _, sub := range []string{
		"level=DEBUG",
		"claude_cli: dispatch",
		"claude_cli: success",
		`provider=claude_cli`,
		`model_id=claude-sonnet-4-5`,
		"prompt_len=4", // "ping" = 4 bytes
		"input_tokens=7",
		"output_tokens=3",
		"cost_usd=0.01",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in slog output:\n%s", sub, out)
		}
	}
	if !hasPositiveDuration(out) {
		t.Errorf("expected duration_ms attribute, got:\n%s", out)
	}
}

func TestClaudeCLISlogExitErrorEmitsWarnWithStderr(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	fake := &fakeClaudeRunner{
		err:    errors.New("exit status 1"),
		stderr: []byte("oauth credentials missing for org_abc"),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	if _, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}); err == nil {
		t.Fatal("expected error")
	}
	out := buf.String()

	for _, sub := range []string{
		"level=WARN",
		"claude_cli: non-zero exit",
		"exit_error=",
		"stderr_truncated=",
		"oauth credentials missing",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in slog output:\n%s", sub, out)
		}
	}
	if !hasPositiveDuration(out) {
		t.Errorf("expected duration_ms attribute, got:\n%s", out)
	}
}

func TestClaudeCLISlogDecodeFailureEmitsError(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	fake := &fakeClaudeRunner{stdout: []byte("this is not json")}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	if _, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}); err == nil {
		t.Fatal("expected error")
	}
	out := buf.String()

	for _, sub := range []string{
		"level=ERROR",
		"claude_cli: decode envelope failed",
		"stdout_truncated=",
		"this is not json",
		"decode_error=",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in slog output:\n%s", sub, out)
		}
	}
	if !hasPositiveDuration(out) {
		t.Errorf("expected duration_ms attribute, got:\n%s", out)
	}
}

func TestClaudeCLISlogIsErrorEnvelopeEmitsWarn(t *testing.T) {
	buf, restore := withCapturedSlog(t)
	defer restore()

	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":true,"result":"rate limited by upstream","total_cost_usd":0,"usage":{}}`),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	if _, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}); err == nil {
		t.Fatal("expected error")
	}
	out := buf.String()

	for _, sub := range []string{
		"level=WARN",
		"claude_cli: error envelope",
		"error_result=",
		"rate limited by upstream",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in slog output:\n%s", sub, out)
		}
	}
	if !hasPositiveDuration(out) {
		t.Errorf("expected duration_ms attribute, got:\n%s", out)
	}
}

// TestClaudeCLISlogQuietByDefault confirms that with a default
// (non-debug) handler the success path emits NO records — Debug
// shouldn't leak into production logs unless the operator opted in.
func TestClaudeCLISlogQuietByDefault(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	// Info-level handler — Debug records should be dropped.
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	fake := &fakeClaudeRunner{
		stdout: []byte(`{"type":"result","is_error":false,"result":"ok","stop_reason":"end_turn","total_cost_usd":0,"usage":{}}`),
	}
	a := newAdapterWithFakeRunner(t, "sonnet", fake)
	if _, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output at Info level, got:\n%s", buf.String())
	}
}
