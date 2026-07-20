package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// resetBaselineSingletons clears the two baseline loops' Onces alongside the
// shared monitoring singletons, so each test observes a fresh boot.
func resetBaselineSingletons() {
	resetMonitoringSingletons()
	monitoringLearnOnce = sync.Once{}
	monitoringEvalOnce = sync.Once{}
}

// baselineLoopsStarted reports whether boot consumed each loop's sync.Once. If
// the goroutine was launched, the Once is spent and the callback never runs.
func baselineLoopsStarted() (learner bool, evaluator bool) {
	learner, evaluator = true, true
	monitoringLearnOnce.Do(func() { learner = false })
	monitoringEvalOnce.Do(func() { evaluator = false })
	return learner, evaluator
}

func newBaselineWireDB(t *testing.T) store.Store {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "baseline-wire.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDaemonBootStartsBaselineLoops is the regression that keeps this feature
// alive. Migration 145 shipped a correct absence evaluator that nothing ever
// called, which is why a hung job produced twelve hours of silence. A learner
// and an evaluator that exist but are never started at boot are the same bug
// wearing new file names, so the boot wiring itself is asserted.
func TestDaemonBootStartsBaselineLoops(t *testing.T) {
	resetBaselineSingletons()
	t.Cleanup(resetBaselineSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db := newBaselineWireDB(t)
	// buildMonitoring supplies the dispatcher the evaluator notifies through.
	buildMonitoring(db, nil, nil)
	startMonitoringBaseline(ctx, db, tasks.New(db))

	learner, evaluator := baselineLoopsStarted()
	if !learner {
		t.Error("daemon boot did not start the baseline learner — no rule would ever be created")
	}
	if !evaluator {
		t.Error("daemon boot did not start the absence evaluator — no rule would ever be checked")
	}
}

// TestViewerModeDoesNotStartBaselineLoops upholds the single-runner contract:
// viewer machines replicate the data but must not each learn and alert.
func TestViewerModeDoesNotStartBaselineLoops(t *testing.T) {
	resetBaselineSingletons()
	t.Cleanup(resetBaselineSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "0")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db := newBaselineWireDB(t)
	buildMonitoring(db, nil, nil)
	startMonitoringBaseline(ctx, db, tasks.New(db))

	if learner, evaluator := baselineLoopsStarted(); learner || evaluator {
		t.Errorf("viewer mode started loops (learner=%v evaluator=%v)", learner, evaluator)
	}
}

// TestBaselineLearnerStartsWithoutTaskService proves the loops degrade
// independently: a daemon that cannot raise incidents should still be learning
// what normal looks like, so it is ready the moment it can.
func TestBaselineLearnerStartsWithoutTaskService(t *testing.T) {
	resetBaselineSingletons()
	t.Cleanup(resetBaselineSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db := newBaselineWireDB(t)
	buildMonitoring(db, nil, nil)
	startMonitoringBaseline(ctx, db, nil)

	learner, evaluator := baselineLoopsStarted()
	if !learner {
		t.Error("the learner must not depend on the task service")
	}
	if evaluator {
		t.Error("the evaluator must not start without a task service to hang incidents off")
	}
}

// TestBaselineLoopsStartOncePerDaemon guards against a second boot path adding
// a second learner — two would race on rule creation — or a second evaluator,
// which would double every absence alert.
func TestBaselineLoopsStartOncePerDaemon(t *testing.T) {
	resetBaselineSingletons()
	t.Cleanup(resetBaselineSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db := newBaselineWireDB(t)
	buildMonitoring(db, nil, nil)
	tasksSvc := tasks.New(db)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startMonitoringBaseline(ctx, db, tasksSvc)
		}()
	}
	wg.Wait()

	learner, evaluator := baselineLoopsStarted()
	if !learner || !evaluator {
		t.Errorf("concurrent boot calls must still start each loop exactly once "+
			"(learner=%v evaluator=%v)", learner, evaluator)
	}
}

// TestSqliteSatisfiesBaselineInterfaces is a compile-and-assert guard: the
// wiring uses type assertions, so a store that silently stopped satisfying an
// interface would degrade to a logged no-op instead of a build failure.
func TestSqliteSatisfiesBaselineInterfaces(t *testing.T) {
	db := newBaselineWireDB(t)
	if _, ok := db.(store.MonitoringBaselineStore); !ok {
		t.Error("sqlite.DB no longer satisfies MonitoringBaselineStore — " +
			"the learner would silently never start")
	}
	if _, ok := db.(store.MonitoringExpectedSignalStore); !ok {
		t.Error("sqlite.DB no longer satisfies MonitoringExpectedSignalStore — " +
			"the evaluator would silently never start")
	}
}
