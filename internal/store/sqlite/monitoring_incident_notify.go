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

	// monitoringAgeEscalateTier1/2 schedule one reminder at meaningful age
	// boundaries. Age changes cadence, never severity: a long-lived warning is
	// still a warning, not a synthetic critical page. 4h is about half a
	// working shift; 12h covers the overnight blind window.
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
	// EffectiveSeverity is the deterministic severity to dispatch and record.
	// Persistence and age reminders retain the classifier severity.
	EffectiveSeverity string
}

// monitoringNotificationDue is pure — now is passed in rather than read from
// the clock, so the policy is deterministic and identical wherever it runs. It
// is the persistence policy (monitoringNotificationDueRaw) gated by the
// operator's ack/silence pause: a pause mutes routine "still broken" and age
// reminders, but never a genuine classifier escalation.
func monitoringNotificationDue(
	i *store.MonitoringIncident, newIncident bool, now time.Time,
) monitoringNotificationDecision {
	d := monitoringNotificationDueRaw(i, newIncident, now)
	if d.Notify && monitoringActionSuppressed(i, now, d.EffectiveSeverity) {
		// Only a classifier escalation raises severity above the pause floor.
		// Age reminders remain at the acknowledged severity and stay muted.
		return monitoringNotificationDecision{EffectiveSeverity: d.EffectiveSeverity}
	}
	return d
}

// monitoringActionSuppressed reports whether an operator acknowledge or silence
// is muting re-notification for this incident at now. A pause is EFFECTIVE only
// while the effective severity has not risen above the floor recorded when the
// action was taken (acked_severity / silenced_severity); an escalation past that
// floor pierces both. Silence is additionally bounded by silenced_until and
// lapses on its own at expiry, after which a still-active incident re-notifies.
//
// Once a classifier escalation pierces a pause it stays pierced until the
// operator acts again.
func monitoringActionSuppressed(i *store.MonitoringIncident, now time.Time, effective string) bool {
	if i == nil {
		return false
	}
	if monitoringSilenceActive(i, now) && !severityHigher(effective, i.SilencedSeverity) {
		return true
	}
	if i.AckedAt != nil && !severityHigher(effective, i.AckedSeverity) {
		return true
	}
	return false
}

// monitoringSilenceActive reports whether a bounded silence is still within its
// window at now. A nil or already-expired silenced_until is not active.
func monitoringSilenceActive(i *store.MonitoringIncident, now time.Time) bool {
	return i != nil && i.SilencedUntil != nil && now.Before(*i.SilencedUntil)
}

// monitoringNotificationDueRaw is the persistence policy with no operator pause
// applied — the pre-action behaviour, kept whole so the pause is a single gate
// in the wrapper rather than a condition threaded through every branch.
func monitoringNotificationDueRaw(
	i *store.MonitoringIncident, newIncident bool, now time.Time,
) monitoringNotificationDecision {
	if i == nil {
		return monitoringNotificationDecision{}
	}
	silent := monitoringNotificationDecision{EffectiveSeverity: monitoringEffectiveSeverity(i, now)}
	// The warn floor is applied to the classifier severity: info-level noise
	// must never become notifiable by ageing.
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
	// The age reminder fires exactly once per tier boundary.
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

// monitoringEffectiveSeverity deliberately preserves classifier severity.
// Incident age affects reminder timing, never page class.
func monitoringEffectiveSeverity(i *store.MonitoringIncident, now time.Time) string {
	_ = now
	return i.Severity
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

// monitoringAgeTier maps incident age onto the two reminder tiers.
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
