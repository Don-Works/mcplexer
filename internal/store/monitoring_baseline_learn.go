// monitoring_baseline_learn.go — the pure promotion ladder.
//
// EvaluateBaselineCandidate is a total function of its input: no clock, no
// database, no network, no model. It answers one question — "is this template a
// recurring scheduled signal whose absence would be genuinely surprising?" —
// and it answers "no" whenever the evidence is merely suggestive.
//
// Ordering is load bearing. Cheap disqualifiers run first so the expensive
// judgements are only reached by candidates that could plausibly survive them,
// and collection health is checked BEFORE any statistic is trusted: what a
// failing source shows us is not evidence of what normal looks like.
package store

import (
	"fmt"
	"time"
)

// BaselineCandidate is one mined template's evidence. Every field is an
// OBSERVED fact; nothing here is inferred, and in particular nothing is derived
// from the absence of data. A hung job contributes no arrivals, so it cannot
// teach the learner anything — which is precisely why a broken cadence cannot
// be relearned as normal.
type BaselineCandidate struct {
	WorkspaceID string
	SourceID    string
	TemplateID  string
	Masked      string

	// Gaps are consecutive inter-arrival durations in arrival order.
	Gaps      []time.Duration
	FirstSeen time.Time
	LastSeen  time.Time
	LineCount int64

	// HourBucketsSeen / HourBucketsTotal measure continuity: how many whole
	// hours of the observed span contained at least one arrival.
	HourBucketsSeen  int
	HourBucketsTotal int

	// DayGaps counts whole calendar days with no observation between the
	// first and last day in the long-horizon day history. That table survives
	// raw-line pruning, so it is the only honest evidence about weekly shape.
	DayGaps int
	// DayHistoryDays is how many distinct days the long-horizon history spans.
	DayHistoryDays int

	// MatchSubstring is the candidate matcher derived from Masked, and
	// SubstringMatches / SubstringTemplateLines are its measured recall and
	// precision against real retained lines on this source.
	MatchSubstring         string
	SubstringMatches       int64
	SubstringTemplateLines int64

	// Health is the source's pull state. Included so the learner refuses to
	// form an opinion while the collector is broken.
	Health SourceCollectionHealth

	// ScanTruncated marks that the per-source line budget clipped history.
	ScanTruncated bool

	// DeployGapsExcised counts inter-arrival gaps dropped because a deploy
	// happened inside them. Recorded so the evidence stays auditable: the gap
	// sample is deliberately smaller than the arrival count implies, and an
	// operator re-deriving the numbers by hand needs to know why.
	DeployGapsExcised int
}

// Span is the observed history width.
func (c BaselineCandidate) Span() time.Duration {
	if c.FirstSeen.IsZero() || c.LastSeen.IsZero() {
		return 0
	}
	return c.LastSeen.Sub(c.FirstSeen)
}

// Occupancy is the fraction of whole hour-buckets containing an arrival.
func (c BaselineCandidate) Occupancy() float64 {
	if c.HourBucketsTotal <= 0 {
		return 0
	}
	return float64(c.HourBucketsSeen) / float64(c.HourBucketsTotal)
}

// BaselineVerdict is the learner's answer for one candidate.
type BaselineVerdict struct {
	Decision BaselineDecision
	Reason   string
	Stats    BaselineStats
	// Cycles is span / period: how many times the cadence repeated.
	Cycles     float64
	Occupancy  float64
	Confidence float64
	// Window is the proposed absence window; zero unless promoted.
	Window time.Duration
}

