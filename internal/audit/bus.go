package audit

import (
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// Bus fans out audit records to SSE subscribers in real time.
type Bus struct {
	mu   sync.RWMutex
	subs map[<-chan *store.AuditRecord]chan *store.AuditRecord
}

// NewBus creates a new audit event bus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[<-chan *store.AuditRecord]chan *store.AuditRecord),
	}
}

// Subscribe registers a new listener and returns a receive-only channel.
// The caller must call Unsubscribe when done.
func (b *Bus) Subscribe() <-chan *store.AuditRecord {
	ch := make(chan *store.AuditRecord, 64)
	b.mu.Lock()
	b.subs[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener and closes its channel.
func (b *Bus) Unsubscribe(ch <-chan *store.AuditRecord) {
	b.mu.Lock()
	if send, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(send)
	}
	b.mu.Unlock()
}

// Publish sends a record to all subscribers without blocking.
// Slow consumers that can't keep up will miss events.
func (b *Bus) Publish(rec *store.AuditRecord) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- rec:
		default:
		}
	}
}
