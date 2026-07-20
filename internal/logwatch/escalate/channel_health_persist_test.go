package escalate

// channel_health_persist_test.go — proves the durable half is actually wired.
//
// The in-memory run was already correct and already logged. What was missing
// was that anything outside the process could see it, so these tests assert the
// dispatcher REACHES the recorder on the paths that matter: every failure
// (including the ones the log deliberately suppresses) and every success.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// recorderCall is one persisted health event.
type recorderCall struct {
	channelID string
	success   bool
	reason    string
}

type fakeHealthRecorder struct {
	mu    sync.Mutex
	calls []recorderCall
	err   error
}

func (f *fakeHealthRecorder) RecordMonitoringChannelFailure(
	_ context.Context, id string, _ time.Time, reason string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recorderCall{channelID: id, reason: reason})
	return f.err
}

func (f *fakeHealthRecorder) RecordMonitoringChannelSuccess(
	_ context.Context, id string, _ time.Time,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recorderCall{channelID: id, success: true})
	return f.err
}

func (f *fakeHealthRecorder) snapshot() []recorderCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorderCall(nil), f.calls...)
}

// identifiedChannel is a route with a real ID, as loaded from the store.
func identifiedChannel() []*store.MonitoringChannel {
	return []*store.MonitoringChannel{{
		ID: "chan-1", Name: "incidents", Kind: store.ChannelKindMesh,
		MinSeverity: store.SeverityError, Enabled: true, WorkspaceID: "ws",
	}}
}

// TestBrokenThresholdMatchesStore is the anti-drift guard. The dispatcher
// escalates to ERROR at its threshold and the API reports `broken` at the
// store's; if the two ever diverge, the product tells an operator their channel
// is healthy while its own log says it is broken.
func TestBrokenThresholdMatchesStore(t *testing.T) {
	if channelUnhealthyThreshold != store.ChannelBrokenThreshold {
		t.Fatalf("dispatcher threshold %d != store.ChannelBrokenThreshold %d — "+
			"the log and the API would disagree about the same channel",
			channelUnhealthyThreshold, store.ChannelBrokenThreshold)
	}
}

// TestFailureIsPersistedEveryTime is the heart of the six-day defect. The ERROR
// log fires at most once an hour by design, so persistence must NOT sit behind
// that cadence gate: the stored counter has to see every failure or the run is
// undercounted and the route takes hours to read broken instead of attempts.
func TestFailureIsPersistedEveryTime(t *testing.T) {
	sender := &failingSender{}
	d, _ := newTestDispatcher(identifiedChannel(),
		map[string]Sender{store.ChannelKindMesh: sender})
	rec := &fakeHealthRecorder{}
	d.RegisterChannelHealthRecorder(rec)

	const attempts = 5
	for i := 0; i < attempts; i++ {
		// Test notifications bypass the throttle, so each one genuinely
		// reaches the channel — this isolates persistence from suppression.
		n := testNotification(store.SeverityError, "tpl-persist")
		n.Test = true
		_, _ = d.NotifyWithOutcome(context.Background(), n)
	}

	calls := rec.snapshot()
	if len(calls) != attempts {
		t.Fatalf("persisted %d failures, want %d — the cadence gate is "+
			"swallowing them", len(calls), attempts)
	}
	for i, c := range calls {
		if c.success {
			t.Fatalf("call %d recorded a success against a failing sender", i)
		}
		if c.channelID != "chan-1" {
			t.Fatalf("call %d channel id = %q, want chan-1", i, c.channelID)
		}
		// The underlying error must reach the row: "delivery failed" alone
		// cannot distinguish a 400 bad token from a DNS failure, and that
		// distinction is the entire diagnosis.
		if !strings.Contains(c.reason, "delivery failed") ||
			!strings.Contains(c.reason, "rejected the message") {
			t.Fatalf("call %d reason = %q, want the failure AND its cause", i, c.reason)
		}
	}
}

