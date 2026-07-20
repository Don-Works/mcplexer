package escalate

// notify_persistence_test.go models a critical incident that stays broken for
// 24 hours, driving the dispatcher with the exact reminder schedule the
// persistence policy produces. The question it answers is the operator's:
// across a full day of an unresolved critical incident, how many times was I
// actually told?

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// The policy lives in internal/store/sqlite/monitoring_incident_notify.go and is
// unexported, so these mirror its critical-path constants. If the policy's
// cadence changes, this mirror must be updated with it — the throttle's job is
// to stay out of the way of whatever the policy decides, and that is what the
// combined count below is checking.
const (
	policyBaseCritical = 30 * time.Minute
	policyCapCritical  = 4 * time.Hour
	policyAgeTier1     = 4 * time.Hour
	policyAgeTier2     = 12 * time.Hour
	policySweepTick    = 5 * time.Minute
)

func policyAgeTier(age time.Duration) int {
	switch {
	case age >= policyAgeTier2:
		return 2
	case age >= policyAgeTier1:
		return 1
	default:
		return 0
	}
}

// policyRenotifyInterval doubles the quiet period from the critical base up to
// its cap, mirroring monitoringRenotifyInterval.
func policyRenotifyInterval(age time.Duration) time.Duration {
	interval := policyBaseCritical
	for interval < policyCapCritical && interval*2 <= age {
		interval *= 2
	}
	return min(interval, policyCapCritical)
}

// policyReminderDue mirrors monitoringPersistenceDue for an incident that is
// sustained, still being observed, and held at critical.
func policyReminderDue(age, sinceNotified, notifiedAtAge time.Duration) bool {
	if policyAgeTier(age) > policyAgeTier(notifiedAtAge) {
		return true
	}
	return sinceNotified >= policyRenotifyInterval(age)
}

// TestDispatcher_PersistentCriticalIncidentOverFullDay: the policy schedules 10
// reminders across 24h. Every one of them must survive the dispatcher throttle,
// and the total must stay in the band that is neither silence nor spam.
func TestDispatcher_PersistentCriticalIncidentOverFullDay(t *testing.T) {
	sender := &captureSender{}
	d, now := newTestDispatcher(errorFloorChannel(),
		map[string]Sender{store.ChannelKindMesh: sender})
	start := *now
	n := reminder(store.SeverityCritical, "incident-24h")

	// t=0: the first notification, from the distiller, of a new incident.
	first := n
	first.NewIncident = true
	if err := d.Notify(context.Background(), first); err != nil {
		t.Fatalf("initial notification: %v", err)
	}
	scheduled, lastNotified := 1, time.Duration(0)

	for tick := policySweepTick; tick <= 24*time.Hour; tick += policySweepTick {
		age := tick
		if !policyReminderDue(age, age-lastNotified, lastNotified) {
			continue
		}
		scheduled++
		lastNotified = age
		*now = start.Add(tick)
		// The sweep marks the incident notified on a nil return, so a nil here
		// means this reminder is consumed whether or not it was delivered.
		if err := d.Notify(context.Background(), n); err != nil {
			t.Fatalf("reminder at %v: %v", age, err)
		}
	}

	if scheduled != 10 {
		t.Fatalf("policy scheduled %d reminders over 24h, expected 10", scheduled)
	}
	delivered := len(sender.got)
	if delivered != scheduled {
		t.Fatalf("policy scheduled %d messages but only %d were delivered: "+
			"the dispatcher throttle is swallowing reminders the policy deliberately raised",
			scheduled, delivered)
	}
	// The band the operator cares about: more than the single alert that let
	// the original incident sit unnoticed for 12h, and short of a spam volume
	// that gets alerting muted.
	if delivered <= 1 || delivered > 12 {
		t.Fatalf("delivered %d messages over 24h, want >1 and <=12", delivered)
	}
}
