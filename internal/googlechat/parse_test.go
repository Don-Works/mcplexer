package googlechat

import (
	"encoding/json"
	"testing"
)

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParseEvent_Message_DMHuman(t *testing.T) {
	env := EventEnvelope{
		Type: EventTypeMessage,
		Space: EventSpace{
			Name:      "spaces/AAAA",
			SpaceType: "DIRECT_MESSAGE",
		},
		User: EventUser{
			Name:        "users/42",
			DisplayName: "Alice",
			Type:        "HUMAN",
		},
		Message: &EventMessage{
			Name: "spaces/AAAA/messages/MSG1",
			Text: "hi bot",
			Sender: EventUser{
				Name:        "users/42",
				DisplayName: "Alice",
				Type:        "HUMAN",
			},
		},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), "users/99")
	if !ok {
		t.Fatal("expected parse ok")
	}
	if msg.EventType != EventTypeMessage {
		t.Errorf("event type: %q", msg.EventType)
	}
	if msg.SpaceType != "dm" {
		t.Errorf("space type: want dm, got %q", msg.SpaceType)
	}
	if msg.SpaceName != "spaces/AAAA" {
		t.Errorf("space name: %q", msg.SpaceName)
	}
	if msg.NativeMessageID != "MSG1" {
		t.Errorf("native id: want MSG1, got %q", msg.NativeMessageID)
	}
	if msg.Text != "hi bot" {
		t.Errorf("text: %q", msg.Text)
	}
	if msg.MentionsBot {
		t.Error("did not expect mention")
	}
	if msg.SenderName != "Alice" {
		t.Errorf("sender: %q", msg.SenderName)
	}
}

func TestParseEvent_Message_GroupMentionViaAnnotation(t *testing.T) {
	botName := "users/99"
	env := EventEnvelope{
		Type: EventTypeMessage,
		Space: EventSpace{
			Name:        "spaces/BBBB",
			DisplayName: "Eng",
			SpaceType:   "SPACE",
		},
		User: EventUser{DisplayName: "Bob", Type: "HUMAN"},
		Message: &EventMessage{
			Name:         "spaces/BBBB/messages/MSG2",
			Text:         "@bot status",
			ArgumentText: "status",
			Sender:       EventUser{Type: "HUMAN", DisplayName: "Bob"},
			Annotations: []EventAnnotation{
				{
					Type: "USER_MENTION",
					UserMention: &EventUserMention{
						User: EventUser{Name: botName, DisplayName: "bot"},
						Type: "MENTION",
					},
				},
			},
		},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), botName)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if !msg.MentionsBot {
		t.Error("expected MentionsBot=true via annotation")
	}
	if msg.SpaceType != "space" {
		t.Errorf("space type: want space, got %q", msg.SpaceType)
	}
	if msg.Text != "status" {
		t.Errorf("text should be argumentText: got %q", msg.Text)
	}
}

func TestParseEvent_Message_GroupMentionFallbackByArgumentText(t *testing.T) {
	env := EventEnvelope{
		Type: EventTypeMessage,
		Space: EventSpace{
			Name:      "spaces/CCCC",
			SpaceType: "GROUP_CHAT",
		},
		User: EventUser{DisplayName: "Carol", Type: "HUMAN"},
		Message: &EventMessage{
			Name:         "spaces/CCCC/messages/MSG3",
			Text:         "@bot deploy",
			ArgumentText: "deploy",
			Sender:       EventUser{Type: "HUMAN", DisplayName: "Carol"},
		},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), "")
	if !ok {
		t.Fatal("parse failed")
	}
	if !msg.MentionsBot {
		t.Error("expected fallback heuristic to detect mention")
	}
	if msg.SpaceType != "group" {
		t.Errorf("space type: want group, got %q", msg.SpaceType)
	}
}

func TestParseEvent_Message_BotSenderRejected(t *testing.T) {
	env := EventEnvelope{
		Type:  EventTypeMessage,
		Space: EventSpace{Name: "spaces/AAAA", SpaceType: "DIRECT_MESSAGE"},
		User:  EventUser{Type: "BOT"},
		Message: &EventMessage{
			Name:   "spaces/AAAA/messages/MSG4",
			Text:   "echo",
			Sender: EventUser{Type: "BOT"},
		},
	}
	if _, ok := ParseEvent(mustMarshal(t, env), ""); ok {
		t.Error("bot-authored messages must be rejected")
	}
}

func TestParseEvent_AddedToSpace(t *testing.T) {
	env := EventEnvelope{
		Type: EventTypeAddedToSpace,
		Space: EventSpace{
			Name:        "spaces/DDDD",
			DisplayName: "Newroom",
			SpaceType:   "SPACE",
		},
		User: EventUser{DisplayName: "Admin", Type: "HUMAN"},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), "")
	if !ok {
		t.Fatal("parse failed")
	}
	if msg.EventType != EventTypeAddedToSpace {
		t.Errorf("event: %q", msg.EventType)
	}
	if msg.SpaceName != "spaces/DDDD" {
		t.Errorf("space name: %q", msg.SpaceName)
	}
	if msg.SpaceTitle != "Newroom" {
		t.Errorf("title: %q", msg.SpaceTitle)
	}
	if msg.SpaceType != "space" {
		t.Errorf("space type: %q", msg.SpaceType)
	}
}

func TestParseEvent_RemovedFromSpace(t *testing.T) {
	env := EventEnvelope{
		Type: EventTypeRemovedFromSpace,
		Space: EventSpace{
			Name:      "spaces/DDDD",
			SpaceType: "SPACE",
		},
		User: EventUser{Type: "HUMAN"},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), "")
	if !ok {
		t.Fatal("parse failed")
	}
	if msg.EventType != EventTypeRemovedFromSpace {
		t.Errorf("event: %q", msg.EventType)
	}
}

func TestParseEvent_PairingCode(t *testing.T) {
	env := EventEnvelope{
		Type:  EventTypeMessage,
		Space: EventSpace{Name: "spaces/AAAA", SpaceType: "DIRECT_MESSAGE"},
		User:  EventUser{DisplayName: "Alice", Type: "HUMAN"},
		Message: &EventMessage{
			Name:   "spaces/AAAA/messages/MSG5",
			Text:   "/pair ABC12345",
			Sender: EventUser{Type: "HUMAN"},
		},
	}
	msg, ok := ParseEvent(mustMarshal(t, env), "")
	if !ok {
		t.Fatal("parse failed")
	}
	if msg.PairingCode != "ABC12345" {
		t.Errorf("pairing code: want ABC12345, got %q", msg.PairingCode)
	}
}

func TestParseEvent_UnsupportedType(t *testing.T) {
	env := map[string]any{"type": "SOMETHING_ELSE"}
	if _, ok := ParseEvent(mustMarshal(t, env), ""); ok {
		t.Error("unsupported event types must yield ok=false")
	}
}

func TestParseEvent_MalformedJSON(t *testing.T) {
	if _, ok := ParseEvent([]byte("{not json"), ""); ok {
		t.Error("malformed JSON must yield ok=false")
	}
}

func TestLastSegment(t *testing.T) {
	cases := map[string]string{
		"spaces/AAA/messages/BBB": "BBB",
		"users/42":                "42",
		"singlepart":              "singlepart",
		"":                        "",
	}
	for in, want := range cases {
		if got := lastSegment(in); got != want {
			t.Errorf("%q: want %q got %q", in, want, got)
		}
	}
}
