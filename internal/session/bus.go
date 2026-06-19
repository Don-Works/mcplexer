package session

import (
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// EventType identifies whether a session was connected or disconnected.
type EventType string

const (
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
)

// Event represents a session lifecycle change.
type Event struct {
	Type    EventType     `json:"type"`
	Session store.Session `json:"session"`
}

// Bus fans out session events to SSE subscribers in real time.
type Bus struct {
	mu   sync.RWMutex
	subs map[<-chan Event]chan Event
}

// NewBus creates a new session event bus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[<-chan Event]chan Event),
	}
}

// Subscribe registers a new listener and returns a receive-only channel.
func (b *Bus) Subscribe() <-chan Event {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener and closes its channel so consumers blocked
// on receive unblock immediately. Double-unsubscribe is safe (no-op).
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	if bidir, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(bidir)
	}
	b.mu.Unlock()
}

// Publish sends an event to all subscribers without blocking.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
