package ephemeral

import (
	"sync"
	"time"
)

// Event is a UI-visible secret-prompt lifecycle change. The struct never
// includes the secret value or the file path — only metadata safe to fan
// out over SSE (the path is internal and visible only to the agent that
// requested it).
type Event struct {
	Type      string    `json:"type"` // "pending" | "resolved"
	ID        string    `json:"id"`
	Reason    string    `json:"reason,omitempty"`
	Label     string    `json:"label,omitempty"`
	Status    string    `json:"status,omitempty"` // pending|submitted|cancelled|timeout
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Bus fans out secret-prompt events to SSE subscribers. Concurrent-safe.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

func (b *Bus) Subscribe() <-chan Event {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	for c := range b.subs {
		if c == ch {
			delete(b.subs, c)
			break
		}
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
