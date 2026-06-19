package runner

import (
	"testing"
	"time"
)

func TestRunBus_PublishFanOutToSubscribers(t *testing.T) {
	b := NewRunBus()
	a := b.Subscribe()
	c := b.Subscribe()
	defer b.Unsubscribe(a)
	defer b.Unsubscribe(c)

	ev := &RunEvent{Kind: RunEventKindStatus, RunID: "run-1"}
	b.Publish(ev)

	for i, ch := range []<-chan *RunEvent{a, c} {
		select {
		case got := <-ch:
			if got != ev {
				t.Fatalf("sub %d got %+v, want %+v", i, got, ev)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("sub %d timed out waiting for event", i)
		}
	}
}

func TestRunBus_UnsubscribeClosesChannel(t *testing.T) {
	b := NewRunBus()
	ch := b.Subscribe()
	b.Unsubscribe(ch)
	if _, ok := <-ch; ok {
		t.Fatalf("expected channel closed after unsubscribe")
	}
}

func TestRunBus_NilSafe(t *testing.T) {
	var b *RunBus
	b.Publish(&RunEvent{Kind: RunEventKindStatus})
	b2 := NewRunBus()
	b2.Publish(nil)
}

func TestRunBus_SlowConsumerDoesNotBlockPublisher(t *testing.T) {
	b := NewRunBus()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			b.Publish(&RunEvent{Kind: RunEventKindUsage, RunID: "run-x", InputTokens: i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked behind slow subscriber")
	}
}
