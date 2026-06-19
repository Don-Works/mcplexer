// notify_body_test.go — regression guard for fix/telegram-no-truncate.
//
// History: buildNotifyBody used to truncate at 240 chars with "…", so
// every notify_user=true mesh message arrived at the Telegram bridge
// already chopped. SplitBody (the chunker) never saw the full content
// and the user's phone showed truncated output even for sub-cap (~564
// char) outputs because the cut had already happened in the publisher.
//
// The fix removes the cap entirely — the notify event Body now carries
// the full mesh content. Display caps (PWA tray line-clamp, OS
// notification truncation, Telegram chunking) are downstream concerns.
package mesh_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
)

func TestSend_NotifyBus_DeliversFullContent(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	bus := notify.NewBus()
	mgr.SetNotifyBus(bus)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	ctx := context.Background()
	meta := mesh.SessionMeta{
		SessionID:    "test-notify-full",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	// 6000 chars — well over Telegram's 4096 cap, and far over the old
	// 240-char notify cap. The Telegram bridge's SplitBody will chunk
	// this downstream; what we're proving HERE is that the notify event
	// itself carries every byte.
	body := strings.Repeat("abcdefghij", 600)
	if _, err := mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:       "finding",
		Content:    body,
		NotifyUser: true,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case evt := <-ch:
		if got := len(evt.Body); got != len(body) {
			t.Errorf("notify body length = %d, want %d (cap regression)",
				got, len(body))
		}
		if !strings.Contains(evt.Body, "abcdefghij") || strings.Contains(evt.Body, "…") {
			t.Errorf("notify body looks truncated: starts %q ends %q",
				evt.Body[:min(40, len(evt.Body))],
				evt.Body[max(0, len(evt.Body)-40):],
			)
		}
	default:
		t.Fatal("expected notify event on bus, got none")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
