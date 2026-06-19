// Package notify delivers user-facing notifications from the MCPlexer backend
// to the Electron shell and web UI via an event bus + SSE stream. An agent
// flags a mesh message with notify_user=true; the mesh manager publishes an
// Event on this bus; subscribers (SSE handlers) fan it out to clients.
//
// Persistence: when a Store is wired (via SetStore), Publish() durably
// records every event before fanning out. The Signal tray reads from the
// store on open; the SSE channel stays as the live push path.
package notify

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Event is a user-facing notification triggered by an agent.
//
// Source classifies the producer ("mesh" / "approval" / "system" /
// "secret") — used as the Signal tray's filter taxonomy. Kind is the
// producer's own sub-classification ("event" / "alert" / "question"
// for mesh; "pending" / "granted" / "denied" for approval; etc.).
// They're orthogonal: Source = who produced it, Kind = what it is.
//
// Link is an optional in-app deep-link target (e.g. "/mesh?msg=abc123") that
// the web UI uses to make the row clickable. Producers should populate it
// whenever there's a meaningful destination — agents publishing mesh messages
// point at /mesh, approval prompts point at /approvals, etc.
type Event struct {
	MessageID string    `json:"message_id"`
	Source    string    `json:"source"` // mesh / approval / system / secret
	AgentName string    `json:"agent_name"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Priority  string    `json:"priority"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      string    `json:"tags"`
	Link      string    `json:"link,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Bus fans out notification events to SSE subscribers and (optionally)
// persists them via Store for the Signal tray.
type Bus struct {
	mu    sync.RWMutex
	subs  map[<-chan Event]chan Event
	store Store // optional — set via SetStore to enable persistence
}

func NewBus() *Bus {
	return &Bus{subs: make(map[<-chan Event]chan Event)}
}

// SetStore enables durable persistence of every published Event. Without
// a store, the Bus is fire-and-forget (Signal tray sees only events
// arriving while the page is open).
func (b *Bus) SetStore(s Store) {
	b.mu.Lock()
	b.store = s
	b.mu.Unlock()
}

// Subscribe registers a new listener.
func (b *Bus) Subscribe() <-chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener. The channel is not closed — subscribers
// exit via ctx.Done() and the channel is GC'd when unreferenced.
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Publish persists (if a store is wired) then sends an event to all
// subscribers without blocking. Persistence errors are logged but
// non-fatal — the live channel still fires.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	store := b.store
	subs := b.subs
	b.mu.RUnlock()

	if store != nil {
		// Bounded background write — we don't want a slow DB to back
		// up publishers. 5s is generous for a single insert.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := store.Insert(ctx, evt); err != nil {
			slog.Warn("notify: persist failed",
				"message_id", evt.MessageID,
				"error", err,
			)
		}
		cancel()
	}

	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
