package telegram

import (
	"strings"
	"testing"
)

func TestDecideInbound_GroupNoMentionIsSkipped(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform: "telegram",
		ChatType: "group",
		Text:     "hello everyone",
	}, RoutingConfig{}, "")
	if !got.Skip {
		t.Fatalf("expected skip, got %+v", got)
	}
}

func TestDecideInbound_GroupMentionRouted(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform:    "telegram",
		ChatType:    "group",
		Text:        "ping",
		MentionsBot: true,
		SenderName:  "Alice",
	}, RoutingConfig{}, "")
	if got.Skip {
		t.Fatalf("expected routed, got skip")
	}
	if got.Send.Audience != "*" {
		t.Errorf("default audience: want *, got %q", got.Send.Audience)
	}
	if !strings.HasPrefix(got.Send.Content, "Alice: ") {
		t.Errorf("group content should be sender-prefixed: %q", got.Send.Content)
	}
}

func TestDecideInbound_DMFreshMessageQuestion(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform: "telegram",
		ChatType: "private",
		Text:     "what's the status?",
	}, RoutingConfig{}, "")
	if got.Skip {
		t.Fatalf("DM should route")
	}
	if got.Send.Kind != "question" {
		t.Errorf("kind: want question, got %q", got.Send.Kind)
	}
	if got.Send.Priority != "high" {
		t.Errorf("priority: want high, got %q", got.Send.Priority)
	}
	if got.Send.Tags != "human,telegram" {
		t.Errorf("tags: want human,telegram, got %q", got.Send.Tags)
	}
}

func TestDecideInbound_AtRolePrefix(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform: "telegram",
		ChatType: "private",
		Text:     "@reviewer please check this",
	}, RoutingConfig{}, "")
	if got.Send.Audience != "reviewer" {
		t.Errorf("audience: want reviewer, got %q", got.Send.Audience)
	}
}

func TestDecideInbound_CallbackReplySubstitutedBySentinel(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform:     "telegram",
		ChatType:     "private",
		CallbackData: "reply:MESH123",
		Text:         "approved",
	}, RoutingConfig{}, "MESH123")
	if got.Send.ReplyTo != "MESH123" {
		t.Errorf("reply_to: want MESH123, got %q", got.Send.ReplyTo)
	}
	if got.Send.Kind != "reply" {
		t.Errorf("kind: want reply, got %q", got.Send.Kind)
	}
	if sentinel := ExtractMeshMessageIDFromPlaceholder(got.Send.Audience); sentinel != "MESH123" {
		t.Errorf("expected placeholder audience mesh-msg:MESH123 got %q", got.Send.Audience)
	}
}

func TestDecideInbound_NativeReplyWithResolvedID(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform:        "telegram",
		ChatType:        "private",
		IsReplyToBot:    true,
		RepliedNativeID: "42",
		Text:            "yes",
	}, RoutingConfig{}, "MESH999")
	if got.Skip {
		t.Fatalf("expected routed")
	}
	if got.Send.ReplyTo != "MESH999" {
		t.Errorf("reply_to: want MESH999, got %q", got.Send.ReplyTo)
	}
}

func TestDecideInbound_DefaultAudienceRole(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		Platform: "telegram",
		ChatType: "private",
		Text:     "deploy now",
	}, RoutingConfig{DefaultAudience: "role:orchestrator"}, "")
	if got.Send.Audience != "orchestrator" {
		t.Errorf("audience: want orchestrator, got %q", got.Send.Audience)
	}
}

func TestSplitRolePrefix(t *testing.T) {
	cases := []struct {
		in       string
		role     string
		rest     string
		expectOk bool
	}{
		{"@reviewer please", "reviewer", "please", true},
		{"@alice-bob hi", "alice-bob", "hi", true},
		{"@", "", "", false},
		{"no prefix", "", "", false},
		{"@onlyrole", "", "", false}, // no rest
	}
	for _, c := range cases {
		role, rest, ok := splitRolePrefix(c.in)
		if ok != c.expectOk {
			t.Errorf("%q: want ok=%v, got %v", c.in, c.expectOk, ok)
			continue
		}
		if role != c.role || rest != c.rest {
			t.Errorf("%q: want role=%q rest=%q, got role=%q rest=%q",
				c.in, c.role, c.rest, role, rest)
		}
	}
}
