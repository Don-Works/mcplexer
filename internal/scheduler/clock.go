// Package scheduler is the Schedule Guard's catalog runner. One in-process
// goroutine + a min-heap ordered by next_run_at handles every job; the
// goroutine sleeps until the next due job, fires it through the approval
// manager (Surface="schedule"), executes via os/exec, updates the row,
// and re-heaps. Tunable for Pi Zero: heap operations are O(log n) and the
// sleep loop wakes ONLY when there's a due job, not on a fixed tick.
//
// Jobs with SurviveDaemonDown=true are also installed into the host's
// native scheduler (systemd-timer on Linux, launchd on macOS) so they
// fire even when mcplexer is down. The native driver execs back through
// `mcplexer run-job <id>` which contacts the daemon, or falls through
// to direct exec when the daemon is unreachable.
package scheduler

import "time"

// Clock is the testing seam — production uses RealClock{}.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer mirrors *time.Timer through an interface so fake clocks can
// supply a controllable channel.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// RealClock is the production Clock backed by time.Now / time.NewTimer.
type RealClock struct{}

// Now returns the wall clock time in UTC.
func (RealClock) Now() time.Time { return time.Now().UTC() }

// NewTimer returns a real time.Timer wrapped in the Timer interface.
func (RealClock) NewTimer(d time.Duration) Timer {
	if d < 0 {
		d = 0
	}
	return &realTimer{t: time.NewTimer(d)}
}

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }
