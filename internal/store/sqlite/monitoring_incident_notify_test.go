package sqlite

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

var monitoringT0 = time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)

// monitoringIncidentAt builds an incident that is unresolved and still being
// observed at now, then applies the case-specific mutation.
func monitoringIncidentAt(now time.Time, mut func(*store.MonitoringIncident)) *store.MonitoringIncident {
	notified := monitoringT0
	i := &store.MonitoringIncident{
		ID: "inc-1", WorkspaceID: "ws-1", ClassKey: "template:tpl-1", TaskID: "task-1",
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityError,
		Title: "sftp transfer failing", OccurrenceCount: 8, EventCount: 40,
		FirstSeen: monitoringT0, LastSeen: now,
		LastNotifiedAt: &notified, LastNotifiedSeverity: store.SeverityError,
	}
	if mut != nil {
		mut(i)
	}
	return i
}

func TestMonitoringNotificationDuePolicy(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		newIncident bool
		mut         func(*store.MonitoringIncident)
		wantNotify  bool
		wantReason  string
		wantSev     string
	}{
		{
			name: "new incident notifies", now: monitoringT0, newIncident: true,
			mut: func(i *store.MonitoringIncident) {
				i.LastNotifiedAt, i.LastNotifiedSeverity, i.OccurrenceCount = nil, "", 1
			},
			wantNotify: true, wantReason: monitoringReasonNewIncident, wantSev: store.SeverityError,
		},
		{
			name: "recorded but never delivered notifies", now: monitoringT0.Add(5 * time.Minute),
			mut: func(i *store.MonitoringIncident) {
				i.LastNotifiedAt, i.LastNotifiedSeverity = nil, ""
			},
			wantNotify: true, wantReason: monitoringReasonUnnotified, wantSev: store.SeverityError,
		},
		{
			name: "steady severity inside quiet period stays silent",
			now:  monitoringT0.Add(30 * time.Minute),
			mut:  nil, wantNotify: false, wantSev: store.SeverityError,
		},
		{
			name: "steady severity past quiet period still active re-notifies",
			now:  monitoringT0.Add(time.Hour),
			mut:  nil, wantNotify: true, wantReason: monitoringReasonPersistent,
			wantSev: store.SeverityError,
		},
		{
			name: "classifier escalation bypasses the quiet period",
			now:  monitoringT0.Add(5 * time.Minute),
			mut: func(i *store.MonitoringIncident) {
				i.Severity, i.LastNotifiedSeverity = store.SeverityError, store.SeverityWarn
			},
			wantNotify: true, wantReason: monitoringReasonSeverityEscalation,
			wantSev: store.SeverityError,
		},
		{
			name: "below warn never notifies however old", now: monitoringT0.Add(48 * time.Hour),
			mut: func(i *store.MonitoringIncident) {
				i.Severity, i.LastNotifiedSeverity = store.SeverityInfo, store.SeverityInfo
				i.OccurrenceCount = 400
			},
			wantNotify: false, wantSev: store.SeverityInfo,
		},
		{
			name: "incident that stopped recurring stops nagging",
			now:  monitoringT0.Add(6 * time.Hour),
			mut: func(i *store.MonitoringIncident) {
				i.LastSeen = monitoringT0.Add(10 * time.Minute)
			},
			wantNotify: false, wantSev: store.SeverityError,
		},
		{
			name: "benign disposition is muted", now: monitoringT0.Add(8 * time.Hour),
			mut: func(i *store.MonitoringIncident) {
				i.Disposition = store.MonitoringDispositionBenign
			},
			wantNotify: false, wantSev: store.SeverityError,
		},
		{
			name: "a fast burst inside one bucket does not escalate by volume",
			now:  monitoringT0.Add(5 * time.Hour),
			mut: func(i *store.MonitoringIncident) {
				i.OccurrenceCount, i.EventCount = 1, 50000
				i.FirstSeen = monitoringT0.Add(5*time.Hour - time.Minute)
				lastNotified := i.FirstSeen
				i.LastNotifiedAt = &lastNotified
			},
			wantNotify: false, wantSev: store.SeverityError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := monitoringNotificationDue(monitoringIncidentAt(tc.now, tc.mut), tc.newIncident, tc.now)
			if got.Notify != tc.wantNotify || got.Reason != tc.wantReason {
				t.Fatalf("decision = notify:%v reason:%q, want notify:%v reason:%q",
					got.Notify, got.Reason, tc.wantNotify, tc.wantReason)
			}
			if got.EffectiveSeverity != tc.wantSev {
				t.Fatalf("effective severity = %q, want %q", got.EffectiveSeverity, tc.wantSev)
			}
		})
	}
}

