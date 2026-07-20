package escalate

// notify_delivery_test.go covers the two ways the dispatcher could silently
// fail to tell an operator about an unresolved incident: the throttle vetoing a
// deliberate re-notification, and a failed delivery reported as success.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// failingSender is a channel that always refuses, permanently (no retry).
type failingSender struct{ attempts int }

func (f *failingSender) Send(context.Context, *store.MonitoringChannel, string, string) error {
	f.attempts++
	return errors.New("channel endpoint rejected the message")
}

func errorFloorChannel() []*store.MonitoringChannel {
	return []*store.MonitoringChannel{{
		Name: "incidents", Kind: store.ChannelKindMesh,
		MinSeverity: store.SeverityError, Enabled: true, WorkspaceID: "ws",
	}}
}

// reminder is what the renotify sweep dispatches: an existing incident, never
// NewIncident, carrying the incident id rather than a template id.
func reminder(sev, incidentID string) distill.Notification {
	return distill.Notification{
		WorkspaceID: "ws", Severity: sev, Title: "Still unresolved: order sync stalled",
		Body:       "This incident is still recurring and has not been resolved.",
		IncidentID: incidentID, NewIncident: false,
	}
}

// cooldownCase drives the same incident twice, tc.elapsed apart.
type cooldownCase struct {
	name        string
	severity    string
	elapsed     time.Duration
	wantSends   int
	wantStatus  DeliveryStatus
	wantErrFree bool
}

func cooldownCases() []cooldownCase {
	return []cooldownCase{
		{
			name: "critical reminder at the policy's tightest cadence", severity: store.SeverityCritical,
			elapsed: 30 * time.Minute, wantSends: 2, wantStatus: StatusDelivered, wantErrFree: true,
		},
		{
			name: "critical reminder at the tier-1 escalation gap", severity: store.SeverityCritical,
			elapsed: 31 * time.Minute, wantSends: 2, wantStatus: StatusDelivered, wantErrFree: true,
		},
		{
			name: "critical duplicate chatter inside 15m is still throttled", severity: store.SeverityCritical,
			elapsed: 5 * time.Minute, wantSends: 1, wantStatus: StatusSuppressed, wantErrFree: true,
		},
		{
			name: "error reminder inside the hour is still throttled", severity: store.SeverityError,
			elapsed: 30 * time.Minute, wantSends: 1, wantStatus: StatusSuppressed, wantErrFree: true,
		},
	}
}

// TestDispatcher_CriticalReminderInsideCooldownIsDelivered is defect 1: the
// policy's tightest critical cadence is 30m, so a 1h dispatcher cooldown
// swallowed the first critical reminder while the sweep advanced the backoff
// regardless — the reminder was consumed, never delivered.
func TestDispatcher_CriticalReminderInsideCooldownIsDelivered(t *testing.T) {
	for _, tc := range cooldownCases() {
		t.Run(tc.name, func(t *testing.T) {
			sender := &captureSender{}
			d, now := newTestDispatcher(errorFloorChannel(),
				map[string]Sender{store.ChannelKindMesh: sender})
			n := reminder(tc.severity, "incident-1")

			if _, err := d.NotifyWithOutcome(context.Background(), n); err != nil {
				t.Fatalf("first dispatch: %v", err)
			}
			*now = now.Add(tc.elapsed)
			outcome, err := d.NotifyWithOutcome(context.Background(), n)

			if (err == nil) != tc.wantErrFree {
				t.Fatalf("error=%v wantErrFree=%v", err, tc.wantErrFree)
			}
			if outcome.Status != tc.wantStatus {
				t.Fatalf("status=%q want %q", outcome.Status, tc.wantStatus)
			}
			if len(sender.got) != tc.wantSends {
				t.Fatalf("sends=%d want %d", len(sender.got), tc.wantSends)
			}
		})
	}
}

// outcomeCase pins one notification against the four-state verdict.
type outcomeCase struct {
	name        string
	severity    string
	channelMin  string
	fails       bool
	wantStatus  DeliveryStatus
	wantTold    bool
	wantErr     bool
	wantAttempt int
}

func outcomeCases() []outcomeCase {
	return []outcomeCase{
		{
			name: "delivered", severity: store.SeverityCritical, channelMin: store.SeverityError,
			wantStatus: StatusDelivered, wantTold: true, wantAttempt: 1,
		},
		{
			name: "below the channel floor is a no-op, not a failure", severity: store.SeverityError,
			channelMin: store.SeverityCritical, wantStatus: StatusNotAttempted, wantAttempt: 0,
		},
		{
			name: "attempted and failed is not success", severity: store.SeverityCritical,
			channelMin: store.SeverityError, fails: true,
			wantStatus: StatusFailed, wantErr: true, wantAttempt: 1,
		},
	}
}

