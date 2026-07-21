package session

import (
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestBusSubscribePublishReceive(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe()

	evt := Event{Type: EventConnected, Session: store.Session{ID: "s1"}}
	bus.Publish(evt)

	select {
	case got := <-ch:
		if got.Type != EventConnected {
			t.Fatalf("expected %q, got %q", EventConnected, got.Type)
		}
		if got.Session.ID != "s1" {
			t.Fatalf("expected session ID %q, got %q", "s1", got.Session.ID)
		}
	default:
		t.Fatal("expected to receive event, channel empty")
	}
}

func TestBusFanOut(t *testing.T) {
	bus := NewBus()
	subs := make([]<-chan Event, 5)
	for i := range subs {
		subs[i] = bus.Subscribe()
	}

	evt := Event{Type: EventDisconnected, Session: store.Session{ID: "s2"}}
	bus.Publish(evt)

	for i, ch := range subs {
		select {
		case got := <-ch:
			if got.Session.ID != "s2" {
				t.Fatalf("subscriber %d: expected session ID %q, got %q", i, "s2", got.Session.ID)
			}
		default:
			t.Fatalf("subscriber %d: expected event, channel empty", i)
		}
	}
}

func TestBusUnsubscribeClosesChannel(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe()

	bus.Unsubscribe(ch)

	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after unsubscribe")
	}
}

func TestBusNoSendAfterUnsubscribe(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe()

	bus.Unsubscribe(ch)

	evt := Event{Type: EventConnected, Session: store.Session{ID: "s3"}}
	bus.Publish(evt)

	_, ok := <-ch
	if ok {
		t.Fatal("expected closed channel (no event), got data")
	}
}

func TestBusDoubleUnsubscribeIsSafe(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe()

	bus.Unsubscribe(ch)
	bus.Unsubscribe(ch)

	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed")
	}
}

func TestBusUnsubscribeUnderContention(t *testing.T) {
	bus := NewBus()
	const n = 100

	var wg sync.WaitGroup
	subs := make([]<-chan Event, n)
	for i := range subs {
		subs[i] = bus.Subscribe()
	}

	wg.Add(n)
	for _, ch := range subs {
		go func(c <-chan Event) {
			defer wg.Done()
			bus.Unsubscribe(c)
		}(ch)
	}
	wg.Wait()

	evt := Event{Type: EventConnected, Session: store.Session{ID: "s4"}}
	bus.Publish(evt)

	for _, ch := range subs {
		_, ok := <-ch
		if ok {
			t.Fatal("expected all channels to be closed")
		}
	}
}

func TestBusPublishDoesNotBlockOnFullChannel(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe()

	for i := 0; i < 32; i++ {
		bus.Publish(Event{Type: EventConnected, Session: store.Session{ID: "full"}})
	}

	bus.Publish(Event{Type: EventDisconnected, Session: store.Session{ID: "overflow"}})

	var count int
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 32 {
		t.Fatalf("expected 32 events (buffer size), got %d", count)
	}

	bus.Unsubscribe(ch)
}
