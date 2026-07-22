package store

import (
	"math"
	"testing"
	"time"
)

// TestBaselineThresholdsRejectTheExponentialNull pins the justification the
// thresholds were chosen from. A non-scheduled template's inter-arrival gaps
// are exponentially distributed, for which MAD/median = asinh(0.5)/ln2 = 0.694
// and p95/median = ln20/ln2 = 4.32. If either threshold ever drifts above the
// value it is supposed to reject, random templates start getting promoted and
// the whole feature becomes a false-alarm generator.
func TestBaselineThresholdsRejectTheExponentialNull(t *testing.T) {
	const exponentialRelativeMAD = 0.6931 // asinh(0.5)/ln2
	const exponentialP95Ratio = 4.3219    // ln20/ln2

	if BaselineMaxRelativeMAD >= exponentialRelativeMAD {
		t.Errorf("BaselineMaxRelativeMAD = %.3f admits random arrivals (%.3f)",
			BaselineMaxRelativeMAD, exponentialRelativeMAD)
	}
	if BaselineMaxP95Ratio >= exponentialP95Ratio {
		t.Errorf("BaselineMaxP95Ratio = %.2f admits random arrivals (%.2f)",
			BaselineMaxP95Ratio, exponentialP95Ratio)
	}
	// And the empirical check: a large exponential sample must fail Regular().
	stats := SummarizeGaps(randomGaps(10*time.Minute, 5000, 20260720))
	if stats.Regular() {
		t.Errorf("a 5000-sample exponential process passed Regular() "+
			"(relative_mad=%.3f p95_ratio=%.2f)", stats.RelativeMAD, stats.P95Ratio)
	}
	if math.Abs(stats.RelativeMAD-exponentialRelativeMAD) > 0.1 {
		t.Errorf("fixture drift: sampled relative MAD %.3f is far from the theoretical %.3f",
			stats.RelativeMAD, exponentialRelativeMAD)
	}
}

func TestSummarizeGapsRobustToOutliers(t *testing.T) {
	base := periodicGaps(10*time.Minute, 20*time.Second, 400)
	clean := SummarizeGaps(base)

	// Five separate multi-hour outages in a 400-sample history — far more than
	// any real incident — must still leave the period intact.
	dirty := append([]time.Duration{}, base...)
	for i := 0; i < 5; i++ {
		dirty = append(dirty, 12*time.Hour)
	}
	got := SummarizeGaps(dirty)
	if math.Abs(got.Median-clean.Median) > 1 {
		t.Errorf("median moved %.1fs after five 12h outages; want immovable",
			math.Abs(got.Median-clean.Median))
	}
}

func TestWindowForClampsAndFloors(t *testing.T) {
	tests := []struct {
		name  string
		stats BaselineStats
		want  time.Duration
	}{
		{
			name:  "six periods for a tight cadence",
			stats: BaselineStats{Median: 600, P95: 620},
			want:  60 * time.Minute,
		},
		{
			name:  "tail-driven window when jitter is wide",
			stats: BaselineStats{Median: 600, P95: 1500},
			want:  75 * time.Minute,
		},
		{
			name: "outage-inflated tail is clamped at the period ceiling",
			// A p95 dragged to an hour by degradation would ask for 3h; the
			// ceiling refuses, so a degrading job cannot disarm its own alarm.
			stats: BaselineStats{Median: 600, P95: 3600},
			want:  120 * time.Minute,
		},
		{
			name:  "fast job is floored so lateness is not mistaken for stopping",
			stats: BaselineStats{Median: 10, P95: 12},
			want:  BaselineMinPromotedWindow,
		},
		{
			name:  "no evidence yields no window",
			stats: BaselineStats{},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stats.WindowFor(); got != tt.want {
				t.Errorf("WindowFor() = %s; want %s", got, tt.want)
			}
		})
	}
}

func liveRule(windowSeconds int64) *MonitoringExpectedSignal {
	return &MonitoringExpectedSignal{
		ID: "rule-1", WorkspaceID: "ws-1", SourceID: "src-1",
		Name: "auto/tpl-orders-s", WindowSeconds: windowSeconds, Enabled: true,
	}
}

