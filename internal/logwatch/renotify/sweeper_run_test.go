package renotify

import (
	"context"
	"testing"
	"time"
)

// TestRunSweepsOnItsTicker proves the loop actually re-asks. A Run that
// compiled but never ticked would restore the original silence exactly.
func TestRunSweepsOnItsTicker(t *testing.T) {
	st := &fakeStore{workspaces: oneWorkspace()}
	s := newSweeper(st, &fakeNotifier{}, sweepClock)
	s.interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for st.listCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("Run did not sweep repeatedly: %d sweeps in 5s", st.listCount())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	s := newSweeper(&fakeStore{workspaces: oneWorkspace()}, &fakeNotifier{}, sweepClock)
	s.interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// TestNewUsesTheDocumentedDefaults pins the cadence justification: 5m is a
// sixth of the tightest quiet period the policy can produce (30m, critical),
// so the sweep never becomes the dominant source of notification latency.
func TestNewUsesTheDocumentedDefaults(t *testing.T) {
	s := New(&fakeStore{}, &fakeNotifier{})
	if s.interval != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", s.interval)
	}
	if s.limit != 100 {
		t.Fatalf("limit = %d, want 100", s.limit)
	}
}

func TestNewRequiresBothDependencies(t *testing.T) {
	if New(nil, &fakeNotifier{}) != nil {
		t.Fatal("nil store must not produce a sweeper")
	}
	if New(&fakeStore{}, nil) != nil {
		t.Fatal("nil notifier must not produce a sweeper")
	}
}