// EvaluateBaselineCandidate applies the promotion ladder.
//
//  1. collection unhealthy    — refuse to learn from a broken source
//  2. conditional terminal    — the line says no work was done; wrong observable
//  3. span too short          — not enough retained history to judge
//  4. too few arrivals        — robust statistics need a real sample
//  5. irregular               — random arrivals, not a schedule
//  6. too few cycles          — period too long for the history we hold
//  7. discontinuous           — real quiet stretches we will not guess at
//  8. day gaps                — weekly shape we cannot honestly model
//  9. no / unverified matcher — cannot identify the signal in raw lines
//  10. otherwise               — promote
//
// Steps 6 and 7 are where recall is deliberately sacrificed. A business-hours
// or weekday-only job is REJECTED with a reason rather than promoted with a
// guessed schedule, because a guessed schedule is how you get the 3am false
// positive that gets the entire channel muted.
func EvaluateBaselineCandidate(c BaselineCandidate) BaselineVerdict {
	stats := SummarizeGaps(c.Gaps)
	span := c.Span()
	occupancy := c.Occupancy()
	v := BaselineVerdict{Stats: stats, Occupancy: occupancy}
	if stats.Median > 0 {
		v.Cycles = span.Seconds() / stats.Median
	}
	v.Confidence = stats.Confidence(v.Cycles, occupancy)
	if decision, reason, done := baselineDisqualify(c, stats, span); done {
		v.Decision, v.Reason = decision, reason
		return v
	}
	if v.Cycles < BaselineMinCycles {
		v.Decision = BaselineRejectTooFewCycles
		v.Reason = fmt.Sprintf(
			"cadence repeated %.1f times in %s of retained history; %d repeats are required. "+
				"A period of %s needs about %s of history, which retention does not hold.",
			v.Cycles, baselineDur(span), BaselineMinCycles,
			baselineDur(time.Duration(stats.Median)*time.Second),
			baselineDur(time.Duration(stats.Median*BaselineMinCycles)*time.Second))
		return v
	}
	if decision, reason, done := baselineShapeCheck(c, occupancy); done {
		v.Decision, v.Reason = decision, reason
		return v
	}
	if decision, reason, done := baselineMatcherCheck(c); done {
		v.Decision, v.Reason = decision, reason
		return v
	}
	v.Decision = BaselinePromoted
	v.Window = stats.WindowFor()
	v.Reason = fmt.Sprintf(
		"recurring every %s (+/- %s) across %d %s spanning %s; "+
			"regularity %.3f and tail ratio %.2f are both well inside the random-arrival values "+
			"(0.694 / 4.32), so an absence lasting %s would be genuinely surprising.",
		baselineDur(time.Duration(stats.Median)*time.Second),
		baselineDur(time.Duration(stats.MAD)*time.Second),
		stats.Count, baselineSampleNoun(stats), baselineDur(span),
		stats.RelativeMAD, stats.P95Ratio, baselineDur(v.Window))
	return v
}

// baselineSampleNoun names what was actually counted, so an operator reading a
// promotion is never left thinking a 5-minute tick fires once when it fires in
// threes. Silently reporting "observations" for a burst-clustered sample would
// make the evidence unreconcilable with the raw line counts they can see.
func baselineSampleNoun(stats BaselineStats) string {
	if !stats.Bursty {
		return "observations"
	}
	return fmt.Sprintf("ticks (arrivals bunch ~%.0f per tick, %d in total)",
		stats.BurstSize, stats.RawCount)
}

// baselineDisqualify runs the cheap, unconditional rejections.
func baselineDisqualify(
	c BaselineCandidate, stats BaselineStats, span time.Duration,
) (BaselineDecision, string, bool) {
	if !c.Health.Enabled {
		return BaselineRejectCollectionUnhealthy,
			"the log source is disabled; what it shows now is not evidence of normal", true
	}
	if c.Health.ConsecutiveFailures > 0 {
		return BaselineRejectCollectionUnhealthy, fmt.Sprintf(
			"log collection has failed %d consecutive time(s); history may have holes that are "+
				"collection artefacts, not cadence", c.Health.ConsecutiveFailures), true
	}
	// Checked before any statistic, because it invalidates all of them. A job
	// that reliably has nothing to do produces a textbook cadence — the numbers
	// cannot tell you the observable is the wrong one, only the text can.
	if phrase := ConditionalTerminalPhrase(c.Masked); phrase != "" {
		return BaselineRejectConditionalTerminal, fmt.Sprintf(
			"this line says no work was done (%q), so it is emitted on a conditional early-return "+
				"branch. Its rate measures how often there is nothing to do, not how often the job "+
				"finished: alerting on its absence would stay green while the job idles and fire the "+
				"first time there is real work. Refusing rather than mis-modelling it. "+
				"If this line really is the unconditional completion, author the rule by hand.",
			phrase), true
	}
	if span < BaselineMinLearnSpan {
		return BaselineRejectShortSpan, fmt.Sprintf(
			"only %s of retained history for this template; %s is required before any cadence "+
				"claim is trustworthy", baselineDur(span), baselineDur(BaselineMinLearnSpan)), true
	}
	if stats.Count < BaselineMinDeltas {
		return BaselineRejectFewSamples, fmt.Sprintf(
			"%d inter-arrival observations; %d are required so that a single outage-length gap "+
				"cannot move the median", stats.Count, BaselineMinDeltas), true
	}
	if !stats.Regular() {
		return BaselineRejectIrregular, fmt.Sprintf(
			"arrivals are not periodic: regularity %.3f (max %.2f) and tail ratio %.2f (max %.2f). "+
				"Random arrivals score 0.694 and 4.32; this template is indistinguishable from noise.",
			stats.RelativeMAD, BaselineMaxRelativeMAD, stats.P95Ratio, BaselineMaxP95Ratio), true
	}
	return "", "", false
}

