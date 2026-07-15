package notify

import (
	"context"
	"errors"
	"testing"
)

type durableTestStore struct {
	err      error
	inserted []Event
}

func (s *durableTestStore) Insert(_ context.Context, evt Event) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.inserted = append(s.inserted, evt)
	return int64(len(s.inserted)), nil
}
func (*durableTestStore) List(context.Context, ListFilter) ([]StoredEvent, error) { return nil, nil }
func (*durableTestStore) MarkRead(context.Context, []int64) error                 { return nil }
func (*durableTestStore) MarkAllRead(context.Context) error                       { return nil }
func (*durableTestStore) UnreadCount(context.Context) (int, error)                { return 0, nil }
func (*durableTestStore) Prune(context.Context, int) (int, error)                 { return 0, nil }

type durableTestDispatcher struct {
	err   error
	calls int
}

func (d *durableTestDispatcher) Dispatch(context.Context, Event) error {
	d.calls++
	return d.err
}

func TestPublishDurableRequiresPersistenceAndInterruptRoute(t *testing.T) {
	evt := Event{MessageID: "critical-1", Priority: "critical"}
	b := NewBus()
	if err := b.PublishDurable(context.Background(), evt, true); !errors.Is(err, ErrNoStore) {
		t.Fatalf("without store error=%v", err)
	}

	store := &durableTestStore{}
	b.SetStore(store)
	ch := b.Subscribe()
	if err := b.PublishDurable(context.Background(), evt, true); !errors.Is(err, ErrNoDispatcher) {
		t.Fatalf("without dispatcher error=%v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("durable event not persisted before route check: %d", len(store.inserted))
	}
	select {
	case got := <-ch:
		if got.MessageID != evt.MessageID {
			t.Fatalf("fanout message=%q", got.MessageID)
		}
	default:
		t.Fatal("durable event was not fanned out locally")
	}
}

func TestPublishDurableSurfacesPersistenceAndPushFailures(t *testing.T) {
	evt := Event{MessageID: "critical-2", Priority: "critical"}
	store := &durableTestStore{err: errors.New("disk full")}
	dispatcher := &durableTestDispatcher{}
	b := NewBus()
	b.SetStore(store)
	b.SetDispatcher(dispatcher)
	if err := b.PublishDurable(context.Background(), evt, true); err == nil {
		t.Fatal("persistence failure was swallowed")
	}
	if dispatcher.calls != 0 {
		t.Fatal("push ran before durable persistence")
	}

	store.err = nil
	dispatcher.err = errors.New("push rejected")
	if err := b.PublishDurable(context.Background(), evt, true); err == nil {
		t.Fatal("push failure was swallowed")
	}
	if dispatcher.calls != 1 {
		t.Fatalf("dispatcher calls=%d", dispatcher.calls)
	}
}

func TestPublishDurableCanRecordWithoutInterrupting(t *testing.T) {
	store := &durableTestStore{}
	dispatcher := &durableTestDispatcher{}
	b := NewBus()
	b.SetStore(store)
	b.SetDispatcher(dispatcher)
	if err := b.PublishDurable(context.Background(), Event{MessageID: "capped"}, false); err != nil {
		t.Fatal(err)
	}
	if len(store.inserted) != 1 || dispatcher.calls != 0 {
		t.Fatalf("inserted=%d dispatcher_calls=%d", len(store.inserted), dispatcher.calls)
	}
}

func TestBestEffortPublishStillFansOutWhenPersistenceFails(t *testing.T) {
	b := NewBus()
	b.SetStore(&durableTestStore{err: errors.New("disk full")})
	ch := b.Subscribe()
	b.Publish(Event{MessageID: "live-only"})
	select {
	case evt := <-ch:
		if evt.MessageID != "live-only" {
			t.Fatalf("message=%q", evt.MessageID)
		}
	default:
		t.Fatal("best-effort publish dropped live fan-out after persistence error")
	}
}
