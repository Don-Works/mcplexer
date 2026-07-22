// monitoring_baseline_adapt.go — proposing a rule from a verdict, and the
// adaptation policy that decides when a live rule may be rewritten.
//
// This file is the crux of the design. "A cadence that legitimately changed
// should update the baseline; a cadence that changed BECAUSE IT BROKE must not
// be quietly learned as the new normal" is a genuine tension, and it is
// resolved in three layers rather than one:
//
//  1. STRUCTURAL (monitoring_baseline_stats.go). The learner only ever reads
//     OBSERVED ARRIVALS. A hung job emits nothing, so it contributes no gaps —
//     an outage adds at most ONE bridging gap to a sample of at least 60, and
//     the median and MAD are immune to it. A job hung for the whole learning
//     horizon yields zero gaps and falls below BaselineMinDeltas, which is a
//     REJECTION, and rejection here means "leave the live rule exactly as it
//     is". The learner cannot be taught by silence, only by activity.
//
//  2. STATEFUL (ReconcileBaseline, below). While a rule has an absence or
//     collection incident open, the learner will not touch it at all. The one
//     moment the evidence is most likely to be arguing "this job's new normal
//     is nothing" is the exact moment we stop listening.
//
//  3. RATE-LIMITED (BaselineMaxWidenRatio). Even a fully-qualified re-learn
//     may only move the window half again per pass, so a real regime change is
//     accepted over hours of consistent evidence rather than in one step.
//
// Layer 1 is the one that actually does the work; 2 and 3 exist because a
// design whose safety rests on a single mechanism has no margin.
package store

import (
	"fmt"
	"strings"
	"time"
)

// BaselineAction is what the learner should do with a live rule.
type BaselineAction string

const (
	// BaselineActionCreate — no rule exists; create one.
	BaselineActionCreate BaselineAction = "create"
	// BaselineActionUpdate — the live rule's window should move.
	BaselineActionUpdate BaselineAction = "update"
	// BaselineActionKeep — the live rule is still right, or the evidence is
	// not good enough to justify changing it. Also the answer whenever a
	// promoted rule stops qualifying: we never unlearn a rule by silence.
	BaselineActionKeep BaselineAction = "keep"
	// BaselineActionSkip — nothing to do; no rule and no promotion.
	BaselineActionSkip BaselineAction = "skip"
	// BaselineActionDisable — a learner-owned rule is structurally unsafe and
	// must stop evaluating. This is intentionally narrower than an ordinary
	// rejection: only monitoring_synthetic uses it, so silence or weak evidence
	// can never disarm a legitimate application rule.
	BaselineActionDisable BaselineAction = "disable"
)

// BaselineReconciliation is the adaptation decision for one candidate.
type BaselineReconciliation struct {
	Action        BaselineAction
	Reason        string
	WindowSeconds int64
	// Frozen marks that adaptation was suppressed by an open incident.
	Frozen bool
}

// ReconcileBaseline decides how a verdict should affect the live rule.
//
// existing is nil when no learner-owned rule exists yet. It is NEVER an
// operator-authored rule: ownership is established by the baseline row pointing
// at the rule id, so a hand-written rule is invisible to this function and is
// never rewritten.
func ReconcileBaseline(
	existing *MonitoringExpectedSignal, v BaselineVerdict,
) BaselineReconciliation {
	if existing == nil {
		if !v.Decision.Promoted() {
			return BaselineReconciliation{Action: BaselineActionSkip, Reason: v.Reason}
		}
		return BaselineReconciliation{
			Action:        BaselineActionCreate,
			WindowSeconds: int64(v.Window / time.Second),
			Reason:        v.Reason,
		}
	}
	// A logwatch-generated diagnostic can never be a valid application
	// heartbeat. Retire a previously promoted learner-owned rule even if its
	// false incident is currently open; the ordinary incident freeze protects
	// legitimate signals from relearning and must not preserve a known-invalid
	// synthetic rule.
	if v.Decision == BaselineRejectMonitoringSynthetic {
		if !existing.Enabled {
			return BaselineReconciliation{
				Action: BaselineActionKeep, WindowSeconds: existing.WindowSeconds,
				Reason: "synthetic monitoring rule is already disabled",
			}
		}
		return BaselineReconciliation{
			Action: BaselineActionDisable, WindowSeconds: existing.WindowSeconds,
			Reason: v.Reason,
		}
	}
	// Layer 2: an open incident freezes the baseline. If this rule is
	// currently reporting that its signal has stopped, the last thing we do is
	// take fresh advice from the same window about what normal looks like.
	if existing.ActiveIncidentID != "" {
		return BaselineReconciliation{
			Action: BaselineActionKeep, Frozen: true,
			WindowSeconds: existing.WindowSeconds,
			Reason: "an incident is open on this rule; the baseline is frozen so a broken " +
				"cadence cannot be accepted as the new normal while it is broken",
		}
	}
	if !v.Decision.Promoted() {
		// Layer 1's consequence: failing to re-qualify never relaxes or
		// removes a rule. A job that went silent produces exactly this, and
		// the correct response is to leave the rule armed.
		return BaselineReconciliation{
			Action: BaselineActionKeep, WindowSeconds: existing.WindowSeconds,
			Reason: "candidate no longer qualifies (" + string(v.Decision) +
				"); the live rule is left unchanged — a rule is never relaxed by absence of evidence",
		}
	}
	return reconcileWindow(existing, v)
}

