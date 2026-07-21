package models

import (
	"strings"
	"testing"
)

func TestParsePiJSONDedupesByResponseIDAndSkipsPartials(t *testing.T) {
	t.Parallel()
	// Two streaming partials (same responseId, empty stopReason) then the
	// finalized line. Only the finalized line counts; partials are skipped.
	raw := []byte(strings.Join([]string{
		`{"role":"assistant","content":[{"type":"text","text":"hel"}],"usage":{"input":5,"output":1},"responseId":"chatcmpl-A"}`,
		`{"role":"assistant","content":[{"type":"text","text":"hello"}],"usage":{"input":5,"output":2},"responseId":"chatcmpl-A"}`,
		`{"role":"assistant","content":[{"type":"text","text":"hello world"}],"usage":{"input":12,"output":8},"stopReason":"stop","responseId":"chatcmpl-A"}`,
	}, "\n"))
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "hello world" {
		t.Fatalf("Text = %q, want hello world", resp.Text)
	}
	if resp.InputTokens != 12 || resp.OutputTokens != 8 {
		t.Fatalf("tokens = %d/%d, want 12/8 (only finalized line counted)", resp.InputTokens, resp.OutputTokens)
	}
}

func TestParsePiJSONSumsAcrossFinalizedMessages(t *testing.T) {
	t.Parallel()
	// Two distinct finalized assistant turns (different responseIds) — a
	// tool-use turn followed by the final text turn. Usage sums; Text is
	// the LAST finalized message's text blocks only.
	raw := []byte(strings.Join([]string{
		`{"role":"assistant","content":[{"type":"toolCall","name":"read_file"}],"usage":{"input":100,"output":10},"stopReason":"toolUse","responseId":"chatcmpl-1"}`,
		`{"role":"user","content":[{"type":"toolResult","text":"file body"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"the answer"}],"usage":{"input":120,"output":40},"stopReason":"stop","responseId":"chatcmpl-2"}`,
	}, "\n"))
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "the answer" {
		t.Fatalf("Text = %q, want the answer", resp.Text)
	}
	if resp.InputTokens != 220 || resp.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 220/50", resp.InputTokens, resp.OutputTokens)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
	}
}

func TestParsePiJSONToolUseStopReason(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"role":"assistant","content":[{"type":"text","text":"calling a tool"},{"type":"toolCall","name":"x"}],"usage":{"input":3,"output":2},"stopReason":"toolUse","responseId":"r1"}`)
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, StopToolUse)
	}
	if resp.Text != "calling a tool" {
		t.Fatalf("Text = %q, want only text blocks", resp.Text)
	}
}

func TestParsePiJSONConcatenatesMultipleTextBlocks(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"role":"assistant","content":[{"type":"text","text":"part one "},{"type":"toolCall"},{"type":"text","text":"part two"}],"stopReason":"stop","responseId":"r1"}`)
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "part one part two" {
		t.Fatalf("Text = %q, want part one part two", resp.Text)
	}
}

func TestParsePiJSONToleratesBannerLines(t *testing.T) {
	t.Parallel()
	raw := []byte("Pi v1.2.3 starting...\n{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"banner ok\"}],\"usage\":{\"input\":2,\"output\":3},\"stopReason\":\"stop\",\"responseId\":\"r1\"}\n")
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "banner ok" || resp.InputTokens != 2 || resp.OutputTokens != 3 {
		t.Fatalf("got %q %d/%d, want banner ok 2/3", resp.Text, resp.InputTokens, resp.OutputTokens)
	}
}

func TestParsePiJSONErrorLineBecomesError(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"error","error":"provider auth failed"}`)
	_, err := parsePiJSON(raw)
	if err == nil || !strings.Contains(err.Error(), "provider auth failed") {
		t.Fatalf("err = %v, want provider auth failed", err)
	}
}

func TestParsePiJSONEmptyIsError(t *testing.T) {
	t.Parallel()
	if _, err := parsePiJSON([]byte("   ")); err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParsePiJSONOnlyPartialsNoFinalized(t *testing.T) {
	t.Parallel()
	// Stream that never finalized (all partials) — no usage counted, empty
	// text, default end_turn. Not an error: an empty result is a delivery
	// problem the caller surfaces, not a parse failure.
	raw := []byte(`{"role":"assistant","content":[{"type":"text","text":"thinking"}],"usage":{"input":4,"output":1},"responseId":"r1"}`)
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "" || resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Fatalf("got %q %d/%d, want empty/0/0", resp.Text, resp.InputTokens, resp.OutputTokens)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
	}
}

// TestParsePiJSONRealNestedEnvelope locks in the ACTUAL `pi --mode json`
// wire format (captured from a live run): assistant content is nested under
// stream envelopes ({"type":"message_end","message":{...}}) and re-emitted
// in the terminal {"type":"agent_end","messages":[...]} — never a flat
// top-level role. The same responseId across envelopes must collapse to a
// single counted turn.
func TestParsePiJSONRealNestedEnvelope(t *testing.T) {
	t.Parallel()
	raw := []byte(strings.Join([]string{
		`{"type":"session","version":3,"id":"s1"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[],"usage":{"input":0,"output":0},"stopReason":"stop","responseId":"r1"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"INTEGRATION_OK"}],"usage":{"input":3910,"output":5,"totalTokens":3915},"stopReason":"stop","responseId":"r1"}}`,
		`{"type":"turn_end","message":{"role":"assistant","content":[{"type":"text","text":"INTEGRATION_OK"}],"usage":{"input":3910,"output":5},"stopReason":"stop","responseId":"r1"}}`,
		`{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"INTEGRATION_OK"}],"usage":{"input":3910,"output":5},"stopReason":"stop","responseId":"r1"}]}`,
	}, "\n"))
	resp, err := parsePiJSON(raw)
	if err != nil {
		t.Fatalf("parsePiJSON: %v", err)
	}
	if resp.Text != "INTEGRATION_OK" {
		t.Fatalf("Text = %q, want INTEGRATION_OK", resp.Text)
	}
	// One turn (responseId r1) appears in message_start/message_end/turn_end
	// AND agent_end — must be counted exactly once with its final usage.
	if resp.InputTokens != 3910 || resp.OutputTokens != 5 {
		t.Fatalf("tokens = %d/%d, want 3910/5 (deduped across all envelopes)", resp.InputTokens, resp.OutputTokens)
	}
}
