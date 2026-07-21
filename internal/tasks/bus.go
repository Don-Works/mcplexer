// bus.go — fan-out of task mutation events to SSE subscribers.
// Modelled on internal/approval/bus.go: each subscriber gets its own
// buffered channel, slow consumers drop events on publish rather than
// back-pressuring the mutation path.
package tasks

import (
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Event kinds.
const (
	EventTaskCreated      = "task_created"
	EventTaskUpdated      = "task_updated"
	EventTaskClaimed      = "task_claimed"
	EventTaskDeleted      = "task_deleted"
	EventTaskNoteAppended = "task_note_appended"
	EventTaskOfferUpdated = "task_offer_updated"
)

// Event is published on every observable task mutation. Task is the
// post-mutation row (or pre-mutation for delete, with DeletedAt set so
// the consumer can drop it). Note is set only for EventTaskNoteAppended;
// Offer is set only for EventTaskOfferUpdated. WorkspaceID is the local
// workspace the event pertains to — empty for inbound offers that have
// not been bound to a local workspace yet, so unfiltered subscribers
// still receive them while filtered subscribers ignore them.
type Event struct {
	Kind            string           `json:"kind"`
	WorkspaceID     string           `json:"workspace_id"`
	Task            *store.Task      `json:"task,omitempty"`
	AssigneeChanged bool             `json:"assignee_changed,omitempty"`
	Note            *store.TaskNote  `json:"note,omitempty"`
	Offer           *store.TaskOffer `json:"offer,omitempty"`
	At              time.Time        `json:"at"`
}

func taskAssigneeChanged(before, after *store.Task) bool {
	if before == nil || after == nil {
		return false
	}
	return before.AssigneeSessionID != after.AssigneeSessionID ||
		before.AssigneePeerID != after.AssigneePeerID ||
		before.AssigneeUserID != after.AssigneeUserID ||
		before.AssigneeOriginKind != after.AssigneeOriginKind
}

// Bus fans out task events to SSE subscribers. Zero-value Bus is not
// usable — call NewBus.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewBus constructs an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new listener and returns its read-only channel
// plus an unsubscribe func. Channel buffer is 32 — large enough to ride
// out a normal burst, small enough that a stuck consumer drops events
// rather than letting the publisher accumulate goroutines.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish sends an event to every subscriber without blocking. A full
// subscriber channel drops the event for that subscriber only; other
// subscribers still receive.
func (b *Bus) Publish(evt Event) {
	if b == nil {
		return
	}
	if evt.At.IsZero() {
		evt.At = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
