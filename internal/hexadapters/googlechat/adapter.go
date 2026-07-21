// Package googlechat provides hexagonal adapters that bridge the existing
// Google Chat Manager to hexcore.InputPort and hexcore.PairingInputPort
// interfaces.
package googlechat

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/hexcore"
)

const adapterName = "googlechat"

var (
	_ hexcore.InputPort        = (*InputAdapter)(nil)
	_ hexcore.PairingInputPort = (*PairingAdapter)(nil)
)

// InputAdapter wraps the Google Chat Manager's inbound channel as a
// hexcore.InputPort.
type InputAdapter struct {
	inbound    <-chan googlechat.IncomingMessage
	cancel     context.CancelFunc
	cancelOnce sync.Once
}

// NewInputAdapter creates an InputAdapter from the Manager's inbound channel.
func NewInputAdapter(inbound <-chan googlechat.IncomingMessage) *InputAdapter {
	return &InputAdapter{inbound: inbound}
}

func (a *InputAdapter) Name() string { return adapterName }

func (a *InputAdapter) Run(ctx context.Context, out chan<- hexcore.Event) error {
	ctx, a.cancel = context.WithCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-a.inbound:
			if !ok {
				return fmt.Errorf("googlechat inbound channel closed")
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

func NewPairingAdapter(adapter *InputAdapter, pairFunc func(ctx context.Context, platform, code string) (string, error)) *PairingAdapter {
	return &PairingAdapter{InputAdapter: adapter, pairFunc: pairFunc}
}

func (a *PairingAdapter) ConsumePairing(ctx context.Context, platform, code string) (string, error) {
	return a.pairFunc(ctx, platform, code)
}

func incomingToEvent(msg googlechat.IncomingMessage) hexcore.Event {
	kind := "message"
	if msg.PairingCode != "" {
		kind = "pairing"
	}
	if msg.EventType != "" && msg.EventType != "MESSAGE" {
		kind = "lifecycle"
	}

	return hexcore.Event{
		ID:          ulid.Make().String(),
		Source:      adapterName,
		Timestamp:   time.Now(),
		Kind:        kind,
		Content:     msg.Text,
		Metadata:    googlechatMetadata(msg),
		Priority:    "normal",
		Tags:        []string{"human", "googlechat"},
		SenderName:  msg.SenderName,
		PairingCode: msg.PairingCode,
	}
}

func googlechatMetadata(msg googlechat.IncomingMessage) map[string]any {
	m := map[string]any{
		"space_name":   msg.SpaceName,
		"space_type":   msg.SpaceType,
		"space_title":  msg.SpaceTitle,
		"sender_type":  msg.SenderType,
		"mentions_bot": msg.MentionsBot,
		"is_reply":     msg.IsReplyToBot,
	}
	if msg.ThreadName != "" {
		m["thread_name"] = msg.ThreadName
	}
	if msg.NativeMessageID != "" {
		m["native_message_id"] = msg.NativeMessageID
	}
	if msg.RepliedNativeID != "" {
		m["replied_native_id"] = msg.RepliedNativeID
	}
	return m
}
