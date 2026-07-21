package downstream

import (
	"testing"
	"time"
)

// TestHealthTracker_ThresholdAndBackoff covers the core stuck-detector
// state machine: counter, window reset, backoff after auto-reload.
func TestHealthTracker_ThresholdAndBackoff(t *testing.T) {
	prevThresh := StuckThresholdCount
	prevWindow := StuckThresholdWindow
	prevMin := MinReloadBackoff
	prevMax := MaxReloadBackoff
	t.Cleanup(func() {
		StuckThresholdCount = prevThresh
		StuckThresholdWindow = prevWindow
		MinReloadBackoff = prevMin
		MaxReloadBackoff = prevMax
	})
	// Tighten for test speed.
	StuckThresholdCount = 3
	StuckThresholdWindow = 60 * time.Second
	MinReloadBackoff = 60 * time.Second
	MaxReloadBackoff = 5 * time.Minute

	tr := NewHealthTracker()
	const sid = "srv-1"
	t0 := time.Now()

	// Establish the server as once-healthy: auto-reload only fires for
	// servers that have served at least one success (see RecordFailure).
	tr.RecordSuccess(sid, t0)

	// First failure — not enough.
	should, _ := tr.RecordFailure(sid, "boom", t0)
	if should {
		t.Fatalf("first failure should not trip reload")
	}
	// Second failure within window — still not enough.
	should, _ = tr.RecordFailure(sid, "boom", t0.Add(1*time.Second))
	if should {
		t.Fatalf("second failure should not trip reload")
	}
	// Third failure within window — must trip.
	should, snap := tr.RecordFailure(sid, "boom", t0.Add(2*time.Second))
	if !should {
		t.Fatalf("third failure within window should trip reload")
	}
	if snap.ConsecutiveFailures != 3 {
		t.Errorf("ConsecutiveFailures = %d, want 3", snap.ConsecutiveFailures)
	}

	// Caller would now perform the reload + mark it.
	tr.MarkReload(sid, t0.Add(2*time.Second))

	// Immediate 4th failure — within backoff, must NOT trip again.
	should, _ = tr.RecordFailure(sid, "boom again", t0.Add(3*time.Second))
	if should {
		t.Fatalf("failure within backoff window should not trip")
	}
	// Even 3 more failures inside backoff don't trip.
	should, _ = tr.RecordFailure(sid, "boom", t0.Add(4*time.Second))
	if should {
		t.Fatalf("counter inside backoff should not trip")
	}
	should, _ = tr.RecordFailure(sid, "boom", t0.Add(5*time.Second))
	if should {
		t.Fatalf("counter inside backoff should not trip (2)")
	}

	// Advance past backoff + 3 failures -> trips again with doubled backoff (120s).
	tBeyond := t0.Add(2*time.Second + MinReloadBackoff + 1*time.Second)
	should, _ = tr.RecordFailure(sid, "boom", tBeyond)
	if !should {
		t.Fatalf("failure beyond backoff should trip; lastReload+backoff was %v, now %v", t0.Add(2*time.Second).Add(MinReloadBackoff), tBeyond)
	}
	tr.MarkReload(sid, tBeyond)

	snap = tr.Snapshot(sid, tBeyond)
	if snap.AutoReloads24h != 2 {
		t.Errorf("AutoReloads24h = %d, want 2", snap.AutoReloads24h)
	}
}

// TestHealthTracker_WindowResetsCounter verifies that a stray failure
// outside the StuckThresholdWindow does NOT add to the streak.
func TestHealthTracker_WindowResetsCounter(t *testing.T) {
	prevThresh := StuckThresholdCount
	prevWindow := StuckThresholdWindow
	t.Cleanup(func() {
		StuckThresholdCount = prevThresh
		StuckThresholdWindow = prevWindow
	})
	StuckThresholdCount = 3
	StuckThresholdWindow = 60 * time.Second

	tr := NewHealthTracker()
	const sid = "srv-flaky"
	t0 := time.Now()

	// One failure now, one 10 minutes later — the gap exceeds the
	// window, so the second one starts a fresh streak.
	_, _ = tr.RecordFailure(sid, "x", t0)
	_, _ = tr.RecordFailure(sid, "x", t0.Add(10*time.Minute))
	snap := tr.Snapshot(sid, t0.Add(10*time.Minute))
	if snap.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1 (window reset)", snap.ConsecutiveFailures)
	}
}

// TestHealthTracker_SuccessResets verifies a single success wipes the
// counter so the next stuck-detection cycle starts fresh.
func TestHealthTracker_SuccessResets(t *testing.T) {
	tr := NewHealthTracker()
	const sid = "srv-recovered"
	t0 := time.Now()

	_, _ = tr.RecordFailure(sid, "x", t0)
	_, _ = tr.RecordFailure(sid, "x", t0.Add(1*time.Second))
	tr.RecordSuccess(sid, t0.Add(2*time.Second))

	snap := tr.Snapshot(sid, t0.Add(2*time.Second))
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 after success", snap.ConsecutiveFailures)
	}
	if snap.LastSuccessAt.IsZero() {
		t.Errorf("LastSuccessAt should be set")
	}
	if snap.LastFailureReason != "" {
		t.Errorf("LastFailureReason = %q, want \"\" after success", snap.LastFailureReason)
	}
}

