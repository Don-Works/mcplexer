// thinking.go — picoclaw-style "typing… / thinking…" UX feedback for
// telegram-routed worker replies.
//
// When an inbound telegram message routes onto the mesh and a worker
// is going to respond:
//  1. The bridge immediately sends a "💭 mcplexerbot is thinking…"
//     placeholder to the chat.
//  2. A goroutine renews the chatAction("typing") indicator every 4s
//     so the user keeps seeing "typing…" in the telegram UI.
//  3. When the worker's reply arrives via the notify bus with
//     reply_to=<source mesh message id>, the bridge EDITS the
//     placeholder text into the worker's actual reply instead of
//     sending a fresh message. The chat ends up with one message that
//     updates, not two.
//
// Cleanup paths:
//   - Reply arrives → placeholder is edited, goroutine cancelled,
//     entry popped from cache.
//   - Timeout (default 90s) → goroutine cancels itself, entry stays
//     in cache but its cancel is a no-op so the late-arriving reply
//     falls through to "post fresh message" behaviour. No leak.
//
// In-memory only; bridge restarts lose pending entries (the worker
// reply then lands as a fresh message — visible, just not threaded).
package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ThinkingPlaceholderText is the visible body of the placeholder. Kept to a
// single thought-bubble emoji so it reads as a clean "thinking…" cue rather
// than literal text — the previous "💭 _thinking…_" rendered its underscores
// verbatim under MarkdownV2 (escaping turns `_` into `\_`), which looked
// broken. A bare emoji needs no escaping and renders identically everywhere.
const ThinkingPlaceholderText = "💭"

// thinkingTTL caps how long a placeholder stays "active" (= eligible
// for in-place edit). After this, the typing renewal goroutine exits
// and any late reply lands as a new message. 90s comfortably covers
// the existing worker MaxWallClockSeconds defaults.
const thinkingTTL = 90 * time.Second

// typingRenewInterval is how often we re-send the chatAction so
// telegram keeps showing "typing…" — the API clears it after ~5s, so
// 4s gives a small margin.
const typingRenewInterval = 4 * time.Second

// thinkingRecord pairs a placeholder's chat + message id with the
// cancel func that stops the typing-renewal goroutine.
type thinkingRecord struct {
	chat          store.TelegramChat
	placeholderID string
	cancel        context.CancelFunc
}

// thinkingCache tracks active placeholders keyed by the source mesh
// message id that drove the routing. Safe for concurrent access.
type thinkingCache struct {
	mu    sync.Mutex
	items map[string]thinkingRecord
}

func newThinkingCache() *thinkingCache {
	return &thinkingCache{items: make(map[string]thinkingRecord)}
}

// Put records a new placeholder. Replaces any prior entry for the
// same key (which would be weird — two inbound for one source — but
// safe: prior cancel fires).
func (c *thinkingCache) Put(meshID string, rec thinkingRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.items[meshID]; ok && prev.cancel != nil {
		prev.cancel()
	}
	c.items[meshID] = rec
}

// Pop returns the record for meshID and removes it from the cache.
// `ok` is false when nothing was cached for that key.
func (c *thinkingCache) Pop(meshID string) (thinkingRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.items[meshID]
	if !ok {
		return thinkingRecord{}, false
	}
	delete(c.items, meshID)
	return rec, true
}

// CancelAll fires every cancel func + clears the cache. Used on
// manager shutdown so in-flight typing goroutines don't leak.
func (c *thinkingCache) CancelAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, rec := range c.items {
		if rec.cancel != nil {
			rec.cancel()
		}
		delete(c.items, k)
	}
}

// startTyping kicks off a goroutine that renews the chatAction every
// typingRenewInterval until ctx is cancelled OR the deadline expires.
// Errors are swallowed — the indicator is a UX nicety, not auth-bearing.
func startTyping(ctx context.Context, sender chatActionSender, chat store.TelegramChat) {
	go func() {
		_ = sender.SendChatAction(ctx, chat, "typing")
		t := time.NewTicker(typingRenewInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = sender.SendChatAction(ctx, chat, "typing")
			}
		}
	}()
}

// chatActionSender is the narrow surface startTyping needs from the
// telegram client. Lets unit tests substitute a fake without spinning
// a real bot connection.
type chatActionSender interface {
	SendChatAction(ctx context.Context, chat store.TelegramChat, action string) error
}