// baselineShapeCheck rejects candidates whose schedule we cannot model.
func baselineShapeCheck(c BaselineCandidate, occupancy float64) (BaselineDecision, string, bool) {
	if occupancy < BaselineMinHourOccupancy {
		return BaselineRejectDiscontinuous, fmt.Sprintf(
			"arrivals cover only %.0f%% of the hours in the observed span (%d of %d), so this is "+
				"not a continuous job. Inferring its active hours needs more history than raw-line "+
				"retention holds, and guessing them is how a rule fires at 3am and gets muted.",
			occupancy*100, c.HourBucketsSeen, c.HourBucketsTotal), true
	}
	if c.DayHistoryDays < BaselineMinDayHistoryDays {
		return BaselineRejectDayGaps, fmt.Sprintf(
			"only %d day(s) of long-horizon day history; %d are required. A weekday-only job "+
				"observed Monday to Friday looks perfectly continuous, because the observed range "+
				"ends on the Friday and the missing Saturday falls outside it. %d consecutive days "+
				"are one whole week, so a gap-free run of them must include a Saturday and a Sunday "+
				"and cannot have come from a weekday-only job.",
			c.DayHistoryDays, BaselineMinDayHistoryDays, BaselineMinDayHistoryDays), true
	}
	if c.DayGaps > 0 {
		return BaselineRejectDayGaps, fmt.Sprintf(
			"long-horizon day history shows %d day(s) with no observation inside a %d-day range. "+
				"That is a weekly or weekday pattern, and it is modelled as a rejection rather "+
				"than an inferred weekday mask: a mask guessed from this little evidence is how a "+
				"rule fires at 3am and gets the channel muted.", c.DayGaps, c.DayHistoryDays), true
	}
	return "", "", false
}

// baselineMatcherCheck verifies the derived substring against reality.
//
// This is the guard against the learner's most dangerous possible mistake. The
// learner mines by template but the rule matches raw lines by substring, so the
// derived matcher is a hypothesis. A matcher that matches nothing would raise
// an absence incident on its very first evaluation and every one after it.
func baselineMatcherCheck(c BaselineCandidate) (BaselineDecision, string, bool) {
	if len([]rune(c.MatchSubstring)) < BaselineMinSubstringLen {
		return BaselineRejectNoMatcher, fmt.Sprintf(
			"no literal run of at least %d characters survives masking of %q, so no substring "+
				"can identify this signal in raw lines",
			BaselineMinSubstringLen, baselineExcerpt(c.Masked)), true
	}
	if c.SubstringTemplateLines <= 0 {
		return BaselineRejectMatcherUnverified,
			"the template has no retained lines to verify the derived matcher against", true
	}
	recall := float64(c.SubstringMatches) / float64(c.SubstringTemplateLines)
	if recall < BaselineSubstringRecall {
		return BaselineRejectMatcherUnverified, fmt.Sprintf(
			"the derived matcher %q matches only %d of the template's %d retained lines (%.1f%%). "+
				"Promoting it would raise a false absence immediately.",
			c.MatchSubstring, c.SubstringMatches, c.SubstringTemplateLines, recall*100), true
	}
	if recall > BaselineSubstringPrecision {
		return BaselineRejectMatcherUnverified, fmt.Sprintf(
			"the derived matcher %q is over-broad: it matches %d lines against the template's %d "+
				"(%.2fx). Sibling templates would keep the rule satisfied after this job stopped.",
			c.MatchSubstring, c.SubstringMatches, c.SubstringTemplateLines, recall), true
	}
	return "", "", false
}

// baselineDur renders a duration at operator resolution.
func baselineDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	case d < 48*time.Hour:
		return d.Round(time.Minute).String()
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

func baselineExcerpt(s string) string {
	const limit = 80
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}
