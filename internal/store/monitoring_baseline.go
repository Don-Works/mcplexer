// monitoring_baseline.go — domain model for LEARNED expected-signal baselines.
//
// The absence evaluator (EvaluateExpectedSignal) is correct and pure, but it
// was dead: it needs a rule, and the operator's position is that nobody will
// ever author one — "no user is gonna describe those alerts, you should just
// infer them from the logs + operations of the system: what does normal look
// like". This file is the domain half of that inference.
//
// The learner mines RECURRING, PERIODIC templates out of retained history and
// proposes rules from them. Everything here is plain statistics over rows the
// daemon already stores: no model, no embedding, no prompt, no extra wake-up.
//
// The governing bias is PRECISION, not recall. A false "your orders stopped!"
// at 3am gets the whole channel muted, after which the system is worse than
// useless — so every threshold below is set where a random (Poisson) template
// is rejected with margin, and anything the evidence cannot settle is recorded
// as a REJECTION with a reason rather than promoted on a guess.
package store

import (
	"time"
)

// Promotion thresholds. Each is justified against the null hypothesis "this
// template is a random arrival process", whose inter-arrival gaps are
// exponentially distributed — the shape a non-scheduled template actually has.
const (
	// BaselineMinLearnSpan is the shortest observed history that may promote
	// anything. Raw log_lines default to 7 days of retention AND a 50MB byte
	// cap that prunes oldest-first, so a chatty source can retain far less
	// than 7 days; 3 days is the floor at which a per-minute-to-hourly cadence
	// has repeated enough to be a fact rather than a coincidence.
	BaselineMinLearnSpan = 72 * time.Hour

	// BaselineMinDeltas is the smallest inter-arrival sample that may promote.
	// It is a robustness budget, not a power calculation: at 60 samples, the
	// single enormous delta an outage contributes is 1.7% of the sample, which
	// cannot move a median or a MAD. This is what stops a broken cadence being
	// relearned as the new normal.
	BaselineMinDeltas = 60

	// BaselineMinCycles requires the span to contain at least this many whole
	// periods. Combined with the span floor it sets the honest upper bound on
	// what is learnable: with 3 days retained we can learn periods up to ~2.4h,
	// with 7 days up to ~5.6h. Rarer jobs are NOT promoted — see
	// BaselineRejectTooFewCycles.
	BaselineMinCycles = 30

	// BaselineMaxRelativeMAD caps median-absolute-deviation over median. For an
	// exponential (Poisson) process MAD/median = asinh(0.5)/ln2 = 0.694, so
	// this threshold sits at exactly half the random-arrival value.
	BaselineMaxRelativeMAD = 0.35

	// BaselineMaxP95Ratio caps p95 over median. For an exponential process
	// p95/median = ln20/ln2 = 4.32, so 3.0 rejects random arrivals with margin
	// while still admitting a genuinely jittery scheduler.
	BaselineMaxP95Ratio = 3.0

	// BaselineBurstSeparation is how many times one step in the sorted gap
	// sample must multiply before the sample is treated as bunched arrivals
	// rather than a single cadence.
	//
	// This is a BIMODALITY test and 8x is chosen to be unreachable by noise.
	// In an exponential sample of several hundred, consecutive order
	// statistics in the interior differ by well under 2% — the distribution is
	// continuous, so it has no empty band for a split to land in. A genuine
	// burst has one: the order-sync job steps straight from 1s intra-burst
	// spacing to a 298s tick, a factor of 298. Nothing observed between "noise"
	// and "obviously clustered" is admitted, which is the point.
	BaselineBurstSeparation = 8.0

	// BaselineMinHourOccupancy is the fraction of whole hour-buckets in the
	// observed span that must contain at least one arrival for a candidate to
	// be treated as continuous (24x7). A job that legitimately sleeps overnight
	// fails this and is REJECTED rather than promoted with a guessed schedule.
	BaselineMinHourOccupancy = 0.95

	// BaselineMinDayHistoryDays is how many days of long-horizon day history
	// must exist, gap-free, before a candidate may be promoted as continuous.
	//
	// The hazard this defends against is the weekday-only job: observed Monday
	// to Friday it looks perfectly continuous, and baselineDayGaps cannot see
	// the missing Saturday because the observed range ENDS on the Friday. Such
	// a candidate would be promoted on the Friday and fire at 06:00 Saturday.
	//
	// Seven is the exact width that disproves it. Seven consecutive calendar
	// days are one whole week cycle, so they contain exactly one Saturday and
	// one Sunday; a gap-free run of seven days has therefore OBSERVED both, and
	// no weekday-only job can produce one. Requiring fourteen bought nothing
	// against this hazard that seven does not already settle.
	//
	// It was not free, either. Raw log_lines retain 7 days, and migration 140
	// backfills log_template_days FROM log_lines, so a fresh install starts
	// with at most 7 days of history and a 14-day floor cannot be met for a
	// further week — during which NOTHING promotes at all. Measured against the
	// real incident the template had 3-4 days of history against a floor of 14,
	// so the rejection was `day_gaps` regardless of how clean the cadence was.
	// A gate that cannot be satisfied is not a precision control, it is an off
	// switch, and it was the second reason recall was zero.
	//
	// WHAT THIS GIVES UP, stated plainly: a pattern whose period exceeds one
	// week — a job quiet on the first of the month, or on alternate weekends —
	// is no longer excluded by this gate, because seven days cannot see it. Two
	// things still stand between that and a false alarm: DayGaps > 0 rejects
	// any interior missing day, and log_template_days is never pruned, so once
	// such a day is observed the candidate stops re-qualifying. The rule is not
	// retracted by that (nothing is ever unlearned by silence), so this is a
	// real residual risk on sub-monthly patterns and not a fully closed hole.
	BaselineMinDayHistoryDays = 7
)

