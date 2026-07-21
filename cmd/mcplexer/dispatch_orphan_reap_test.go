package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeDispatchOrphanReaper records every SweepOrphanedDispatches call so
// the wiring test can prove the sweep is actually started (boot pass +
// periodic ticker).
type fakeDispatchOrphanReaper struct {
	mu     sync.Mutex
	calls  int
	graces []time.Duration
}

func (f *fakeDispatchOrphanReaper) SweepOrphanedDispatches(_ context.Context, grace time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.graces = append(f.graces, grace)
	return 0, nil
}

func (f *fakeDispatchOrphanReaper) snapshot() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestStartDispatchOrphanReaperRunsBootAndPeriodicSweep FAILS if the
// reaper is not wired: it asserts both the synchronous boot sweep and at
// least one subsequent ticker sweep fire. This is the "boot-wiring test"
// guarding against shipping the sweep unstarted.
func TestStartDispatchOrphanReaperRunsBootAndPeriodicSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &fakeDispatchOrphanReaper{}
	startDispatchOrphanReaper(ctx, fake, 20*time.Millisecond, 0)

	// Boot sweep is synchronous — it must have already fired.
	if got := fake.snapshot(); got < 1 {
		t.Fatalf("boot sweep calls = %d, want >= 1 (sweep not started synchronously)", got)
	}

	// Ticker must fire at least once more.
	deadline := time.Now().Add(2 * time.Second)
	for fake.snapshot() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("periodic sweep calls = %d, want >= 2 (ticker not started)", fake.snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStartDispatchOrphanReaperNilIsNoOp confirms slim/stdio callers with
// no admin service don't panic.
func TestStartDispatchOrphanReaperNilIsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDispatchOrphanReaper(ctx, nil, 20*time.Millisecond, 0)
}

// TestStartDispatchOrphanReaperStopsOnContextCancel proves the ticker
// goroutine exits when the daemon lifecycle context is cancelled — no
// leak past shutdown.
func TestStartDispatchOrphanReaperStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeDispatchOrphanReaper{}
	startDispatchOrphanReaper(ctx, fake, 15*time.Millisecond, 0)

	// Let a couple of ticks land, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	stable := fake.snapshot()
	time.Sleep(60 * time.Millisecond) // several tick intervals
	if got := fake.snapshot(); got != stable {
		t.Fatalf("sweep kept running after cancel: %d → %d", stable, got)
	}
}
