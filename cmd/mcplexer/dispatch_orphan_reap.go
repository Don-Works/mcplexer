package main

import (
	"context"
	"log/slog"
	"time"
)

// dispatchOrphanSweepInterval is the periodic tick for the dispatch-orphan
// reaper. A dispatch orphaned during the daemon's life (the worker
// goroutine died without a restart) resolves within grace + one interval;
// an orphan left by the PRIOR process is caught by the synchronous boot
// sweep below. Kept a few minutes so the ledger self-heals promptly
// without hot-looping the delegation scan.
const dispatchOrphanSweepInterval = 5 * time.Minute

// dispatchOrphanReaper is the narrow slice of *workersadmin.Service the
// boot wiring needs — declared as an interface so the wiring test can
// supply a fake and assert the sweep is actually started.
type dispatchOrphanReaper interface {
	SweepOrphanedDispatches(ctx context.Context, grace time.Duration) (int, error)
}

// startDispatchOrphanReaper runs an immediate synchronous boot sweep — to
// resolve delegation dispatches orphaned by the prior process (worker died
// or the daemon restarted between DISPATCH and the run-row insert) — then
// starts a periodic ticker for the life of ctx. grace <= 0 defers to the
// service default (admin.DefaultDispatchOrphanGrace). A nil reaper is a
// no-op so slim/stdio callers don't panic.
func startDispatchOrphanReaper(ctx context.Context, reaper dispatchOrphanReaper, interval, grace time.Duration) {
	if reaper == nil {
		return
	}
	if interval <= 0 {
		interval = dispatchOrphanSweepInterval
	}
	sweep := func() {
		if n, err := reaper.SweepOrphanedDispatches(ctx, grace); err != nil {
			slog.Warn("dispatch orphan reaper: sweep failed", "error", err)
		} else if n > 0 {
			slog.Info("dispatch orphan reaper: reaped dispatched-but-never-ran delegations",
				"reaped", n)
		}
	}
	// Boot recovery first, synchronously, so the boot log reflects it and
	// the 5-known-stuck backlog clears on the first tick after deploy.
	sweep()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
