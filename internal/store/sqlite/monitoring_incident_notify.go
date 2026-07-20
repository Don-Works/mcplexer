package sqlite

import (
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Deterministic incident-persistence policy.
//
// Gap A (2026-07-20 incident): an incident held at a steady severity was
// notified exactly once and then went silent forever, so "still broken" and
// "fixed" were indistinguishable to the operator for ~12h. Every rule below is
// computed in Go from columns the daemon already writes (first_seen, last_seen,
// last_notified_at, last_notified_severity, occurrence_count, disposition).
// No model is consulted, no prompt grows, no extra model wake-up is scheduled —
// the classifier's job shrinks, because it is handed a pre-computed fact
// instead of being asked to notice persistence.
const (
	// monitoringRenotifyBase* is the quiet period before the FIRST re-notify of
	// an incident that is still being observed. Cadence is a function of
	// severity per the operator's stated preference: interrupt decisively for
	// genuine critical incidents, leave routine ones quiet.
	monitoringRenotifyBaseWarn     = 4 * time.Hour
	monitoringRenotifyBaseError    = time.Hour
	monitoringRenotifyBaseCritical = 30 * time.Minute

	// monitoringRenotifyCap* bounds the doubling backoff so a long-lived
	// incident settles at a floor cadence rather than decaying into silence
	// again. Warn ends at one message a day; a genuine critical is never
	// allowed to go more than half a working shift without a reminder.
	monitoringRenotifyCapWarn     = 24 * time.Hour
	monitoringRenotifyCapError    = 12 * time.Hour
	monitoringRenotifyCapCritical = 4 * time.Hour

	// monitoringAgeEscalateTier1/2 raise the EFFECTIVE severity of an incident
	// that is still recurring, so it eventually crosses channel min_severity
	// floors (which default to "error") and actually reaches the operator.
	// Sustained duration is the signal, never raw volume: a single template
	// repeating fast cannot escalate itself. 4h is about half a working shift;
	// 12h is the overnight blind window that let the original incident sit
	// unnoticed until the operator found it himself the next day.
	monitoringAgeEscalateTier1 = 4 * time.Hour
	monitoringAgeEscalateTier2 = 12 * time.Hour

	// monitoringIncidentActiveWindow is how long after LastSeen an incident
	// still counts as "being observed". LastSeen advances on every mapped log
	// batch with zero AI involvement, so it is a free liveness heartbeat. One
	// hour is four occurrence buckets: long enough not to flap on a sparse
	// signal, short enough that an incident which stopped recurring goes quiet
	// promptly instead of nagging forever.
	monitoringIncidentActiveWindow = time.Hour

	// monitoringSustainedOccurrences is the minimum number of distinct 15m
	// occurrence buckets required before age escalation applies. Buckets are a
	// duration proxy (one bucket = one 15-minute window touched), not a volume
	// counter — a burst inside one bucket never qualifies.
	monitoringSustainedOccurrences = 2
)

// Notification reasons. The first three are the pre-existing vocabulary and
// must keep their exact wire values; callers and dashboards switch on them.
const (
	monitoringReasonNewIncident        = "new_incident"
	monitoringReasonUnnotified         = "unnotified_incident"
	monitoringReasonSeverityEscalation = "severity_escalation"
	monitoringReasonAgeEscalation      = "age_escalation"
	monitoringReasonPersistent         = "persistent_incident"
)

// monitoringSeverityLadder is store's severity order, indexed by SeverityRank.
var monitoringSeverityLadder = []string{
	store.SeverityInfo, store.SeverityWarn, store.SeverityError, store.SeverityCritical,
}

// monitoringNotificationDecision is the deterministic verdict for one incident.
type monitoringNotificationDecision struct {
	Notify bool
	Reason string
	// EffectiveSeverity is the classifier severity raised by sustained age.
	// Dispatch and record notifications with this, not the raw severity.
	EffectiveSeverity string
}

// monitoringNotificationDue is pure — now is passed in rather than read from
// the clock, so the policy is deterministic and identical wherever it runs.
func monitoringNotificationDue(
	i *store.MonitoringIncident, newIncident bool, now time.Time,
) monitoringNotificationDecision {
	if i == nil {
		return monitoringNotificationDecision{}
	}
	silent := monitoringNotificationDecision{EffectiveSeverity: monitoringEffectiveSeverity(i, now)}
	// The warn floor is applied to the CLASSIFIER severity, before any age
	// escalation: info-level noise must never become notifiable by ageing.
	if store.SeverityRank(i.Severity) < store.SeverityRank(store.SeverityWarn) {
		return silent
	}
	// "benign" is the existing disposition vocabulary for judged-not-a-problem.
	if i.Disposition == store.MonitoringDispositionBenign {
		return silent
	}
	notify := func(reason string) monitoringNotificationDecision {
		return monitoringNotificationDecision{
			Notify: true, Reason: reason, EffectiveSeverity: silent.EffectiveSeverity,
		}
	}
	if i.LastNotifiedAt == nil {
		if newIncident {
			return notify(monitoringReasonNewIncident)
		}
		return notify(monitoringReasonUnnotified)
	}
	// A classifier severity escalation always bypasses the backoff.
	if severityHigher(i.Severity, i.LastNotifiedSeverity) {
		return notify(monitoringReasonSeverityEscalation)
	}
	// Stopped recurring: stop nagging. Resolution is represented by LastSeen
	// going stale, which needs no extra state and no worker to declare it.
	if !monitoringIncidentActive(i, now) {
		return silent
	}
	return monitoringPersistenceDue(i, now, silent, notify)
}

// monitoringPersistenceDue covers the two "still broken" paths for an incident
// that is unresolved, still being observed, and already notified once.
func monitoringPersistenceDue(
	i *store.MonitoringIncident, now time.Time,
	silent monitoringNotificationDecision,
	notify func(string) monitoringNotificationDecision,
) monitoringNotificationDecision {
	lastNotified := *i.LastNotifiedAt
	// Age escalation fires exactly once per tier boundary. The comparison is
	// between tiers, not severities, so it stays correct even if a caller
	// records the raw classifier severity when marking the incident notified.
	if monitoringSustained(i) &&
		monitoringAgeTier(now.Sub(i.FirstSeen)) > monitoringAgeTier(lastNotified.Sub(i.FirstSeen)) {
		return notify(monitoringReasonAgeEscalation)
	}
	// Otherwise re-notify on a widening backoff so a persistent incident keeps
	// saying it is still broken without becoming spam.
	interval := monitoringRenotifyInterval(silent.EffectiveSeverity, now.Sub(i.FirstSeen))
	if now.Sub(lastNotified) >= interval {
		return notify(monitoringReasonPersistent)
	}
	return silent
}

// monitoringEffectiveSeverity raises severity with sustained age only. Volume
// is deliberately absent from this computation.
func monitoringEffectiveSeverity(i *store.MonitoringIncident, now time.Time) string {
	base := i.Severity
	if store.SeverityRank(base) < store.SeverityRank(store.SeverityWarn) {
		return base
	}
	if i.Disposition == store.MonitoringDispositionBenign {
		return base
	}
	if !monitoringSustained(i) || !monitoringIncidentActive(i, now) {
		return base
	}
	return monitoringRaiseSeverity(base, monitoringAgeTier(now.Sub(i.FirstSeen)))
}

// monitoringSustained reports whether the incident has spanned more than one
// 15-minute occurrence bucket, i.e. it lasted rather than merely burst.
func monitoringSustained(i *store.MonitoringIncident) bool {
	return i.OccurrenceCount >= monitoringSustainedOccurrences
}

// monitoringIncidentActive reports whether the incident is still being
// observed. LastSeen ahead of now (clock skew) counts as active.
func monitoringIncidentActive(i *store.MonitoringIncident, now time.Time) bool {
	if i.LastSeen.IsZero() {
		return false
	}
	return now.Sub(i.LastSeen) <= monitoringIncidentActiveWindow
}

// monitoringAgeTier maps incident age onto the two escalation tiers.
func monitoringAgeTier(age time.Duration) int {
	switch {
	case age >= monitoringAgeEscalateTier2:
		return 2
	case age >= monitoringAgeEscalateTier1:
		return 1
	default:
		return 0
	}
}

func monitoringRaiseSeverity(severity string, steps int) string {
	rank := store.SeverityRank(severity)
	if rank < 0 || steps <= 0 {
		return severity
	}
	if rank += steps; rank >= len(monitoringSeverityLadder) {
		rank = len(monitoringSeverityLadder) - 1
	}
	return monitoringSeverityLadder[rank]
}

// monitoringRenotifyInterval doubles the quiet period as the incident ages,
// from the severity's base up to its cap: e.g. error gives roughly 1h, 2h, 4h,
// 8h, then every 12h.
func monitoringRenotifyInterval(severity string, age time.Duration) time.Duration {
	base, ceiling := monitoringRenotifyBounds(severity)
	interval := base
	for interval < ceiling && interval*2 <= age {
		interval *= 2
	}
	if interval > ceiling {
		interval = ceiling
	}
	return interval
}

func monitoringRenotifyBounds(severity string) (base, ceiling time.Duration) {
	switch severity {
	case store.SeverityCritical:
		return monitoringRenotifyBaseCritical, monitoringRenotifyCapCritical
	case store.SeverityError:
		return monitoringRenotifyBaseError, monitoringRenotifyCapError
	default:
		return monitoringRenotifyBaseWarn, monitoringRenotifyCapWarn
	}
}
