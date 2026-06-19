package googlechat

import (
	"strings"
	"testing"
)

func TestDecideInbound_DMRoutedFreshMessage(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceType: "dm",
		Text:      "hi",
	}, RoutingConfig{}, "mention", "")
	if got.Skip {
		t.Fatalf("DM should route, got skip")
	}
	if got.Send.Audience != "*" {
		t.Errorf("default audience: want *, got %q", got.Send.Audience)
	}
	if got.Send.Tags != "human,googlechat" {
		t.Errorf("tags: want human,googlechat got %q", got.Send.Tags)
	}
	if got.Send.Priority != "high" {
		t.Errorf("priority: want high, got %q", got.Send.Priority)
	}
}

func TestDecideInbound_DMQuestionKind(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceType: "dm",
		Text:      "what's the status?",
	}, RoutingConfig{}, "mention", "")
	if got.Send.Kind != "question" {
		t.Errorf("kind: want question, got %q", got.Send.Kind)
	}
}

func TestDecideInbound_GroupNoMentionSkipped(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceType: "space",
		Text:      "hello everyone",
	}, RoutingConfig{}, "mention", "")
	if !got.Skip {
		t.Fatalf("expected skip, got %+v", got)
	}
}

func TestDecideInbound_GroupMentionRouted(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType:   EventTypeMessage,
		SpaceType:   "space",
		Text:        "ping",
		MentionsBot: true,
		SenderName:  "Alice",
	}, RoutingConfig{}, "mention", "")
	if got.Skip {
		t.Fatalf("expected route, got skip")
	}
	if got.Send.Audience != "*" {
		t.Errorf("audience: want *, got %q", got.Send.Audience)
	}
	if !strings.HasPrefix(got.Send.Content, "Alice: ") {
		t.Errorf("group content should be sender-prefixed: %q", got.Send.Content)
	}
}

func TestDecideInbound_GroupListenAllRouted(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType:  EventTypeMessage,
		SpaceType:  "space",
		Text:       "ambient chatter",
		SenderName: "Bob",
	}, RoutingConfig{}, "all", "")
	if got.Skip {
		t.Fatalf("expected route under listen=all, got skip")
	}
	if !strings.HasPrefix(got.Send.Content, "Bob: ") {
		t.Errorf("expected sender prefix, got %q", got.Send.Content)
	}
}

func TestDecideInbound_NativeReplyToBotResolvedWithSentinel(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType:    EventTypeMessage,
		SpaceType:    "dm",
		IsReplyToBot: true,
		Text:         "yes",
	}, RoutingConfig{}, "mention", "MESH999")
	if got.Skip {
		t.Fatalf("expected route")
	}
	if got.Send.ReplyTo != "MESH999" {
		t.Errorf("reply_to: want MESH999, got %q", got.Send.ReplyTo)
	}
	if got.Send.Kind != "reply" {
		t.Errorf("kind: want reply, got %q", got.Send.Kind)
	}
	if sentinel := ExtractMeshMessageIDFromPlaceholder(got.Send.Audience); sentinel != "MESH999" {
		t.Errorf("expected placeholder audience mesh-msg:MESH999, got %q", got.Send.Audience)
	}
}

func TestDecideInbound_AtRolePrefix(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceType: "dm",
		Text:      "@reviewer please check",
	}, RoutingConfig{}, "mention", "")
	if got.Send.Audience != "reviewer" {
		t.Errorf("audience: want reviewer, got %q", got.Send.Audience)
	}
	// Content should be stripped of the @role prefix.
	if strings.HasPrefix(got.Send.Content, "@reviewer") {
		t.Errorf("content should drop @role prefix, got %q", got.Send.Content)
	}
}

func TestDecideInbound_DefaultAudienceRolePrefix(t *testing.T) {
	got := DecideInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceType: "dm",
		Text:      "deploy now",
	}, RoutingConfig{DefaultAudience: "role:orchestrator"}, "mention", "")
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
		{"@onlyrole", "", "", false},
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

func TestExtractMeshMessageIDFromPlaceholder(t *testing.T) {
	if got := ExtractMeshMessageIDFromPlaceholder("mesh-msg:abc"); got != "abc" {
		t.Errorf("want abc, got %q", got)
	}
	if got := ExtractMeshMessageIDFromPlaceholder("other:abc"); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}