// TestHealthTracker_NeverHealthyDoesNotAutoReload pins the flood fix: a
// server that has never served a single successful response (disabled,
// auth-required, bad command, downstream 404 / no init) must NOT trip the
// auto-reload path no matter how many times it fails — reloading evicts
// nothing and the "auto-recovered" mesh alert it would fire is both false
// and the dominant flood source. Once the server proves it can work, the
// stuck-detector engages normally.
func TestHealthTracker_NeverHealthyDoesNotAutoReload(t *testing.T) {
	prevThresh := StuckThresholdCount
	prevWindow := StuckThresholdWindow
	t.Cleanup(func() {
		StuckThresholdCount = prevThresh
		StuckThresholdWindow = prevWindow
	})
	StuckThresholdCount = 3
	StuckThresholdWindow = 60 * time.Second

	tr := NewHealthTracker()
	const sid = "srv-broken"
	t0 := time.Now()

	// Five consecutive failures, all within the window — would trip a
	// once-healthy server twice over, but this one has never succeeded.
	for i := range 5 {
		should, _ := tr.RecordFailure(sid, "downstream server is disabled", t0.Add(time.Duration(i)*time.Second))
		if should {
			t.Fatalf("failure %d tripped auto-reload for a never-healthy server", i+1)
		}
	}

	// After a single success the safety net engages: 3 fresh failures trip.
	tr.RecordSuccess(sid, t0.Add(10*time.Second))
	var should bool
	for i := range 3 {
		should, _ = tr.RecordFailure(sid, "call timeout", t0.Add(time.Duration(11+i)*time.Second))
	}
	if !should {
		t.Fatalf("once-healthy server should trip auto-reload after 3 failures")
	}
}

// TestHealthTracker_SnapshotMissingServer returns a zero snapshot with
// just the ID populated rather than panicking on a never-seen server —
// useful for the /health endpoint hitting a freshly-installed server.
func TestHealthTracker_SnapshotMissingServer(t *testing.T) {
	tr := NewHealthTracker()
	snap := tr.Snapshot("nonexistent", time.Now())
	if snap.ServerID != "nonexistent" {
		t.Errorf("ServerID = %q, want nonexistent", snap.ServerID)
	}
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures should default to 0")
	}
}

// TestHealthTracker_BackoffCap ensures the exponential backoff never
// exceeds MaxReloadBackoff regardless of how many reloads have fired.
func TestHealthTracker_BackoffCap(t *testing.T) {
	prevMin := MinReloadBackoff
	prevMax := MaxReloadBackoff
	t.Cleanup(func() {
		MinReloadBackoff = prevMin
		MaxReloadBackoff = prevMax
	})
	MinReloadBackoff = 1 * time.Second
	MaxReloadBackoff = 4 * time.Second

	tr := NewHealthTracker()
	const sid = "srv"
	now := time.Now()
	tr.MarkReload(sid, now) // backoff -> 1s
	tr.MarkReload(sid, now) // -> 2s
	tr.MarkReload(sid, now) // -> 4s (cap)
	tr.MarkReload(sid, now) // -> 4s (still cap)

	snap := tr.Snapshot(sid, now)
	gap := snap.NextReloadEligible.Sub(snap.LastAutoReloadAt)
	if gap > MaxReloadBackoff {
		t.Errorf("backoff exceeded cap: %v > %v", gap, MaxReloadBackoff)
	}
}

// TestHealthTracker_BackoffDelayAccessor verifies the BackoffDelay method
// returns the current backoff for a known server and zero for unknown ones.
func TestHealthTracker_BackoffDelayAccessor(t *testing.T) {
	tr := NewHealthTracker()
	const sid = "srv"

	// Unknown server returns zero.
	if d := tr.BackoffDelay(sid); d != 0 {
		t.Fatalf("BackoffDelay for unknown server = %v, want 0", d)
	}

	// After MarkReload, backoff should equal MinReloadBackoff.
	now := time.Now()
	tr.MarkReload(sid, now)
	if d := tr.BackoffDelay(sid); d != MinReloadBackoff {
		t.Fatalf("BackoffDelay after first reload = %v, want %v", d, MinReloadBackoff)
	}

	// Second reload doubles.
	tr.MarkReload(sid, now)
	if d := tr.BackoffDelay(sid); d != 2*MinReloadBackoff {
		t.Fatalf("BackoffDelay after second reload = %v, want %v", d, 2*MinReloadBackoff)
	}

	// Empty serverID returns zero.
	if d := tr.BackoffDelay(""); d != 0 {
		t.Fatalf("BackoffDelay(\"\") = %v, want 0", d)
	}
}
