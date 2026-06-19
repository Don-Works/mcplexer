package notify

import (
	"testing"
	"time"
)

func TestBusPublishFanout(t *testing.T) {
	b := NewBus()
	a := b.Subscribe()
	c := b.Subscribe()

	go b.Publish(Event{MessageID: "m1", Title: "hi"})

	for i, ch := range []<-chan Event{a, c} {
		select {
		case evt := <-ch:
			if evt.MessageID != "m1" {
				t.Fatalf("subscriber %d got %q", i, evt.MessageID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

func TestBusPublishNonBlockingWhenFull(t *testing.T) {
	b := NewBus()
	_ = b.Subscribe()

	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Publish(Event{MessageID: "x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on full subscriber")
	}
}

func TestBusUnsubscribeStopsDelivery(t *testing.T) {
	b := NewBus()
	ch := b.Subscribe()
	b.Unsubscribe(ch)

	b.Publish(Event{MessageID: "m1"})

	select {
	case evt := <-ch:
		t.Fatalf("received after unsubscribe: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}