func TestReconcileBaseline(t *testing.T) {
	promoted := func(window time.Duration) BaselineVerdict {
		return BaselineVerdict{
			Decision: BaselinePromoted, Window: window,
			Stats: BaselineStats{Count: 400, Median: window.Seconds() / 6},
		}
	}
	tests := []struct {
		name       string
		existing   *MonitoringExpectedSignal
		verdict    BaselineVerdict
		wantAction BaselineAction
		wantWindow int64
		wantFrozen bool
	}{
		{
			name:       "first promotion creates a rule",
			existing:   nil,
			verdict:    promoted(time.Hour),
			wantAction: BaselineActionCreate,
			wantWindow: 3600,
		},
		{
			name:       "rejection with no rule does nothing",
			existing:   nil,
			verdict:    BaselineVerdict{Decision: BaselineRejectIrregular},
			wantAction: BaselineActionSkip,
		},
		{
			name:     "monitor synthetic rejection disables a learned rule",
			existing: liveRule(3600),
			verdict: BaselineVerdict{
				Decision: BaselineRejectMonitoringSynthetic,
				Reason:   "monitor output cannot be an application heartbeat",
			},
			wantAction: BaselineActionDisable,
			wantWindow: 3600,
		},
		{
			name:       "small drift is not worth a write",
			existing:   liveRule(3600),
			verdict:    promoted(63 * time.Minute),
			wantAction: BaselineActionKeep,
			wantWindow: 3600,
		},
		{
			name:       "a real slowdown is accepted, rate limited",
			existing:   liveRule(3600),
			verdict:    promoted(4 * time.Hour),
			wantAction: BaselineActionUpdate,
			wantWindow: 5400, // clamped to 1.5x, not the 14400 proposed
		},
		{
			name:       "a real speed-up is accepted, rate limited",
			existing:   liveRule(3600),
			verdict:    promoted(10 * time.Minute),
			wantAction: BaselineActionUpdate,
			wantWindow: 2412, // clamped to 0.67x
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileBaseline(tt.existing, tt.verdict)
			if got.Action != tt.wantAction {
				t.Fatalf("action = %q; want %q (%s)", got.Action, tt.wantAction, got.Reason)
			}
			if got.WindowSeconds != tt.wantWindow {
				t.Errorf("window = %ds; want %ds", got.WindowSeconds, tt.wantWindow)
			}
			if got.Frozen != tt.wantFrozen {
				t.Errorf("frozen = %v; want %v", got.Frozen, tt.wantFrozen)
			}
		})
	}
}

// TestReconcileBaselineFreezesDuringIncident is layer 2 of the drift defence.
// The moment a rule is reporting that its signal stopped is the moment the
// evidence most wants to argue the new normal is nothing — so that is exactly
// when the learner stops listening.
func TestReconcileBaselineFreezesDuringIncident(t *testing.T) {
	rule := liveRule(3600)
	rule.ActiveIncidentID = "incident-1"

	// Even a fully-qualified promotion asking for a much wider window.
	got := ReconcileBaseline(rule, BaselineVerdict{
		Decision: BaselinePromoted, Window: 12 * time.Hour,
		Stats: BaselineStats{Count: 500, Median: 7200},
	})
	if got.Action != BaselineActionKeep || !got.Frozen {
		t.Fatalf("action = %q frozen = %v; want keep + frozen", got.Action, got.Frozen)
	}
	if got.WindowSeconds != 3600 {
		t.Errorf("window = %ds; the live window must survive untouched", got.WindowSeconds)
	}
}

func TestReconcileBaselineDisablesSyntheticRuleDuringIncident(t *testing.T) {
	rule := liveRule(3600)
	rule.ActiveIncidentID = "incident-1"

	got := ReconcileBaseline(rule, BaselineVerdict{
		Decision: BaselineRejectMonitoringSynthetic,
		Reason:   "monitor output cannot be an application heartbeat",
	})
	if got.Action != BaselineActionDisable || got.Frozen {
		t.Fatalf("action = %q frozen = %v; want disable without freeze", got.Action, got.Frozen)
	}
}

// TestReconcileBaselineNeverRelaxesOnMissingEvidence is layer 1's consequence.
// A job that went silent stops qualifying — and that must leave the rule armed,
// never widen or remove it. Getting this wrong is how a system learns "zero
// orders is normal" after one bad night.
func TestReconcileBaselineNeverRelaxesOnMissingEvidence(t *testing.T) {
	for _, decision := range []BaselineDecision{
		BaselineRejectFewSamples, BaselineRejectShortSpan,
		BaselineRejectIrregular, BaselineRejectCollectionUnhealthy,
	} {
		got := ReconcileBaseline(liveRule(3600), BaselineVerdict{Decision: decision})
		if got.Action != BaselineActionKeep {
			t.Errorf("%s: action = %q; want keep", decision, got.Action)
		}
		if got.WindowSeconds != 3600 {
			t.Errorf("%s: window = %ds; want the live 3600s left alone",
				decision, got.WindowSeconds)
		}
	}
}

func TestProposeExpectedSignalIsValid(t *testing.T) {
	c := promotableCandidate()
	v := EvaluateBaselineCandidate(c)
	rule := ProposeExpectedSignal(c, v)
	if err := ValidateMonitoringExpectedSignal(rule); err != nil {
		t.Fatalf("proposed rule is invalid: %v", err)
	}
	if rule.MinCount != 1 {
		t.Errorf("min_count = %d; want 1 — anything higher can be tripped by jitter",
			rule.MinCount)
	}
	if !rule.RequireSourceLiveness {
		t.Error("require_source_liveness must be on so total silence reports COLLECTION")
	}
	if rule.Name != "auto/tpl-orders-s" {
		t.Errorf("name = %q; want a stable auto/ name derived from the template", rule.Name)
	}
	if rule.MatchSubstring != c.MatchSubstring {
		t.Errorf("match_substring = %q; want the verified matcher %q",
			rule.MatchSubstring, c.MatchSubstring)
	}
}
