package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/hexcore"
	"github.com/don-works/mcplexer/internal/telegram"
)

func TestInputAdapter_Name(t *testing.T) {
	a := NewInputAdapter(make(chan telegram.IncomingMessage))
	if a.Name() != "telegram" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "telegram")
	}
}

func TestInputAdapter_RunReceivesEvents(t *testing.T) {
	inbound := make(chan telegram.IncomingMessage, 1)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx, out)

	inbound <- telegram.IncomingMessage{
		SenderName:   "Alice",
		Text:         "hello bot",
		ChatNativeID: "chat-123",
		ChatType:     "private",
	}

	select {
	case evt := <-out:
		if evt.Source != "telegram" {
			t.Fatalf("Source = %q, want %q", evt.Source, "telegram")
		}
		if evt.Content != "hello bot" {
			t.Fatalf("Content = %q, want %q", evt.Content, "hello bot")
		}
		if evt.Kind != "message" {
			t.Fatalf("Kind = %q, want %q", evt.Kind, "message")
		}
		if evt.SenderName != "Alice" {
			t.Fatalf("SenderName = %q, want %q", evt.SenderName, "Alice")
		}
		if evt.Priority != "normal" {
			t.Fatalf("Priority = %q, want %q", evt.Priority, "normal")
		}
		if len(evt.Tags) != 2 || evt.Tags[0] != "human" || evt.Tags[1] != "telegram" {
			t.Fatalf("Tags = %v, want [human telegram]", evt.Tags)
		}
		if evt.Metadata["chat_native_id"] != "chat-123" {
			t.Fatalf("Metadata[chat_native_id] = %v, want %q", evt.Metadata["chat_native_id"], "chat-123")
		}
		if evt.Metadata["chat_type"] != "private" {
			t.Fatalf("Metadata[chat_type] = %v, want %q", evt.Metadata["chat_type"], "private")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestInputAdapter_Stop(t *testing.T) {
	inbound := make(chan telegram.IncomingMessage)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx, out) }()

	inbound <- telegram.IncomingMessage{Text: "sync"}
	<-out

	a.Stop()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run() expected context error after Stop()")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run to exit")
	}
}

func TestInputAdapter_StopIdempotent(t *testing.T) {
	inbound := make(chan telegram.IncomingMessage)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx, out)

	inbound <- telegram.IncomingMessage{Text: "sync"}
	<-out

	a.Stop()
	a.Stop()
}

func TestIncomingToEvent(t *testing.T) {
	tests := []struct {
		name     string
		msg      telegram.IncomingMessage
		wantKind string
	}{
		{
			name:     "plain message",
			msg:      telegram.IncomingMessage{Text: "hi", SenderName: "Bob"},
			wantKind: "message",
		},
		{
			name:     "callback message",
			msg:      telegram.IncomingMessage{Text: "btn", CallbackData: "clicked"},
			wantKind: "callback",
		},
		{
			name:     "pairing message",
			msg:      telegram.IncomingMessage{PairingCode: "ABC123"},
			wantKind: "pairing",
		},
		{
			name:     "pairing takes priority over callback",
			msg:      telegram.IncomingMessage{PairingCode: "XYZ", CallbackData: "cb"},
			wantKind: "pairing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := incomingToEvent(tt.msg)
			if evt.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", evt.Kind, tt.wantKind)
			}
			if evt.Source != "telegram" {
				t.Fatalf("Source = %q, want %q", evt.Source, "telegram")
			}
			if evt.ID == "" {
				t.Fatal("ID should not be empty")
			}
		})
	}
}

func TestTelegramMetadata(t *testing.T) {
	msg := telegram.IncomingMessage{
		ChatNativeID:    "c1",
		ChatType:        "group",
		ChatTitle:       "Test Group",
		MentionsBot:     true,
		IsReplyToBot:    false,
		CallbackData:    "btn_click",
		RepliedNativeID: "msg-99",
		NativeID:        "msg-100",
	}

	m := telegramMetadata(msg)
	if m["chat_native_id"] != "c1" {
		t.Fatalf("chat_native_id = %v, want %q", m["chat_native_id"], "c1")
	}
	if m["callback_data"] != "btn_click" {
		t.Fatalf("callback_data = %v, want %q", m["callback_data"], "btn_click")
	}
	if m["replied_native_id"] != "msg-99" {
		t.Fatalf("replied_native_id = %v, want %q", m["replied_native_id"], "msg-99")
	}
	if m["native_id"] != "msg-100" {
		t.Fatalf("native_id = %v, want %q", m["native_id"], "msg-100")
	}
	if m["mentions_bot"] != true {
		t.Fatalf("mentions_bot = %v, want true", m["mentions_bot"])
	}
}

func TestTelegramMetadataMinimal(t *testing.T) {
	msg := telegram.IncomingMessage{ChatNativeID: "c2"}
	m := telegramMetadata(msg)
	if _, ok := m["callback_data"]; ok {
		t.Fatal("callback_data should not be present when empty")
	}
	if _, ok := m["replied_native_id"]; ok {
		t.Fatal("replied_native_id should not be present when empty")
	}
	if _, ok := m["native_id"]; ok {
		t.Fatal("native_id should not be present when empty")
	}
}

func TestPairingAdapter_ConsumePairing(t *testing.T) {
	inbound := make(chan telegram.IncomingMessage)
	a := NewInputAdapter(inbound)

	wantResult := "paired-ok"
	pa := NewPairingAdapter(a, func(ctx context.Context, platform, code string) (string, error) {
		if platform != "telegram" {
			t.Fatalf("platform = %q, want %q", platform, "telegram")
		}
		if code != "ABC123" {
			t.Fatalf("code = %q, want %q", code, "ABC123")
		}
		return wantResult, nil
	})

	result, err := pa.ConsumePairing(context.Background(), "telegram", "ABC123")
	if err != nil {
		t.Fatalf("ConsumePairing() error = %v", err)
	}
	if result != wantResult {
		t.Fatalf("result = %q, want %q", result, wantResult)
	}
}

func TestPairingAdapter_Name(t *testing.T) {
	inbound := make(chan telegram.IncomingMessage)
	a := NewInputAdapter(inbound)
	pa := NewPairingAdapter(a, func(ctx context.Context, platform, code string) (string, error) {
		return "", nil
	})
	if pa.Name() != "telegram" {
		t.Fatalf("Name() = %q, want %q", pa.Name(), "telegram")
	}
}

func TestCompileTimeInterfaces(t *testing.T) {
	var _ hexcore.InputPort = (*InputAdapter)(nil)
	var _ hexcore.PairingInputPort = (*PairingAdapter)(nil)
}
