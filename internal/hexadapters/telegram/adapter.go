// Package telegram provides hexagonal adapters that bridge the existing
// Telegram Manager to hexcore.InputPort and hexcore.PairingInputPort
// interfaces.
package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/hexcore"
	"github.com/don-works/mcplexer/internal/telegram"
)

const adapterName = "telegram"

var (
	_ hexcore.InputPort        = (*InputAdapter)(nil)
	_ hexcore.PairingInputPort = (*PairingAdapter)(nil)
)

// InputAdapter wraps the Telegram Manager's inbound channel as a
// hexcore.InputPort. It receives IncomingMessage values from the
// Manager and converts them to hexcore.Events.
type InputAdapter struct {
	inbound    <-chan telegram.IncomingMessage
	cancel     context.CancelFunc
	cancelOnce sync.Once
}

// NewInputAdapter creates an InputAdapter from the Manager's inbound
// channel. The caller must start the Manager's long-poll loop before
// calling Run.
func NewInputAdapter(inbound <-chan telegram.IncomingMessage) *InputAdapter {
	return &InputAdapter{inbound: inbound}
}

func (a *InputAdapter) Name() string { return adapterName }

// Run reads from the Manager's inbound channel and pushes hexcore.Events
// onto the output channel. Blocks until ctx is cancelled.
func (a *InputAdapter) Run(ctx context.Context, out chan<- hexcore.Event) error {
	ctx, a.cancel = context.WithCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-a.inbound:
			if !ok {
				return fmt.Errorf("telegram inbound channel closed")
			}
			out <- incomingToEvent(msg)
		}
	}
}

func (a *InputAdapter) Stop() error {
	a.cancelOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
	})
	return nil
}

// PairingAdapter extends InputAdapter with pairing support.
type PairingAdapter struct {
	*InputAdapter
	pairFunc func(ctx context.Context, platform, code string) (string, error)
}

// NewPairingAdapter wraps an InputAdapter with a pairing function.
func NewPairingAdapter(adapter *InputAdapter, pairFunc func(ctx context.Context, platform, code string) (string, error)) *PairingAdapter {
	return &PairingAdapter{InputAdapter: adapter, pairFunc: pairFunc}
}

func (a *PairingAdapter) ConsumePairing(ctx context.Context, platform, code string) (string, error) {
	return a.pairFunc(ctx, platform, code)
}

// incomingToEvent converts a Telegram IncomingMessage to a hexcore.Event.
func incomingToEvent(msg telegram.IncomingMessage) hexcore.Event {
	kind := "message"
	if msg.CallbackData != "" {
		kind = "callback"
	}
	if msg.PairingCode != "" {
		kind = "pairing"
	}

	return hexcore.Event{
		ID:          ulid.Make().String(),
		Source:      adapterName,
		Timestamp:   time.Now(),
		Kind:        kind,
		Content:     msg.Text,
		Metadata:    telegramMetadata(msg),
		Priority:    "normal",
		Tags:        []string{"human", "telegram"},
		SenderName:  msg.SenderName,
		PairingCode: msg.PairingCode,
	}
}

func telegramMetadata(msg telegram.IncomingMessage) map[string]any {
	m := map[string]any{
		"chat_native_id": msg.ChatNativeID,
		"chat_type":      msg.ChatType,
		"chat_title":     msg.ChatTitle,
		"mentions_bot":   msg.MentionsBot,
		"is_reply":       msg.IsReplyToBot,
	}
	if msg.CallbackData != "" {
		m["callback_data"] = msg.CallbackData
	}
	if msg.RepliedNativeID != "" {
		m["replied_native_id"] = msg.RepliedNativeID
	}
	if msg.NativeID != "" {
		m["native_id"] = msg.NativeID
	}
	return m
}
