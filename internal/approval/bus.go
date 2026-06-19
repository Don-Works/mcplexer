package approval

import (
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// ApprovalEvent is published when an approval is created or resolved.
type ApprovalEvent struct {
	Type     string              `json:"type"` // "pending" or "resolved"
	Approval *store.ToolApproval `json:"approval"`
}

// Bus fans out approval events to SSE subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[<-chan ApprovalEvent]chan ApprovalEvent
}

// NewBus creates a new approval event bus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[<-chan ApprovalEvent]chan ApprovalEvent),
	}
}

// Subscribe registers a new listener.
func (b *Bus) Subscribe() <-chan ApprovalEvent {
	ch := make(chan ApprovalEvent, 64)
	b.mu.Lock()
	b.subs[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener. The channel is not closed — the subscriber
// loop uses ctx.Done() to exit, and the channel is GC'd once unreferenced.
func (b *Bus) Unsubscribe(ch <-chan ApprovalEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Publish sends an event to all subscribers without blocking.
func (b *Bus) Publish(evt ApprovalEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