// Matcher-derivation thresholds. The rule matches raw lines by case-insensitive
// substring, but the learner mines by template. A substring derived from a
// masked template is a HYPOTHESIS about what will match, and an unverified
// matcher is the single most dangerous thing here: one that matches nothing
// fires an absence alert on its first evaluation, forever.
const (
	// BaselineMinSubstringLen keeps the derived matcher specific enough to
	// mean something. Anything shorter is a fragment, not a job identity.
	BaselineMinSubstringLen = 12

	// BaselineSubstringRecall is the fraction of the template's own retained
	// lines the derived substring must actually match. Below 1.0 only to
	// tolerate whitespace collapse in masking.
	BaselineSubstringRecall = 0.99

	// BaselineSubstringPrecision caps how many EXTRA lines the substring may
	// sweep in from other templates on the same source. An over-broad matcher
	// is safe against false alarms but blind: the job could stop while a
	// sibling template kept the rule satisfied.
	BaselineSubstringPrecision = 1.25
)

// Window-sizing constants. The promoted window is how long the signal may be
// missing before absence is claimed, so it is the direct false-positive knob.
const (
	// BaselineWindowPeriodMultiple alerts after roughly this many consecutive
	// missed runs. Six is deliberately forgiving: for the 10-minute job that
	// caused the 2026-07-20 incident it still detects inside an hour against a
	// twelve-hour actual silence, while surviving any plausible jitter.
	BaselineWindowPeriodMultiple = 6

	// BaselineWindowP95Multiple sizes the window off observed tail jitter as
	// well as the median, so a scheduler with a long tail is not alarmed on.
	BaselineWindowP95Multiple = 3

	// BaselineMinPromotedWindow floors every learned window. The incident
	// class this feature exists for is "the job stopped", not "one run was
	// late", and a sub-five-minute absence window is a machine for producing
	// the second while pretending it found the first. Flooring only ever makes
	// a rule more forgiving, so it can never introduce a false positive.
	BaselineMinPromotedWindow = 5 * time.Minute

	// BaselineMaxWindowPeriodMultiple is the hard ceiling on the window in
	// units of the learned period. This is the anti-drift clamp: even if an
	// outage inflates p95, the window can never stretch beyond 12 periods and
	// quietly turn a broken job into an accepted one.
	BaselineMaxWindowPeriodMultiple = 12
)

// Adaptation bounds. A cadence that legitimately changed must update the
// baseline; a cadence that changed because it BROKE must not.
const (
	// BaselineMinAdaptDelta suppresses churn: a re-learned window within 20%
	// of the live one is not worth a write.
	BaselineMinAdaptDelta = 0.20
	// BaselineMaxWidenRatio and BaselineMaxNarrowRatio bound one adaptation
	// pass. Even a fully-qualified re-learn may only move the window by half
	// again (or two thirds down) per pass, so a sustained regime change takes
	// several hours of consistent evidence to be accepted rather than one.
	BaselineMaxWidenRatio  = 1.5
	BaselineMaxNarrowRatio = 0.67
)

// BaselineDecision is why the learner did or did not promote a candidate. Every
// code is recorded against the candidate so an operator can ask "why is there
// no rule for this job?" and get an answer instead of a shrug.
type BaselineDecision string

