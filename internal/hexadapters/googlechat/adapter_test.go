package googlechat

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/hexcore"
)

func TestInputAdapter_Name(t *testing.T) {
	a := NewInputAdapter(make(chan googlechat.IncomingMessage))
	if a.Name() != "googlechat" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "googlechat")
	}
}

func TestInputAdapter_RunReceivesEvents(t *testing.T) {
	inbound := make(chan googlechat.IncomingMessage, 1)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx, out)

	inbound <- googlechat.IncomingMessage{
		SenderName: "Bob",
		Text:       "hello gc",
		SpaceName:  "spaces/AAA",
		SpaceType:  "dm",
		SenderType: "HUMAN",
	}

	select {
	case evt := <-out:
		if evt.Source != "googlechat" {
			t.Fatalf("Source = %q, want %q", evt.Source, "googlechat")
		}
		if evt.Content != "hello gc" {
			t.Fatalf("Content = %q, want %q", evt.Content, "hello gc")
		}
		if evt.Kind != "message" {
			t.Fatalf("Kind = %q, want %q", evt.Kind, "message")
		}
		if evt.SenderName != "Bob" {
			t.Fatalf("SenderName = %q, want %q", evt.SenderName, "Bob")
		}
		if evt.Priority != "normal" {
			t.Fatalf("Priority = %q, want %q", evt.Priority, "normal")
		}
		if len(evt.Tags) != 2 || evt.Tags[0] != "human" || evt.Tags[1] != "googlechat" {
			t.Fatalf("Tags = %v, want [human googlechat]", evt.Tags)
		}
		if evt.Metadata["space_name"] != "spaces/AAA" {
			t.Fatalf("Metadata[space_name] = %v, want %q", evt.Metadata["space_name"], "spaces/AAA")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestInputAdapter_Stop(t *testing.T) {
	inbound := make(chan googlechat.IncomingMessage)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx, out) }()

	inbound <- googlechat.IncomingMessage{Text: "sync"}
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
	inbound := make(chan googlechat.IncomingMessage)
	a := NewInputAdapter(inbound)
	out := make(chan hexcore.Event, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx, out)

	inbound <- googlechat.IncomingMessage{Text: "sync"}
	<-out

	a.Stop()
	a.Stop()
}

func TestIncomingToEvent(t *testing.T) {
	tests := []struct {
		name      string
		msg       googlechat.IncomingMessage
		wantKind  string
	}{
		{
			name:     "plain message",
			msg:      googlechat.IncomingMessage{Text: "hi", SenderName: "Bob"},
			wantKind: "message",
		},
		{
			name:     "pairing message",
			msg:      googlechat.IncomingMessage{PairingCode: "ABC123"},
			wantKind: "pairing",
		},
		{
			name:     "lifecycle event (ADDED_TO_SPACE)",
			msg:      googlechat.IncomingMessage{EventType: "ADDED_TO_SPACE"},
			wantKind: "lifecycle",
		},
		{
			name:     "lifecycle event (REMOVED_FROM_SPACE)",
			msg:      googlechat.IncomingMessage{EventType: "REMOVED_FROM_SPACE"},
			wantKind: "lifecycle",
		},
		{
			name:     "MESSAGE event type defaults to message kind",
			msg:      googlechat.IncomingMessage{EventType: "MESSAGE", Text: "hi"},
			wantKind: "message",
		},
		{
			name:     "empty event type defaults to message kind",
			msg:      googlechat.IncomingMessage{EventType: ""},
			wantKind: "message",
		},
		{
			name:     "lifecycle overrides pairing (lifecycle checked last)",
			msg:      googlechat.IncomingMessage{PairingCode: "XYZ", EventType: "ADDED_TO_SPACE"},
			wantKind: "lifecycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := incomingToEvent(tt.msg)
			if evt.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", evt.Kind, tt.wantKind)
			}
			if evt.Source != "googlechat" {
				t.Fatalf("Source = %q, want %q", evt.Source, "googlechat")
			}
			if evt.ID == "" {
				t.Fatal("ID should not be empty")
			}
		})
	}
}

func TestGooglechatMetadata(t *testing.T) {
	msg := googlechat.IncomingMessage{
		SpaceName:        "spaces/BBB",
		SpaceType:        "group",
		SpaceTitle:       "Dev Team",
		SenderType:       "HUMAN",
		MentionsBot:      true,
		IsReplyToBot:     true,
		ThreadName:       "spaces/BBB/threads/t1",
		NativeMessageID:  "msg-42",
		RepliedNativeID:  "msg-40",
	}

	m := googlechatMetadata(msg)
	if m["space_name"] != "spaces/BBB" {
		t.Fatalf("space_name = %v, want %q", m["space_name"], "spaces/BBB")
	}
	if m["thread_name"] != "spaces/BBB/threads/t1" {
		t.Fatalf("thread_name = %v, want %q", m["thread_name"], "spaces/BBB/threads/t1")
	}
	if m["native_message_id"] != "msg-42" {
		t.Fatalf("native_message_id = %v, want %q", m["native_message_id"], "msg-42")
	}
	if m["replied_native_id"] != "msg-40" {
		t.Fatalf("replied_native_id = %v, want %q", m["replied_native_id"], "msg-40")
	}
	if m["mentions_bot"] != true {
		t.Fatalf("mentions_bot = %v, want true", m["mentions_bot"])
	}
}

func TestGooglechatMetadataMinimal(t *testing.T) {
	msg := googlechat.IncomingMessage{SpaceName: "spaces/CCC"}
	m := googlechatMetadata(msg)
	if _, ok := m["thread_name"]; ok {
		t.Fatal("thread_name should not be present when empty")
	}
	if _, ok := m["native_message_id"]; ok {
		t.Fatal("native_message_id should not be present when empty")
	}
	if _, ok := m["replied_native_id"]; ok {
		t.Fatal("replied_native_id should not be present when empty")
	}
}

func TestPairingAdapter_ConsumePairing(t *testing.T) {
	inbound := make(chan googlechat.IncomingMessage)
	a := NewInputAdapter(inbound)

	wantResult := "paired-gc"
	pa := NewPairingAdapter(a, func(ctx context.Context, platform, code string) (string, error) {
		if platform != "googlechat" {
			t.Fatalf("platform = %q, want %q", platform, "googlechat")
		}
		if code != "XYZ789" {
			t.Fatalf("code = %q, want %q", code, "XYZ789")
		}
		return wantResult, nil
	})

	result, err := pa.ConsumePairing(context.Background(), "googlechat", "XYZ789")
	if err != nil {
		t.Fatalf("ConsumePairing() error = %v", err)
	}
	if result != wantResult {
		t.Fatalf("result = %q, want %q", result, wantResult)
	}
}

func TestPairingAdapter_Name(t *testing.T) {
	inbound := make(chan googlechat.IncomingMessage)
	a := NewInputAdapter(inbound)
	pa := NewPairingAdapter(a, func(ctx context.Context, platform, code string) (string, error) {
		return "", nil
	})
	if pa.Name() != "googlechat" {
		t.Fatalf("Name() = %q, want %q", pa.Name(), "googlechat")
	}
}

func TestCompileTimeInterfaces(t *testing.T) {
	var _ hexcore.InputPort = (*InputAdapter)(nil)
	var _ hexcore.PairingInputPort = (*PairingAdapter)(nil)
}
