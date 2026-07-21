// monitoring_expected_signal_eval.go — the pure absence evaluator.
//
// EvaluateExpectedSignal is a total function of its input: no clock, no
// database, no network, no model. That is the whole point of this feature — the
// operator's constraint was to make the daemon operationally aware WITHOUT
// making the weak triage model smarter, larger, or more frequently woken. The
// AI is handed a pre-computed fact; it is never asked to notice an absence.
package store

import (
	"fmt"
	"time"
)

// ExpectedSignalOutcome enumerates every terminal state of one evaluation.
type ExpectedSignalOutcome string

const (
	// OutcomeSignalDisabled — the rule itself is switched off.
	OutcomeSignalDisabled ExpectedSignalOutcome = "disabled"
	// OutcomeSignalWarmingUp — the rule is younger than one window, so a
	// fresh install cannot fire before it has had a chance to observe.
	OutcomeSignalWarmingUp ExpectedSignalOutcome = "warming_up"
	// OutcomeSignalHealthy — the signal is present; this is also the
	// recovery edge that clears an active absence incident.
	OutcomeSignalHealthy ExpectedSignalOutcome = "healthy"
	// OutcomeSignalOutsideActiveHours — legitimately quiet right now.
	OutcomeSignalOutsideActiveHours ExpectedSignalOutcome = "outside_active_hours"
	// OutcomeSignalPartialWindow — inside active hours, but a full window
	// has not yet elapsed within the current contiguous active period.
	OutcomeSignalPartialWindow ExpectedSignalOutcome = "partial_window"
	// OutcomeSignalAwaitingFirst — the rule has never observed its signal,
	// so absence is unproven rather than anomalous.
	OutcomeSignalAwaitingFirst ExpectedSignalOutcome = "awaiting_first_signal"
	// OutcomeSignalInconclusive — pulls have failed recently but not enough
	// to raise; we may have missed material, so no absence claim is made.
	OutcomeSignalInconclusive ExpectedSignalOutcome = "inconclusive"
	// OutcomeSignalCollection — we cannot see. A DIFFERENT incident from
	// absence, with a different fix.
	OutcomeSignalCollection ExpectedSignalOutcome = "collection"
	// OutcomeSignalAbsent — the signal is genuinely missing.
	OutcomeSignalAbsent ExpectedSignalOutcome = "absent"
)

// Machine-readable sub-reasons, carried into the incident evidence.
const (
	ReasonSourceDisabled = "source_disabled"
	ReasonPullFailing    = "pull_failing"
	ReasonSourceSilent   = "source_silent"
	ReasonNoMatches      = "no_matching_lines"
	ReasonBelowMinCount  = "below_min_count"
)

// ExpectedSignalInput is everything the evaluator is allowed to know. Now and
// Location are injected, so every case below is exhaustively unit-testable
// against fixed instants.
type ExpectedSignalInput struct {
	Rule     MonitoringExpectedSignal
	Observed ExpectedSignalObservation
	Health   SourceCollectionHealth
	Now      time.Time
	Location *time.Location
}

