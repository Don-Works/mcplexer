package mesh

import (
	"context"
	"log/slog"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// Subscriber is the callback signature for mesh-message subscribers (M4).
// Implementations are invoked SYNCHRONOUSLY from the Send / ingest path
// after InsertMeshMessage succeeds — keep the work fast (queue heavier
// processing in a goroutine inside the subscriber) so a slow subscriber
// can't stall a paired-peer dispatch.
//
// Errors are not surfaced through the return type by design: a
// subscriber that panics or blocks is a deployment defect we want to
// log + isolate, not propagate up through Send.
type Subscriber func(ctx context.Context, msg *store.MeshMessage)

// subscribers holds the registered callbacks behind an RWMutex so adds /
// removes are race-free against concurrent Sends. Stored on Manager via
// a separate field so the existing field set stays intact (the M4
// migration is additive).
type subscribers struct {
	mu     sync.RWMutex
	list   []*subscription
	nextID uint64
}

// subscription wraps a Subscriber with an id so the unsubscribe closure
// can find + remove this exact registration even when the same function
// has been subscribed multiple times.
type subscription struct {
	id uint64
	fn Subscriber
}

// Subscribe registers fn to be invoked after every successful mesh
// message insert. Returns an idempotent unsubscribe func. fn must not
// block; long work should fan out to its own goroutine inside fn.
//
// Nil-safe: passing a nil fn returns a no-op unsubscribe.
func (m *Manager) Subscribe(fn Subscriber) func() {
	if m == nil || fn == nil {
		return func() {}
	}
	if m.subs == nil {
		m.subs = &subscribers{}
	}
	m.subs.mu.Lock()
	m.subs.nextID++
	id := m.subs.nextID
	m.subs.list = append(m.subs.list, &subscription{id: id, fn: fn})
	m.subs.mu.Unlock()
	return func() {
		if m.subs == nil {
			return
		}
		m.subs.mu.Lock()
		defer m.subs.mu.Unlock()
		for i, s := range m.subs.list {
			if s.id == id {
				m.subs.list = append(m.subs.list[:i], m.subs.list[i+1:]...)
				return
			}
		}
	}
}

// nextID extension on subscribers — kept on the struct so subscriptions
// across different Manager instances don't share state.
//
// We initialise lazily in Subscribe; the zero value is fine because the
// first ever id will be 1 (incremented before use).

// notifySubscribers invokes every registered subscriber with msg. Called
// from Send (after InsertMeshMessage succeeds) and from the p2p inbound
// path (after ingestEnvelope inserts). Subscribers are invoked under a
// read lock so concurrent Sends can fan out in parallel, but a long
// subscriber WILL block other Sends from completing — that's why
// subscribers are documented as fast.
//
// Per-subscriber panics are caught + logged so one broken subscriber
// does not poison the rest.
func (m *Manager) notifySubscribers(ctx context.Context, msg *store.MeshMessage) {
	if m == nil || m.subs == nil || msg == nil {
		return
	}
	m.subs.mu.RLock()
	defer m.subs.mu.RUnlock()
	for _, sub := range m.subs.list {
		runSubscriberSafely(ctx, sub.fn, msg)
	}
}

// runSubscriberSafely invokes fn under a panic recover so a misbehaving
// subscriber surfaces as a slog.Warn instead of crashing the mesh send.
func runSubscriberSafely(ctx context.Context, fn Subscriber, msg *store.MeshMessage) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("mesh subscriber panicked",
				"msg_id", msg.ID,
				"recover", r,
			)
		}
	}()
	fn(ctx, msg)
}
