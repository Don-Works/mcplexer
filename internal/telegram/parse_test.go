package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestParseMessage_Private(t *testing.T) {
	msg := &models.Message{
		ID:   42,
		From: &models.User{ID: 7, FirstName: "Alice"},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate, FirstName: "Alice"},
		Text: "hi bot",
	}
	got, ok := ParseMessage(msg, "mybot")
	if !ok {
		t.Fatal("expected parse ok")
	}
	if got.ChatNativeID != "100" {
		t.Errorf("chat id: want 100 got %q", got.ChatNativeID)
	}
	if got.ChatType != "private" {
		t.Errorf("chat type: want private got %q", got.ChatType)
	}
	if got.Text != "hi bot" {
		t.Errorf("text: want hi bot got %q", got.Text)
	}
	if got.MentionsBot {
		t.Error("unexpected mention")
	}
	if got.NativeID != "42" {
		t.Errorf("native id: want 42 got %q", got.NativeID)
	}
}

func TestParseMessage_GroupMention(t *testing.T) {
	msg := &models.Message{
		ID:   7,
		From: &models.User{ID: 1, FirstName: "Bob"},
		Chat: models.Chat{ID: -100, Type: models.ChatTypeSupergroup, Title: "Team"},
		Text: "@mybot status",
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeMention, Offset: 0, Length: 6},
		},
	}
	got, ok := ParseMessage(msg, "mybot")
	if !ok {
		t.Fatal("parse failed")
	}
	if !got.MentionsBot {
		t.Errorf("expected mention detected")
	}
	if got.Text != "status" {
		t.Errorf("text: want status got %q", got.Text)
	}
	if got.ChatType != "supergroup" {
		t.Errorf("chat type: want supergroup got %q", got.ChatType)
	}
}

func TestParseMessage_StartPairing(t *testing.T) {
	msg := &models.Message{
		ID:   1,
		From: &models.User{ID: 7, FirstName: "Alice"},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Text: "/start ABC12345",
	}
	got, ok := ParseMessage(msg, "mybot")
	if !ok {
		t.Fatal("parse failed")
	}
	if got.PairingCode != "ABC12345" {
		t.Errorf("pairing code: want ABC12345 got %q", got.PairingCode)
	}
}

func TestParseMessage_ReplyToBot(t *testing.T) {
	msg := &models.Message{
		ID:   2,
		From: &models.User{ID: 7, FirstName: "Alice"},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Text: "ok",
		ReplyToMessage: &models.Message{
			ID:   999,
			From: &models.User{ID: 1, IsBot: true, Username: "mybot"},
		},
	}
	got, ok := ParseMessage(msg, "mybot")
	if !ok {
		t.Fatal("parse failed")
	}
	if !got.IsReplyToBot {
		t.Error("expected IsReplyToBot")
	}
	if got.RepliedNativeID != "999" {
		t.Errorf("replied native: want 999 got %q", got.RepliedNativeID)
	}
}

func TestParseMessage_IgnoresNonText(t *testing.T) {
	msg := &models.Message{
		ID:   3,
		Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate},
		Text: "",
	}
	if _, ok := ParseMessage(msg, "mybot"); ok {
		t.Error("empty text should be skipped")
	}
}
