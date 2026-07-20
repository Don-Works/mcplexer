package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/tasks"
)

// This file exists because the failure mode it guards has now happened
// repeatedly on this feature: correct, well-tested code that NOTHING CALLS.
// Migration 145 shipped a pure absence evaluator with zero callers, which is
// precisely why a hung order-sync job produced 7h39m of silence with monitoring
// "green". Tests that drive startMonitoringBaseline directly cannot catch that —
// they prove the function works, not that the daemon runs it.
//
// So the boot path is asserted at BOTH levels:
//
//  1. TestCollectorBootStartsBaselineLoops drives the daemon's single monitoring
//     boot entry point and fails if the baseline call is removed from it.
//  2. TestServeBootWiresMonitoringCollector reads serve.go and fails if the boot
//     entry point itself stops being called, or is called without the task
//     service the evaluator needs. That is the one link no runtime test can
//     observe, because nothing in a test binary executes serve().

// TestCollectorBootStartsBaselineLoops is the level-1 guard. startMonitoringCollector
// is the daemon's one monitoring boot call; deleting the startMonitoringBaseline
// line inside it fails this test.
func TestCollectorBootStartsBaselineLoops(t *testing.T) {
	resetBaselineSingletons()
	t.Cleanup(resetBaselineSingletons)
	t.Setenv("MCPLEXER_MONITORING_RUNNER", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db := newBaselineWireDB(t)
	// secretsMgr is nil, so the collector itself cannot start. Baseline
	// learning and absence evaluation must be independent of SSH credentials:
	// they read rows the daemon already holds.
	startMonitoringCollector(ctx, db, nil, nil, tasks.New(db))

	if monitoringCollector != nil {
		t.Fatal("collector should not exist without a secrets manager")
	}
	learner, evaluator := baselineLoopsStarted()
	if !learner {
		t.Error("daemon boot did not start the baseline learner — " +
			"no expected-signal rule would ever be created, and the absence " +
			"evaluator would have nothing to evaluate")
	}
	if !evaluator {
		t.Error("daemon boot did not start the absence evaluator — " +
			"store.EvaluateExpectedSignal would go back to having zero callers, " +
			"which is the exact state that produced the 7h39m silence")
	}
}

// TestServeBootWiresMonitoringCollector is the level-2 guard. It reads the
// daemon's own source, because the link between serve() and the monitoring boot
// entry point is not reachable from a test binary — and an unexercised line of
// boot code is exactly how this feature died the first time.
func TestServeBootWiresMonitoringCollector(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	// Either wiring shape is accepted: baseline learning currently hangs off
	// startMonitoringCollector (so it is reachable from test 1 above), but a
	// direct startMonitoringBaseline call from serve.go is equally valid. What
	// is NOT acceptable is neither.
	call := regexp.MustCompile(
		`startMonitoring(?:Collector|Baseline)\([^)]*\)`).FindString(string(src))
	if call == "" {
		t.Fatal("serve.go no longer wires monitoring boot — every monitoring loop " +
			"(collector, renotify sweep, baseline learner, absence evaluator) " +
			"is dead code without it")
	}
	// The evaluator raises incidents that hang off a canonical task, so it is
	// silently skipped when the task service is not passed through. A boot call
	// that drops the argument degrades to a logged no-op rather than a build
	// failure, so it is asserted here.
	if !strings.Contains(call, "d.tasksSvc") {
		t.Errorf("serve.go boot call %q does not pass the task service; "+
			"the absence evaluator would log a warning and never start", call)
	}
}