// reconcileWindow applies the churn floor and the per-pass rate limit.
func reconcileWindow(existing *MonitoringExpectedSignal, v BaselineVerdict) BaselineReconciliation {
	current := existing.WindowSeconds
	proposed := int64(v.Window / time.Second)
	if current <= 0 || proposed <= 0 {
		return BaselineReconciliation{
			Action: BaselineActionKeep, WindowSeconds: current,
			Reason: "no usable window on either side; leaving the live rule unchanged",
		}
	}
	ratio := float64(proposed) / float64(current)
	if ratio > 1-BaselineMinAdaptDelta && ratio < 1+BaselineMinAdaptDelta {
		return BaselineReconciliation{
			Action: BaselineActionKeep, WindowSeconds: current,
			Reason: fmt.Sprintf("re-learned window is within %.0f%% of the live one (%.2fx); not worth a write",
				BaselineMinAdaptDelta*100, ratio),
		}
	}
	clamped, limited := clampAdaptation(current, proposed)
	reason := fmt.Sprintf("cadence re-learned from %d fresh observations: window %ds -> %ds (%.2fx)",
		v.Stats.Count, current, clamped, float64(clamped)/float64(current))
	if limited {
		reason += fmt.Sprintf("; rate-limited from the proposed %ds so a regime change is accepted "+
			"over several passes of consistent evidence rather than one", proposed)
	}
	if clamped == current {
		return BaselineReconciliation{Action: BaselineActionKeep, WindowSeconds: current, Reason: reason}
	}
	return BaselineReconciliation{Action: BaselineActionUpdate, WindowSeconds: clamped, Reason: reason}
}

// clampAdaptation bounds one adaptation pass in both directions. Widening is
// bounded because it is the direction that disarms the rule; narrowing is
// bounded because it is the direction that makes it trigger-happy.
func clampAdaptation(current, proposed int64) (int64, bool) {
	maxUp := int64(float64(current) * BaselineMaxWidenRatio)
	minDown := int64(float64(current) * BaselineMaxNarrowRatio)
	switch {
	case proposed > maxUp:
		return maxUp, true
	case proposed < minDown:
		return minDown, true
	default:
		return proposed, false
	}
}

// baselineRuleNamePrefix marks learner-owned rules in every operator-facing
// listing. Ownership is established by the baseline row, not by this string —
// the prefix is for humans reading a rule list, not for code.
const baselineRuleNamePrefix = "auto/"

// BaselineRuleName is the deterministic, stable name for a learned rule. It is
// derived from the template id so re-running the learner converges on the same
// row through UNIQUE(workspace_id, source_id, name) rather than accumulating
// near-duplicates.
func BaselineRuleName(templateID string) string {
	id := strings.TrimSpace(templateID)
	if len(id) > 12 {
		id = id[:12]
	}
	return baselineRuleNamePrefix + id
}

// ProposeExpectedSignal builds the rule a promoted verdict implies.
//
// The knobs are set at the safe end deliberately:
//   - MinCount is 1. "At least one completion in the window" cannot be tripped
//     by jitter, whereas any higher count can.
//   - RequireSourceLiveness is on, so total source silence reports COLLECTION
//     rather than claiming the job stopped.
//   - The schedule is always-on, because a candidate that was not continuous
//     never reached promotion in the first place.
func ProposeExpectedSignal(c BaselineCandidate, v BaselineVerdict) *MonitoringExpectedSignal {
	rule := &MonitoringExpectedSignal{
		WorkspaceID:            c.WorkspaceID,
		SourceID:               c.SourceID,
		Name:                   BaselineRuleName(c.TemplateID),
		MatchSubstring:         c.MatchSubstring,
		MinCount:               1,
		WindowSeconds:          int64(v.Window / time.Second),
		Severity:               SeverityError,
		Timezone:               "UTC",
		ActiveDaysMask:         allWeekdaysMask,
		ActiveStartMinute:      0,
		ActiveEndMinute:        expectedSignalDayMinutes,
		RequireSourceLiveness:  true,
		MaxConsecutiveFailures: 3,
		Enabled:                true,
	}
	ApplyExpectedSignalDefaults(rule)
	return rule
}

// DeriveMatchSubstring extracts the longest literal run from a masked template.
//
// Masking replaces volatile atoms with <ts>, <uuid>, <n> and friends, so the
// text BETWEEN those placeholders is the invariant part of the line and appears
// verbatim in every raw line of the template. The longest such run is the most
// specific matcher available without a regex — and the caller must still verify
// it against real lines (baselineMatcherCheck) before anything is promoted,
// because masking also collapses runs of whitespace.
func DeriveMatchSubstring(masked string) string {
	best := ""
	for _, part := range splitMaskPlaceholders(masked) {
		candidate := strings.TrimSpace(part)
		// A run containing a double space cannot be matched literally: masking
		// collapsed whitespace, so the raw line may differ inside the run.
		if strings.Contains(candidate, "  ") {
			continue
		}
		if len([]rune(candidate)) > len([]rune(best)) {
			best = candidate
		}
	}
	if len([]rune(best)) > 200 {
		best = string([]rune(best)[:200])
		best = strings.TrimSpace(best)
	}
	return best
}

// splitMaskPlaceholders cuts a masked template at every <placeholder>.
func splitMaskPlaceholders(masked string) []string {
	parts := []string{}
	rest := masked
	for {
		open := strings.IndexByte(rest, '<')
		if open < 0 {
			parts = append(parts, rest)
			return parts
		}
		close := strings.IndexByte(rest[open:], '>')
		if close < 0 {
			parts = append(parts, rest)
			return parts
		}
		parts = append(parts, rest[:open])
		rest = rest[open+close+1:]
	}
}
