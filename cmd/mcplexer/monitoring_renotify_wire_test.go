package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// renotifySweepStarted reports whether the boot path consumed the sweep's
// sync.Once. If startMonitoringCollector launched the goroutine, the Once is
// already spent and this callback never runs.
func renotifySweepStarted() bool {
	started := true
	monitoringRenotifyOnce.Do(func() { started = false })
	return started
}

func newRenotifyWireDB(t *testing.T) store.Store {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "renotify-wire.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDaemonBootStartsRenotifySweep is the regression that keeps this feature
// alive. The persistence policy was already correct before this change and
// still produced twelve hours of silence, purely because nothing on a timer
// ever consulted it. A sweep that exists but is never started at boot is the
// same bug wearing a new file name, so the boot wiring itself is asserted.
func TestDaemonBootStartsRenotifySweep(t *testing.T) {
	resetMonitoringSingletons()
	t.Cleanup(resetMonitoringSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// secretsMgr is nil, so the collector cannot start — the sweep must be
	// independent of it, because SSH credentials have nothing to do with
	// re-notifying about an incident already in the database.
	startMonitoringCollector(ctx, newRenotifyWireDB(t), nil, nil, nil)

	if monitoringCollector != nil {
		t.Fatal("collector should not exist without a secrets manager")
	}
	if !renotifySweepStarted() {
		t.Fatal("daemon boot did not start the renotify sweep — the policy would never be re-evaluated")
	}
}

// TestViewerModeDoesNotStartRenotifySweep upholds the single-runner contract:
// paired viewer machines replicate the incidents but must not each send their
// own copy of every reminder.
func TestViewerModeDoesNotStartRenotifySweep(t *testing.T) {
	resetMonitoringSingletons()
	t.Cleanup(resetMonitoringSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "0")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	startMonitoringCollector(ctx, newRenotifyWireDB(t), nil, nil, nil)

	if renotifySweepStarted() {
		t.Fatal("viewer mode must not start the renotify sweep")
	}
}

// TestRenotifySweepStartsOncePerDaemon guards against a second gateway
// construction adding a second loop — every incident would be reminded about
// twice per tick.
func TestRenotifySweepStartsOncePerDaemon(t *testing.T) {
	resetMonitoringSingletons()
	t.Cleanup(resetMonitoringSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db := newRenotifyWireDB(t)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startMonitoringCollector(ctx, db, nil, nil, nil)
		}()
	}
	wg.Wait()

	if !renotifySweepStarted() {
		t.Fatal("concurrent boot calls must still start the sweep exactly once")
	}
}