// TestSuccessIsPersistedEveryTime: last_success_at is the question an operator
// asks first, and it is only truthful if each delivery stamps it — not only the
// delivery that happens to be a recovery.
func TestSuccessIsPersistedEveryTime(t *testing.T) {
	d, _ := newTestDispatcher(identifiedChannel(),
		map[string]Sender{store.ChannelKindMesh: &captureSender{}})
	rec := &fakeHealthRecorder{}
	d.RegisterChannelHealthRecorder(rec)

	for i := 0; i < 3; i++ {
		n := testNotification(store.SeverityError, "tpl-ok")
		n.Test = true
		if _, err := d.NotifyWithOutcome(context.Background(), n); err != nil {
			t.Fatalf("notify %d: %v", i, err)
		}
	}

	calls := rec.snapshot()
	if len(calls) != 3 {
		t.Fatalf("persisted %d successes, want 3", len(calls))
	}
	for i, c := range calls {
		if !c.success {
			t.Fatalf("call %d recorded a failure against a working sender", i)
		}
	}
}

// TestRecoveryPersistsSuccessAfterFailures: a route that comes back must clear
// its stored run. A channel that recovers but keeps reading broken is the same
// defect pointed the other way — it trains the operator to ignore the field.
func TestRecoveryPersistsSuccessAfterFailures(t *testing.T) {
	swap := &swappableSender{err: errors.New("channel endpoint rejected the message")}
	d, _ := newTestDispatcher(identifiedChannel(),
		map[string]Sender{store.ChannelKindMesh: swap})
	rec := &fakeHealthRecorder{}
	d.RegisterChannelHealthRecorder(rec)

	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		n := testNotification(store.SeverityError, "tpl-recover")
		n.Test = true
		_, _ = d.NotifyWithOutcome(context.Background(), n)
	}
	swap.setErr(nil) // operator fixes the webhook
	n := testNotification(store.SeverityError, "tpl-recover")
	n.Test = true
	if _, err := d.NotifyWithOutcome(context.Background(), n); err != nil {
		t.Fatalf("notify after fix: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != store.ChannelBrokenThreshold+1 {
		t.Fatalf("persisted %d calls, want %d", len(calls), store.ChannelBrokenThreshold+1)
	}
	if last := calls[len(calls)-1]; !last.success {
		t.Fatal("recovery did not persist a success — the route would keep reading broken")
	}
}

// TestPersistenceFailureDoesNotBreakDelivery: health writes are bookkeeping
// about a delivery that already happened. A wedged database must not turn a
// successful send into a failed one.
func TestPersistenceFailureDoesNotBreakDelivery(t *testing.T) {
	d, _ := newTestDispatcher(identifiedChannel(),
		map[string]Sender{store.ChannelKindMesh: &captureSender{}})
	d.RegisterChannelHealthRecorder(&fakeHealthRecorder{err: errors.New("database is locked")})

	n := testNotification(store.SeverityError, "tpl-dberr")
	n.Test = true
	outcome, err := d.NotifyWithOutcome(context.Background(), n)
	if err != nil {
		t.Fatalf("delivery reported an error because bookkeeping failed: %v", err)
	}
	if outcome.Delivered == 0 {
		t.Fatal("delivered = 0 — a health-write error must not unmake a real delivery")
	}
}

// TestNoRecorderIsInert: the recorder is optional, and a dispatcher without one
// must behave exactly as it did before this work.
func TestNoRecorderIsInert(t *testing.T) {
	d, _ := newTestDispatcher(identifiedChannel(),
		map[string]Sender{store.ChannelKindMesh: &failingSender{}})

	n := testNotification(store.SeverityError, "tpl-norecorder")
	n.Test = true
	if _, err := d.NotifyWithOutcome(context.Background(), n); err == nil {
		t.Fatal("expected a delivery error from the failing sender")
	}
}

// swappableSender fails until its error is cleared, modelling an operator
// fixing a broken webhook mid-run.
type swappableSender struct {
	mu  sync.Mutex
	err error
}

func (s *swappableSender) Send(context.Context, *store.MonitoringChannel, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *swappableSender) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}