// TestDispatcher_ReminderOutcomeIsHonest is defect 2: a nil return used to mean
// "not a new incident", not "delivered". Success, a below-floor no-op, and a
// genuine delivery failure must all be tellable apart.
func TestDispatcher_ReminderOutcomeIsHonest(t *testing.T) {
	for _, tc := range outcomeCases() {
		t.Run(tc.name, func(t *testing.T) {
			var sender Sender = &captureSender{}
			if tc.fails {
				sender = &failingSender{}
			}
			d, _ := newTestDispatcher([]*store.MonitoringChannel{{
				Name: "incidents", Kind: store.ChannelKindMesh,
				MinSeverity: tc.channelMin, Enabled: true, WorkspaceID: "ws",
			}}, map[string]Sender{store.ChannelKindMesh: sender})
			d.retryPause = func(context.Context, time.Duration) error { return nil }

			outcome, err := d.NotifyWithOutcome(context.Background(), reminder(tc.severity, "incident-2"))

			if outcome.Status != tc.wantStatus {
				t.Fatalf("status=%q want %q", outcome.Status, tc.wantStatus)
			}
			if outcome.Told() != tc.wantTold {
				t.Fatalf("Told()=%v want %v", outcome.Told(), tc.wantTold)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
			if outcome.Attempted != tc.wantAttempt {
				t.Fatalf("attempted=%d want %d", outcome.Attempted, tc.wantAttempt)
			}
		})
	}
}

// TestDispatcher_ReminderFailureRetriesAreBounded: a failing route must be
// reported (so the sweep does not advance the backoff on a lie) but only for a
// bounded run, or a dead channel is hammered every 5m for the rest of the day.
func TestDispatcher_ReminderFailureRetriesAreBounded(t *testing.T) {
	sender := &failingSender{}
	d, _ := newTestDispatcher(errorFloorChannel(), map[string]Sender{store.ChannelKindMesh: sender})
	d.retryPause = func(context.Context, time.Duration) error { return nil }
	n := reminder(store.SeverityCritical, "incident-3")

	for attempt := 1; attempt <= maxReminderDeliveryRetries; attempt++ {
		if err := d.Notify(context.Background(), n); err == nil {
			t.Fatalf("attempt %d: failed delivery must be reported", attempt)
		}
	}
	// Budget exhausted: released to the policy so the incident's backoff can
	// advance and a fresh reminder is scheduled instead of a 5m retry storm.
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatalf("retry budget must release after %d attempts: %v", maxReminderDeliveryRetries, err)
	}
	// The next policy reminder gets a full budget of its own.
	if err := d.Notify(context.Background(), n); err == nil {
		t.Fatal("retry budget must re-arm for the next reminder")
	}
}

// TestDispatcher_DeliveredReminderClearsFailureBudget: a route that recovers
// must not carry its failure count into the next incident.
func TestDispatcher_DeliveredReminderClearsFailureBudget(t *testing.T) {
	sender := &scriptedSender{errs: []error{errors.New("endpoint down")}}
	d, _ := newTestDispatcher(errorFloorChannel(), map[string]Sender{store.ChannelKindMesh: sender})
	d.retryPause = func(context.Context, time.Duration) error { return nil }
	n := reminder(store.SeverityCritical, "incident-4")

	if err := d.Notify(context.Background(), n); err == nil {
		t.Fatal("first dispatch failed and must be reported")
	}
	outcome, err := d.NotifyWithOutcome(context.Background(), n)
	if err != nil || !outcome.Told() {
		t.Fatalf("recovered route: outcome=%+v err=%v", outcome, err)
	}
	if got := d.reminderFailures["ws/incident-4"]; got != 0 {
		t.Fatalf("failure budget not cleared on delivery: %d", got)
	}
}

// TestDispatcher_FailedDeliveryDoesNotArmTheCooldown pins the invariant that
// makes reminderResult's nil-on-suppressed honest: a throttle mark is written
// only after a channel actually accepted the message. If a failed send armed
// the cooldown, the very next reminder would come back StatusSuppressed —
// reported as "already told them" — and the sweep would advance the backoff for
// an operator who was never told. That is the original defect wearing a
// different hat, so it gets its own test rather than riding on another's.
func TestDispatcher_FailedDeliveryDoesNotArmTheCooldown(t *testing.T) {
	d, _ := newTestDispatcher(errorFloorChannel(),
		map[string]Sender{store.ChannelKindMesh: &failingSender{}})
	d.retryPause = func(context.Context, time.Duration) error { return nil }
	n := reminder(store.SeverityCritical, "incident-5")

	first, err := d.NotifyWithOutcome(context.Background(), n)
	if err == nil || first.Status != StatusFailed {
		t.Fatalf("first dispatch: status=%q err=%v want failed+error", first.Status, err)
	}
	// No clock movement: well inside the 15m critical cooldown.
	second, err := d.NotifyWithOutcome(context.Background(), n)
	if second.Status == StatusSuppressed {
		t.Fatal("a failed send armed the cooldown: the next reminder was " +
			"suppressed, so the sweep would advance the backoff on a delivery " +
			"that never happened")
	}
	if err == nil || second.Status != StatusFailed || second.Attempted != 1 {
		t.Fatalf("second dispatch: %+v err=%v want a fresh failed attempt", second, err)
	}
}
