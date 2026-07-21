package sqlite

// Pure-policy coverage for the operator pause (migration 150): an ack or silence
// mutes the routine "still broken" nag, but a severity escalation past the level
// the operator acted at always pierces it. These drive monitoringNotificationDue
// directly with hand-built incidents so the escalation-piercing contract is
// pinned independently of any storage.

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMonitoringNotificationPauseSemantics(t *testing.T) {
	at := monitoringT0
	tests := []struct {
		name       string
		now        time.Time
		mut        func(*store.MonitoringIncident)
		wantNotify bool
		wantReason string
	}{
		{
			// Without a pause this is a due persistent re-notify (see the base
			// policy test). An in-force ack at the current effective severity
			// mutes it.
			name: "ack holds the routine nag", now: monitoringT0.Add(time.Hour),
			mut: func(i *store.MonitoringIncident) {
				i.AckedAt = &at
				i.AckedSeverity = store.SeverityError
			},
			wantNotify: false,
		},
		{
			name: "ack does not survive a classifier escalation",
			now:  monitoringT0.Add(5 * time.Minute),
			mut: func(i *store.MonitoringIncident) {
				i.Severity, i.LastNotifiedSeverity = store.SeverityError, store.SeverityWarn
				i.AckedAt = &at
				i.AckedSeverity = store.SeverityWarn // acked while it was warn
			},
			wantNotify: true, wantReason: monitoringReasonSeverityEscalation,
		},
		{
			name: "ack does not survive an age escalation",
			now:  monitoringT0.Add(monitoringAgeEscalateTier1),
			mut: func(i *store.MonitoringIncident) {
				i.Severity, i.LastNotifiedSeverity = store.SeverityWarn, store.SeverityWarn
				i.AckedAt = &at
				i.AckedSeverity = store.SeverityWarn // floor before it aged past a tier
			},
			wantNotify: true, wantReason: monitoringReasonAgeEscalation,
		},
		{
			name: "active silence holds the nag", now: monitoringT0.Add(time.Hour),
			mut: func(i *store.MonitoringIncident) {
				until := monitoringT0.Add(2 * time.Hour)
				i.SilencedUntil = &until
				i.SilencedSeverity = store.SeverityError
			},
			wantNotify: false,
		},
		{
			name: "expired silence re-notifies a still-active incident",
			now:  monitoringT0.Add(time.Hour),
			mut: func(i *store.MonitoringIncident) {
				until := monitoringT0.Add(30 * time.Minute) // already past
				i.SilencedUntil = &until
				i.SilencedSeverity = store.SeverityError
			},
			wantNotify: true, wantReason: monitoringReasonPersistent,
		},
		{
			name: "silence does not survive an escalation",
			now:  monitoringT0.Add(5 * time.Minute),
			mut: func(i *store.MonitoringIncident) {
				i.Severity, i.LastNotifiedSeverity = store.SeverityError, store.SeverityWarn
				until := monitoringT0.Add(2 * time.Hour)
				i.SilencedUntil = &until
				i.SilencedSeverity = store.SeverityWarn
			},
			wantNotify: true, wantReason: monitoringReasonSeverityEscalation,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := monitoringNotificationDue(monitoringIncidentAt(tc.now, tc.mut), false, tc.now)
			if got.Notify != tc.wantNotify || got.Reason != tc.wantReason {
				t.Fatalf("decision = notify:%v reason:%q, want notify:%v reason:%q",
					got.Notify, got.Reason, tc.wantNotify, tc.wantReason)
			}
		})
	}
}
