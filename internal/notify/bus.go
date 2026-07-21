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
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	// ErrNoStore means a caller requested durable publication before the
	// notification store was wired.
	ErrNoStore = errors.New("notify: durable store is not configured")
	// ErrNoDispatcher means a caller requested an interrupting delivery but
	// no out-of-browser dispatcher (for example Web Push) was wired.
	ErrNoDispatcher = errors.New("notify: push dispatcher is not configured")
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
	mu         sync.RWMutex
	subs       map[<-chan Event]chan Event
	store      Store      // optional — set via SetStore to enable persistence
	dispatcher Dispatcher // optional — sends durable out-of-browser push
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

// Dispatcher receives every published notification after the bus has taken
// its persistence snapshot. Regular Publish calls invoke it asynchronously;
// PublishDurable can wait for an accepted out-of-browser delivery.
type Dispatcher interface {
	Dispatch(ctx context.Context, evt Event) error
}

// SetDispatcher wires an optional out-of-process notification sender, such as
// standards-based Web Push for installed PWAs.
func (b *Bus) SetDispatcher(d Dispatcher) {
	b.mu.Lock()
	b.dispatcher = d
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

// Publish persists (if a store is wired), fans out locally, and starts any
// out-of-browser delivery asynchronously. Errors remain non-fatal for this
// best-effort API; critical producers should use PublishDurable.
func (b *Bus) Publish(evt Event) {
	if err := b.publish(context.Background(), evt, false, true, false); err != nil {
		slog.Warn("notify: publish failed", "message_id", evt.MessageID, "error", err)
	}
}

// PublishDurable requires local persistence before local fan-out. When
// interrupt is true it also waits until the configured out-of-browser
// dispatcher accepts the event, allowing critical producers to observe a
// broken or missing push path instead of silently claiming delivery.
func (b *Bus) PublishDurable(ctx context.Context, evt Event, interrupt bool) error {
	return b.publish(ctx, evt, true, interrupt, interrupt)
}

func (b *Bus) publish(
	ctx context.Context, evt Event, requireStore, dispatch, waitDispatch bool,
) error {
	snapshot := b.snapshot()
	if evt.CreatedAt.IsZero() {
		evt.CreatedAt = time.Now().UTC()
	}
	if err := persistEvent(ctx, snapshot.store, evt, requireStore); err != nil {
		return err
	}
	fanOut(snapshot.subs, evt)
	return dispatchEvent(ctx, snapshot.dispatcher, evt, dispatch, waitDispatch)
}

type busSnapshot struct {
	store      Store
	dispatcher Dispatcher
	subs       []chan Event
}

func (b *Bus) snapshot() busSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	subs := make([]chan Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	return busSnapshot{store: b.store, dispatcher: b.dispatcher, subs: subs}
}

func persistEvent(ctx context.Context, store Store, evt Event, required bool) error {
	if store == nil && required {
		return ErrNoStore
	}
	if store == nil {
		return nil
	}
	persistCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := store.Insert(persistCtx, evt)
	cancel()
	if err == nil {
		return nil
	}
	if required {
		return fmt.Errorf("notify: persist %s: %w", evt.MessageID, err)
	}
	slog.Warn("notify: persist failed; continuing live delivery",
		"message_id", evt.MessageID, "error", err)
	return nil
}

func fanOut(subs []chan Event, evt Event) {
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

func dispatchEvent(
	ctx context.Context, dispatcher Dispatcher, evt Event, dispatch, wait bool,
) error {
	if !dispatch {
		return nil
	}
	if dispatcher == nil {
		if wait {
			return ErrNoDispatcher
		}
		return nil
	}
	if wait {
		if err := dispatcher.Dispatch(ctx, evt); err != nil {
			return fmt.Errorf("notify: push %s: %w", evt.MessageID, err)
		}
		return nil
	}
	go func() {
		if err := dispatcher.Dispatch(context.Background(), evt); err != nil {
			slog.Warn("notify: async push failed", "message_id", evt.MessageID, "error", err)
		}
	}()
	return nil
}
