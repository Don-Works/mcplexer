package hexcore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventCreation(t *testing.T) {
	now := time.Now()
	e := Event{
		ID:          "evt-1",
		Source:      "telegram",
		Timestamp:   now,
		Kind:        "message",
		Content:     "hello",
		Metadata:    map[string]any{"chat_id": "123"},
		Priority:    "normal",
		Tags:        []string{"human", "telegram"},
		SenderName:  "Alice",
		PairingCode: "",
	}

	if e.ID != "evt-1" {
		t.Fatalf("ID = %q, want %q", e.ID, "evt-1")
	}
	if e.Source != "telegram" {
		t.Fatalf("Source = %q, want %q", e.Source, "telegram")
	}
	if e.Kind != "message" {
		t.Fatalf("Kind = %q, want %q", e.Kind, "message")
	}
	if e.Content != "hello" {
		t.Fatalf("Content = %q, want %q", e.Content, "hello")
	}
	if e.Metadata["chat_id"] != "123" {
		t.Fatalf("Metadata[chat_id] = %v, want %v", e.Metadata["chat_id"], "123")
	}
	if len(e.Tags) != 2 {
		t.Fatalf("len(Tags) = %d, want 2", len(e.Tags))
	}
}

func TestActionCreation(t *testing.T) {
	now := time.Now()
	a := Action{
		ID:        "act-1",
		Source:    "worker",
		Timestamp: now,
		Kind:      "deliver",
		Content:   "result text",
		Title:     "Task Done",
		Target: ActionTarget{
			Channel:    "telegram",
			ChatID:     "456",
			WebhookURL: "https://example.com/hook",
		},
		Priority: "high",
		Tags:     []string{"output"},
		WorkerID: "w-1",
		RunID:    "r-1",
		Status:   "completed",
		CostUSD:  0.05,
	}

	if a.ID != "act-1" {
		t.Fatalf("ID = %q, want %q", a.ID, "act-1")
	}
	if a.Target.Channel != "telegram" {
		t.Fatalf("Target.Channel = %q, want %q", a.Target.Channel, "telegram")
	}
	if a.Target.WebhookURL != "https://example.com/hook" {
		t.Fatalf("Target.WebhookURL = %q, want %q", a.Target.WebhookURL, "https://example.com/hook")
	}
	if a.CostUSD != 0.05 {
		t.Fatalf("CostUSD = %f, want 0.05", a.CostUSD)
	}
}

func TestActionTargetZeroValue(t *testing.T) {
	at := ActionTarget{}
	if at.Channel != "" {
		t.Fatalf("zero Channel = %q, want empty", at.Channel)
	}
	if at.Headers != nil {
		t.Fatalf("zero Headers = %v, want nil", at.Headers)
	}
}

func TestDefaultEventRouter_Route(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		handlerKey string
		wantCalled bool
	}{
		{"matching kind routes to handler", "message", "message", true},
		{"unhandled kind is silently ignored", "unknown_kind", "message", false},
		{"empty kind unhandled", "", "message", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewDefaultEventRouter()
			var called atomic.Bool
			router.RegisterHandler(tt.handlerKey, func(ctx context.Context, event Event) error {
				called.Store(true)
				return nil
			})

			err := router.Route(context.Background(), Event{Kind: tt.kind, Source: "test"})
			if err != nil {
				t.Fatalf("Route() error = %v", err)
			}
			if called.Load() != tt.wantCalled {
				t.Fatalf("handler called = %v, want %v", called.Load(), tt.wantCalled)
			}
		})
	}
}

func TestDefaultEventRouter_HandlerError(t *testing.T) {
	router := NewDefaultEventRouter()
	wantErr := errors.New("handler failed")
	router.RegisterHandler("fail", func(ctx context.Context, event Event) error {
		return wantErr
	})

	err := router.Route(context.Background(), Event{Kind: "fail"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Route() error = %v, want %v", err, wantErr)
	}
}

func TestDefaultEventRouter_OverwriteHandler(t *testing.T) {
	router := NewDefaultEventRouter()
	var count atomic.Int32

	router.RegisterHandler("msg", func(ctx context.Context, event Event) error {
		count.Add(1)
		return nil
	})
	router.RegisterHandler("msg", func(ctx context.Context, event Event) error {
		count.Add(10)
		return nil
	})

	_ = router.Route(context.Background(), Event{Kind: "msg"})
	if count.Load() != 10 {
		t.Fatalf("count = %d, want 10 (overwritten handler)", count.Load())
	}
}

func TestDefaultActionDispatcher_DispatchFirstMatch(t *testing.T) {
	tests := []struct {
		name       string
		channel    string
		wantDeliver string
	}{
		{"dispatches to matching port", "mesh", "mesh"},
		{"dispatches to file port", "file", "file"},
		{"no matching port returns nil", "nonexistent", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDefaultActionDispatcher()
			var delivered atomic.Value
			delivered.Store("")

			meshPort := &mockOutputPort{
				name: "mesh",
				canDeliver: func(a Action) bool { return a.Target.Channel == "mesh" },
				deliver: func(ctx context.Context, a Action) error {
					delivered.Store("mesh")
					return nil
				},
			}
			filePort := &mockOutputPort{
				name: "file",
				canDeliver: func(a Action) bool { return a.Target.Channel == "file" },
				deliver: func(ctx context.Context, a Action) error {
					delivered.Store("file")
					return nil
				},
			}

			d.RegisterOutput(meshPort)
			d.RegisterOutput(filePort)

			err := d.Dispatch(context.Background(), Action{
				Target: ActionTarget{Channel: tt.channel},
			})
			if err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if delivered.Load().(string) != tt.wantDeliver {
				t.Fatalf("delivered to %q, want %q", delivered.Load(), tt.wantDeliver)
			}
		})
	}
}

func TestDefaultActionDispatcher_FanOut(t *testing.T) {
	d := NewDefaultActionDispatcher()
	d.fanOut = true

	var count atomic.Int32
	port := &mockOutputPort{
		name:       "always",
		canDeliver: func(a Action) bool { return true },
		deliver: func(ctx context.Context, a Action) error {
			count.Add(1)
			return nil
		},
	}
	d.RegisterOutput(port)
	d.RegisterOutput(port)

	_ = d.Dispatch(context.Background(), Action{})
	if count.Load() != 2 {
		t.Fatalf("fanOut: delivered %d times, want 2", count.Load())
	}
}

func TestDefaultActionDispatcher_DeliverError(t *testing.T) {
	d := NewDefaultActionDispatcher()
	wantErr := errors.New("deliver failed")
	d.RegisterOutput(&mockOutputPort{
		name:       "fail",
		canDeliver: func(a Action) bool { return true },
		deliver:    func(ctx context.Context, a Action) error { return wantErr },
	})

	err := d.Dispatch(context.Background(), Action{})
	if err == nil {
		t.Fatal("Dispatch() expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Dispatch() error = %v, want wrapped %v", err, wantErr)
	}
}

type mockOutputPort struct {
	name       string
	canDeliver func(Action) bool
	deliver    func(context.Context, Action) error
}

func (m *mockOutputPort) Name() string                    { return m.name }
func (m *mockOutputPort) CanDeliver(a Action) bool        { return m.canDeliver(a) }
func (m *mockOutputPort) Deliver(ctx context.Context, a Action) error { return m.deliver(ctx, a) }