// Age gets a bounded reminder but cannot turn a warn into an error/critical.
func TestMonitoringAgeReminderKeepsClassifierSeverity(t *testing.T) {
	tier1 := monitoringT0.Add(monitoringAgeEscalateTier1)
	warn := func(now time.Time, notifiedAt time.Time, notifiedSev string) *store.MonitoringIncident {
		return monitoringIncidentAt(now, func(i *store.MonitoringIncident) {
			i.Severity = store.SeverityWarn
			at := notifiedAt
			i.LastNotifiedAt, i.LastNotifiedSeverity = &at, notifiedSev
		})
	}
	first := monitoringNotificationDue(warn(tier1, monitoringT0, store.SeverityWarn), false, tier1)
	if !first.Notify || first.Reason != monitoringReasonAgeEscalation ||
		first.EffectiveSeverity != store.SeverityWarn {
		t.Fatalf("tier-1 crossing = %+v, want age reminder at warn", first)
	}
	// Once notified, the same tier must not fire again — whether the caller
	// recorded the effective severity or the raw classifier severity.
	for _, sev := range []string{store.SeverityWarn} {
		after := tier1.Add(5 * time.Minute)
		got := monitoringNotificationDue(warn(after, tier1, sev), false, after)
		if got.Notify {
			t.Fatalf("tier-1 re-fired with last_notified_severity=%q: %+v", sev, got)
		}
	}
	tier2 := monitoringT0.Add(monitoringAgeEscalateTier2)
	second := monitoringNotificationDue(warn(tier2, tier1, store.SeverityError), false, tier2)
	if !second.Notify || second.Reason != monitoringReasonAgeEscalation ||
		second.EffectiveSeverity != store.SeverityWarn {
		t.Fatalf("tier-2 crossing = %+v, want age reminder at warn", second)
	}
}

// simulateRenotifyGaps walks a minute at a time, re-notifying whenever the
// policy says so, and returns the gaps between successive notifications. Age
// escalation is held out (single occurrence bucket) so this isolates backoff.
func simulateRenotifyGaps(severity string, span time.Duration) []time.Duration {
	lastNotified := monitoringT0
	gaps := []time.Duration{}
	for step := time.Minute; step <= span; step += time.Minute {
		now := monitoringT0.Add(step)
		notified := lastNotified
		i := monitoringIncidentAt(now, func(i *store.MonitoringIncident) {
			i.Severity, i.LastNotifiedSeverity = severity, severity
			i.OccurrenceCount, i.LastNotifiedAt = 1, &notified
		})
		if d := monitoringNotificationDue(i, false, now); d.Notify {
			gaps = append(gaps, now.Sub(lastNotified))
			lastNotified = now
		}
	}
	return gaps
}

func TestMonitoringRenotifyBackoffWidensAndCaps(t *testing.T) {
	h := time.Hour
	tests := []struct {
		name     string
		severity string
		span     time.Duration
		want     []time.Duration
	}{
		{
			name: "error backoff", severity: store.SeverityError, span: 40 * h,
			want: []time.Duration{1 * h, 2 * h, 4 * h, 8 * h, 12 * h, 12 * h},
		},
		{
			name: "critical nags fastest", severity: store.SeverityCritical, span: 16 * h,
			want: []time.Duration{30 * time.Minute, 1 * h, 2 * h, 4 * h, 4 * h, 4 * h},
		},
		{
			name: "warn stays quiet and settles at a day", severity: store.SeverityWarn, span: 80 * h,
			want: []time.Duration{4 * h, 8 * h, 16 * h, 24 * h, 24 * h},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := simulateRenotifyGaps(tc.severity, tc.span)
			if len(got) != len(tc.want) {
				t.Fatalf("gaps = %v, want %v", got, tc.want)
			}
			for idx := range tc.want {
				if got[idx] != tc.want[idx] {
					t.Fatalf("gaps = %v, want %v", got, tc.want)
				}
				if idx > 0 && got[idx] < got[idx-1] {
					t.Fatalf("backoff narrowed at %d: %v", idx, got)
				}
			}
		})
	}
}