const (
	// BaselinePromoted — the candidate is a confident recurring signal.
	BaselinePromoted BaselineDecision = "promoted"
	// BaselineRejectShortSpan — not enough retained history to judge.
	BaselineRejectShortSpan BaselineDecision = "short_span"
	// BaselineRejectFewSamples — too few arrivals for robust statistics.
	BaselineRejectFewSamples BaselineDecision = "few_samples"
	// BaselineRejectTooFewCycles — the period is long relative to retained
	// history, so the cadence has not repeated enough to be trusted.
	BaselineRejectTooFewCycles BaselineDecision = "too_few_cycles"
	// BaselineRejectIrregular — the arrival pattern is not periodic. This is
	// the random-template rejection.
	BaselineRejectIrregular BaselineDecision = "irregular"
	// BaselineRejectDiscontinuous — the signal has real quiet stretches that
	// the evidence cannot model as a schedule.
	BaselineRejectDiscontinuous BaselineDecision = "discontinuous"
	// BaselineRejectDayGaps — long-horizon day history shows whole missing
	// days inside the observed range: a weekly/weekday pattern we refuse to
	// guess at from a 7-day line window.
	BaselineRejectDayGaps BaselineDecision = "day_gaps"
	// BaselineRejectConditionalTerminal — the template asserts that the job
	// did NO WORK ("no invoices to send"). Its arrival rate measures how often
	// there was nothing to do, not how often the job completed, so it is the
	// wrong observable and is refused outright. See ConditionalTerminalPhrase.
	BaselineRejectConditionalTerminal BaselineDecision = "conditional_terminal"
	// BaselineRejectNoMatcher — no substring specific enough to identify the
	// signal in raw lines could be derived.
	BaselineRejectNoMatcher BaselineDecision = "no_matcher"
	// BaselineRejectMatcherUnverified — the derived substring did not match
	// the template's own lines in practice. Promoting it would guarantee a
	// false absence alert.
	BaselineRejectMatcherUnverified BaselineDecision = "matcher_unverified"
	// BaselineRejectCollectionUnhealthy — the source is failing or disabled;
	// what we can see right now is not evidence of normal.
	BaselineRejectCollectionUnhealthy BaselineDecision = "collection_unhealthy"
	// BaselineFrozen — a promoted rule is currently in an absence/collection
	// incident, so the learner declines to touch its baseline at all.
	BaselineFrozen BaselineDecision = "frozen_incident_active"
)

// Promoted reports whether the decision produces or maintains a rule.
func (d BaselineDecision) Promoted() bool { return d == BaselinePromoted }

// SignalBaseline is one learned (or explicitly rejected) candidate, persisted
// so the inference is inspectable. A learned rule nobody can interrogate is a
// rule nobody trusts, so rejections are stored with the same weight as
// promotions — "why did this NOT fire" is the more common operator question.
type SignalBaseline struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	SourceID    string `json:"source_id"`
	TemplateID  string `json:"template_id"`
	// RuleID is set only while a promoted rule exists. It is also the
	// learner's ownership marker: a monitoring_expected_signals row with a
	// baseline pointing at it is learner-owned, anything else is the
	// operator's and is never rewritten.
	RuleID string `json:"rule_id,omitempty"`

	Masked         string `json:"masked"`
	MatchSubstring string `json:"match_substring,omitempty"`

	Decision BaselineDecision `json:"decision"`
	Reason   string           `json:"reason"`

	// Evidence. All of it is echoed into storage so the promotion can be
	// re-derived by hand from the same numbers.
	PeriodSeconds  float64 `json:"period_seconds"`
	P95Seconds     float64 `json:"p95_seconds"`
	MADSeconds     float64 `json:"mad_seconds"`
	RelativeMAD    float64 `json:"relative_mad"`
	P95Ratio       float64 `json:"p95_ratio"`
	SampleCount    int     `json:"sample_count"`
	CyclesObserved float64 `json:"cycles_observed"`
	HourOccupancy  float64 `json:"hour_occupancy"`
	SpanSeconds    float64 `json:"span_seconds"`
	Confidence     float64 `json:"confidence"`

	// Proposed rule shape, kept even on rejection so an operator can see what
	// WOULD have been created.
	WindowSeconds     int64 `json:"window_seconds"`
	ActiveStartMinute int   `json:"active_start_minute"`
	ActiveEndMinute   int   `json:"active_end_minute"`

	// ScanTruncated records that the per-source line budget clipped the
	// history actually examined, so the span above is a floor, not the truth.
	ScanTruncated bool `json:"scan_truncated"`

	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	ObservedAt  time.Time `json:"observed_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LearnedRuns int64     `json:"learned_runs"`
}