// ExpectedSignalDecision is the evaluator's verdict.
type ExpectedSignalDecision struct {
	Outcome  ExpectedSignalOutcome `json:"outcome"`
	Raise    bool                  `json:"raise"`
	ClassKey string                `json:"class_key,omitempty"`
	Severity string                `json:"severity,omitempty"`
	Title    string                `json:"title,omitempty"`
	Reason   string                `json:"reason,omitempty"`
	Detail   string                `json:"detail,omitempty"`
	// SignalPresent drives the LastSignalAt bootstrap latch and recovery.
	SignalPresent bool      `json:"signal_present"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
}

// EvaluateExpectedSignal decides whether an expected signal's absence is an
// incident. Ordering is load-bearing:
//
//  1. disabled rule            — nothing to say
//  2. warming up               — a fresh rule cannot fire
//  3. signal present           — checked BEFORE active hours, so a signal
//     arriving at 3am still counts as recovery
//  4. outside active hours     — legitimate quiet
//  5. partial window           — not enough active time has elapsed
//  6. source disabled / pulls failing — we cannot see (RAISE, collection)
//  7. never observed           — absence unproven (bootstrap guard)
//  8. pulls degraded           — inconclusive, no absence claim
//  9. source silent            — we cannot distinguish (RAISE, collection)
//  10. otherwise               — genuine absence (RAISE)
//
// Steps 6, 8 and 9 are the collection-health discipline: this function never
// reports "no orders!" when the honest answer is "we cannot see".
func EvaluateExpectedSignal(in ExpectedSignalInput) ExpectedSignalDecision {
	rule := in.Rule
	now := in.Now.UTC()
	base := ExpectedSignalDecision{
		WindowStart: rule.WindowStart(now),
		WindowEnd:   now,
	}
	if !rule.Enabled {
		base.Outcome = OutcomeSignalDisabled
		return base
	}
	if !rule.CreatedAt.IsZero() && now.Before(rule.CreatedAt.UTC().Add(rule.Window())) {
		base.Outcome = OutcomeSignalWarmingUp
		base.Detail = fmt.Sprintf("rule created %s; one full %s window has not yet elapsed",
			rule.CreatedAt.UTC().Format(time.RFC3339), rule.Window())
		return base
	}
	if in.Observed.MatchCount >= rule.MinCount {
		base.Outcome = OutcomeSignalHealthy
		base.SignalPresent = true
		base.Detail = fmt.Sprintf("%d matching lines in the last %s (min_count %d)",
			in.Observed.MatchCount, rule.Window(), rule.MinCount)
		return base
	}
	if decision, done := evaluateSchedule(rule, in.Location, now, base); done {
		return decision
	}
	return evaluateAbsence(in, base)
}

// evaluateSchedule applies the active-hours and partial-window guards. Returns
// done=true when the schedule alone settles the evaluation.
func evaluateSchedule(
	rule MonitoringExpectedSignal, loc *time.Location, now time.Time, base ExpectedSignalDecision,
) (ExpectedSignalDecision, bool) {
	if loc == nil {
		loc = time.UTC
	}
	periodStart, active := activePeriodStart(rule, now, loc)
	if !active {
		base.Outcome = OutcomeSignalOutsideActiveHours
		base.Detail = "outside the rule's active hours; quiet here is expected"
		return base, true
	}
	// The killer false positive this prevents: a 09:00-17:00 rule with a 6h
	// window firing at 09:05 because 03:00-09:05 was quiet. Absence is only
	// claimable once a FULL window has elapsed inside the active period.
	if now.Sub(periodStart) < rule.Window() {
		base.Outcome = OutcomeSignalPartialWindow
		base.Detail = fmt.Sprintf("active period began %s; a full %s window has not yet elapsed inside it",
			periodStart.UTC().Format(time.RFC3339), rule.Window())
		return base, true
	}
	return base, false
}

// evaluateAbsence resolves the collection-health / genuine-absence fork.
func evaluateAbsence(in ExpectedSignalInput, base ExpectedSignalDecision) ExpectedSignalDecision {
	rule, obs, health := in.Rule, in.Observed, in.Health
	if !health.Enabled {
		return collectionDecision(rule, obs, health, base, ReasonSourceDisabled)
	}
	if health.ConsecutiveFailures >= rule.MaxConsecutiveFailures {
		return collectionDecision(rule, obs, health, base, ReasonPullFailing)
	}
	// Bootstrap guard: a rule that has never seen its signal has not proven it
	// can see it. Firing here would alert on every fresh install and on every
	// mistyped match_substring.
	if rule.LastSignalAt == nil && obs.LastMatchAt == nil {
		base.Outcome = OutcomeSignalAwaitingFirst
		base.Detail = "this rule has never observed its signal; absence is unproven until it does"
		return base
	}
	if health.ConsecutiveFailures > 0 {
		base.Outcome = OutcomeSignalInconclusive
		base.Detail = fmt.Sprintf(
			"%d recent collection failure(s) below the raise threshold of %d; a pull may have been missed, so no absence is claimed",
			health.ConsecutiveFailures, rule.MaxConsecutiveFailures)
		return base
	}
	if rule.RequireSourceLiveness && obs.TotalLines == 0 {
		return collectionDecision(rule, obs, health, base, ReasonSourceSilent)
	}
	return absenceDecision(rule, obs, base)
}

// collectionDecision and absenceDecision build the human-facing Title/Detail
// through the shared ExpectedSignalAlert renderer, so the title leads with the
// matched text and source rather than the learner's auto/<hash> rule name. The
// pure evaluator can only pass the rule's raw source id; the daemon re-renders
// with the source's display label before persisting (see the baseline
// evaluator). Neither the outcome, class key nor severity is touched — only the
// operator-facing text.
func collectionDecision(
	rule MonitoringExpectedSignal, obs ExpectedSignalObservation,
	health SourceCollectionHealth, base ExpectedSignalDecision, reason string,
) ExpectedSignalDecision {
	base.Outcome = OutcomeSignalCollection
	base.Raise = true
	base.ClassKey = rule.CollectionClassKey()
	base.Severity = rule.Severity
	base.Reason = reason
	alert := NewExpectedSignalAlert(rule, base, obs, health, rule.SourceID)
	base.Title, base.Detail = alert.Title(), alert.Body()
	return base
}

func absenceDecision(
	rule MonitoringExpectedSignal, obs ExpectedSignalObservation, base ExpectedSignalDecision,
) ExpectedSignalDecision {
	base.Outcome = OutcomeSignalAbsent
	base.Raise = true
	base.ClassKey = rule.AbsenceClassKey()
	base.Severity = rule.Severity
	base.Reason = ReasonNoMatches
	if obs.MatchCount > 0 {
		base.Reason = ReasonBelowMinCount
	}
	alert := NewExpectedSignalAlert(rule, base, obs, SourceCollectionHealth{}, rule.SourceID)
	base.Title, base.Detail = alert.Title(), alert.Body()
	return base
}

// maxActiveWalkbackDays bounds the contiguous-period walk. Windows are capped
// at 7 days (MaxExpectedSignalWindow), so 8 days of walk-back always covers a
// valid rule while keeping the loop terminating.
