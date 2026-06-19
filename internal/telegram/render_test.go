package telegram

import (
	"strings"
	"testing"
)

func TestEscapeMarkdownV2(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"a*b", `a\*b`},
		{"a.b", `a\.b`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{"brackets (x)", `brackets \(x\)`},
		{"hash#tag", `hash\#tag`},
	}
	for _, c := range cases {
		if got := EscapeMarkdownV2(c.in); got != c.want {
			t.Errorf("Escape(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

func TestRenderText_TitleBodyNoTagFooter(t *testing.T) {
	out := RenderText(OutgoingMessage{
		Title:    "task from agent-a",
		Body:     "Please review PR #123.",
		Priority: "high",
		Tags:     "backend,urgent",
	})
	if !strings.Contains(out, `*task from agent\-a*`) {
		t.Errorf("title not found / not escaped: %q", out)
	}
	if !strings.Contains(out, `Please review PR \#123\.`) {
		t.Errorf("body not escaped: %q", out)
	}
	if strings.Contains(out, `\#high`) || strings.Contains(out, `\#backend`) ||
		strings.Contains(out, `\#urgent`) {
		t.Errorf("telegram should not render tag footers: %q", out)
	}
}

func TestRenderPlainText_NoMarkdownEscapes(t *testing.T) {
	out := RenderPlainText(OutgoingMessage{
		Title: "finding from telegram-responder [Telegram, opencode_cli:MiniMax-M2.7-highspeed]",
		Body:  "Created task `test_remove_me`.\nUse /workers/cost next.",
	})
	if strings.Contains(out, `\_`) || strings.Contains(out, `\.`) || strings.Contains(out, "\\`") {
		t.Fatalf("plain fallback should not include MarkdownV2 escapes: %q", out)
	}
	if !strings.Contains(out, "opencode_cli") || !strings.Contains(out, "`test_remove_me`") {
		t.Fatalf("plain fallback lost raw content: %q", out)
	}
}

func TestThinkingPlaceholderRendersClean(t *testing.T) {
	// The placeholder must render to a bare bubble with no escaping artifacts.
	got := RenderText(OutgoingMessage{Body: ThinkingPlaceholderText})
	if got != "💭" {
		t.Errorf("thinking placeholder should render as bare bubble, got %q", got)
	}
}

func TestRenderText_CleansWorkerFailureNoise(t *testing.T) {
	body := "worker \"delegate-crm-leads-audit-c828ad54fc1d\" finished (run 01KTY2KHTEQ6FTEWZR0P2DTX0T) status=failure — adapter send: grok_cli: run: exit status 1 (stderr: \x1b[2m2026-06-12T14:08:35Z\x1b[0m \x1b[31mERROR\x1b[0m config toml has syntax errors: TOML parse error at line 26, column 6\n26 | <!-- MCPLEXER:HARNESS-SYNC:BEGIN v1 (grok) -->\nkey with no value, expected `=`)"
	out := RenderText(OutgoingMessage{Body: body})
	if !strings.Contains(out, `Worker failed: delegate\-crm\-leads\-audit\-c828ad54fc1d`) {
		t.Fatalf("compact worker failure missing: %q", out)
	}
	for _, noisy := range []string{"[2m", "[31m", "worker_finished", "#failure"} {
		if strings.Contains(out, noisy) {
			t.Errorf("expected noisy fragment %q to be removed from %q", noisy, out)
		}
	}
	if !strings.Contains(out, "TOML parse error") {
		t.Errorf("useful failure detail should survive: %q", out)
	}
}

func TestRenderKeyboard_Nil(t *testing.T) {
	if kb := RenderKeyboard(nil); kb != nil {
		t.Errorf("expected nil keyboard, got %+v", kb)
	}
}

func TestRenderKeyboard_Buttons(t *testing.T) {
	kb := RenderKeyboard([]Button{
		{Label: "Reply", CallbackData: "reply:ABC"},
	})
	if kb == nil || len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 1 {
		t.Fatalf("unexpected keyboard: %+v", kb)
	}
	b := kb.InlineKeyboard[0][0]
	if b.Text != "Reply" || b.CallbackData != "reply:ABC" {
		t.Errorf("button mismatch: %+v", b)
	}
}
