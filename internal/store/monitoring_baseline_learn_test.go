package store

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateBaselineCandidatePromotesCleanPeriodicSignal(t *testing.T) {
	v := EvaluateBaselineCandidate(promotableCandidate())
	if v.Decision != BaselinePromoted {
		t.Fatalf("decision = %q (%s); want promoted", v.Decision, v.Reason)
	}
	// Period must be recovered as the median, not perturbed by the jitter.
	if got := time.Duration(v.Stats.Median) * time.Second; got != 10*time.Minute {
		t.Errorf("learned period = %s; want 10m", got)
	}
	// The window must be forgiving enough to survive several missed runs.
	if v.Window < 6*10*time.Minute {
		t.Errorf("window = %s; want at least 6 periods (1h)", v.Window)
	}
	if v.Window > BaselineMaxWindowPeriodMultiple*10*time.Minute {
		t.Errorf("window = %s; exceeds the %dx period ceiling",
			v.Window, BaselineMaxWindowPeriodMultiple)
	}
	if v.Confidence <= 0.5 {
		t.Errorf("confidence = %.3f; a textbook cadence should score high", v.Confidence)
	}
}

// TestEvaluateBaselineCandidateRejectsRandomTemplate is the precision test that
// matters most: a template that merely appears often must never be promoted,
// because alerting on its absence would be alerting on noise.
func TestEvaluateBaselineCandidateRejectsRandomTemplate(t *testing.T) {
	for _, seed := range []int64{1, 7, 42, 1234, 99999} {
		c := promotableCandidate()
		c.Gaps = randomGaps(10*time.Minute, 432, seed)
		v := EvaluateBaselineCandidate(c)
		if v.Decision != BaselineRejectIrregular {
			t.Errorf("seed %d: decision = %q; want irregular (relative_mad=%.3f p95_ratio=%.2f)",
				seed, v.Decision, v.Stats.RelativeMAD, v.Stats.P95Ratio)
		}
	}
}

func TestEvaluateBaselineCandidateLadder(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*BaselineCandidate)
		want      BaselineDecision
		reasonHas string
	}{
		{
			name: "monitor-generated observations never become heartbeats",
			mutate: func(c *BaselineCandidate) {
				c.Masked = "logwatch: source discontinuity — container/service restarted"
				c.MatchSubstring = "logwatch: source discontinuity"
			},
			want:      BaselineRejectMonitoringSynthetic,
			reasonHas: "monitoring observation",
		},
		{
			name: "insufficient history does not promote",
			mutate: func(c *BaselineCandidate) {
				c.FirstSeen = baselineTestNow.Add(-40 * time.Hour)
			},
			want:      BaselineRejectShortSpan,
			reasonHas: "retained history",
		},
		{
			name: "too few observations does not promote",
			mutate: func(c *BaselineCandidate) {
				c.Gaps = periodicGaps(10*time.Minute, 20*time.Second, 30)
			},
			want:      BaselineRejectFewSamples,
			reasonHas: "inter-arrival observations",
		},
		{
			name: "period too long for retained history does not promote",
			mutate: func(c *BaselineCandidate) {
				// Six-hourly job: only 12 repeats fit in three days.
				c.Gaps = periodicGaps(6*time.Hour, time.Minute, 61)
			},
			want:      BaselineRejectTooFewCycles,
			reasonHas: "repeats are required",
		},
		{
			name: "business-hours pattern does not promote",
			mutate: func(c *BaselineCandidate) {
				// Nine active hours a day out of twenty-four.
				c.HourBucketsSeen = 27
			},
			want:      BaselineRejectDiscontinuous,
			reasonHas: "not a continuous job",
		},
		{
			name: "weekday-only pattern does not promote",
			mutate: func(c *BaselineCandidate) {
				c.DayGaps = 8 // eight weekend days missing over four weeks
			},
			want:      BaselineRejectDayGaps,
			reasonHas: "weekly or weekday pattern",
		},
		{
			// Six gap-free days can be Monday-to-Saturday, which never
			// observes a Sunday and so cannot disprove a weekday-only job.
			name: "too little day history to rule out a weekly pattern",
			mutate: func(c *BaselineCandidate) {
				c.DayHistoryDays = BaselineMinDayHistoryDays - 1
			},
			want:      BaselineRejectDayGaps,
			reasonHas: "one whole week",
		},
		{
			name: "no derivable matcher does not promote",
			mutate: func(c *BaselineCandidate) {
				c.Masked, c.MatchSubstring = "<ts> <n> <n>", ""
			},
			want:      BaselineRejectNoMatcher,
			reasonHas: "no substring",
		},
		{
			name: "matcher that finds nothing does not promote",
			mutate: func(c *BaselineCandidate) {
				c.SubstringMatches = 0
			},
			want:      BaselineRejectMatcherUnverified,
			reasonHas: "false absence",
		},
		{
			name: "over-broad matcher does not promote",
			mutate: func(c *BaselineCandidate) {
				c.SubstringMatches = 900 // sweeps in sibling templates
			},
			want:      BaselineRejectMatcherUnverified,
			reasonHas: "over-broad",
		},
		{
			name: "failing collection does not teach a baseline",
			mutate: func(c *BaselineCandidate) {
				c.Health.ConsecutiveFailures = 2
			},
			want:      BaselineRejectCollectionUnhealthy,
			reasonHas: "collection has failed",
		},
		{
			name: "disabled source does not teach a baseline",
			mutate: func(c *BaselineCandidate) {
				c.Health.Enabled = false
			},
			want:      BaselineRejectCollectionUnhealthy,
			reasonHas: "disabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := promotableCandidate()
			tt.mutate(&c)
			v := EvaluateBaselineCandidate(c)
			if v.Decision != tt.want {
				t.Fatalf("decision = %q; want %q (reason: %s)", v.Decision, tt.want, v.Reason)
			}
			if v.Window != 0 {
				t.Errorf("rejected candidate proposed a window of %s; want 0", v.Window)
			}
			if !strings.Contains(v.Reason, tt.reasonHas) {
				t.Errorf("reason %q does not explain itself with %q", v.Reason, tt.reasonHas)
			}
		})
	}
}

// TestEvaluateBaselineCandidateOutageDoesNotMovePeriod is the drift test. A job
// that ran cleanly and then hung for twelve hours contributes exactly ONE
// enormous gap. The learned period must not move, or the next pass would widen
// the rule's window and quietly accept the outage as the new normal.
func TestEvaluateBaselineCandidateOutageDoesNotMovePeriod(t *testing.T) {
	cleanCandidate := promotableCandidate()
	clean := EvaluateBaselineCandidate(cleanCandidate)

	broken := promotableCandidate()
	broken.Gaps = append(append([]time.Duration{}, broken.Gaps...), 12*time.Hour)
	v := EvaluateBaselineCandidate(broken)

	if v.Stats.Median != clean.Stats.Median {
		t.Errorf("median moved from %.0fs to %.0fs after a 12h outage; the median must be immune",
			clean.Stats.Median, v.Stats.Median)
	}
	// The regularity score may twitch by a sample position but must stay far
	// from the threshold — an outage must not push a healthy job towards
	// looking irregular either.
	if v.Stats.RelativeMAD > BaselineMaxRelativeMAD/2 {
		t.Errorf("relative MAD = %.3f after a 12h outage; want well under %.2f",
			v.Stats.RelativeMAD, BaselineMaxRelativeMAD)
	}
	if v.Decision != BaselinePromoted {
		t.Errorf("decision = %q after one outage; a single gap must not disqualify a real job",
			v.Decision)
	}
	if v.Window > clean.Window {
		t.Errorf("window widened from %s to %s after an outage; an outage must never "+
			"relax the rule that would have caught it", clean.Window, v.Window)
	}
	// For contrast, the MEAN — the estimator this design deliberately avoids —
	// is visibly corrupted by the same single gap.
	if mean(broken.Gaps) <= mean(cleanCandidate.Gaps) {
		t.Fatal("fixture is wrong: the outage should have moved a mean")
	}
}

// TestEvaluateBaselineCandidateFullOutageYieldsNoSample proves the structural
// guarantee: a job hung for the whole horizon produces no arrivals, so it falls
// out as "few samples" — which callers treat as "leave the live rule alone",
// never as "the new normal is nothing".
func TestEvaluateBaselineCandidateFullOutageYieldsNoSample(t *testing.T) {
	c := promotableCandidate()
	c.Gaps = nil
	c.LineCount = 0
	v := EvaluateBaselineCandidate(c)
	if v.Decision != BaselineRejectFewSamples {
		t.Fatalf("decision = %q; want few_samples", v.Decision)
	}
	if v.Decision.Promoted() {
		t.Fatal("a silent job must never be promoted")
	}
}

func mean(gaps []time.Duration) float64 {
	if len(gaps) == 0 {
		return 0
	}
	var total float64
	for _, g := range gaps {
		total += g.Seconds()
	}
	return total / float64(len(gaps))
}

func TestDeriveMatchSubstring(t *testing.T) {
	tests := []struct {
		name   string
		masked string
		want   string
	}{
		{
			name:   "longest literal run between placeholders",
			masked: "<ts> INFO order sync completed batch=<n> in <dur>",
			want:   "INFO order sync completed batch=",
		},
		{
			name:   "all placeholders yields nothing",
			masked: "<ts> <n> <uuid>",
			want:   "",
		},
		{
			name:   "no placeholders yields the whole line",
			masked: "scheduler heartbeat ok",
			want:   "scheduler heartbeat ok",
		},
		{
			name:   "unterminated placeholder does not panic",
			masked: "worker started <incomplete",
			want:   "worker started <incomplete",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveMatchSubstring(tt.masked); got != tt.want {
				t.Errorf("DeriveMatchSubstring(%q) = %q; want %q", tt.masked, got, tt.want)
			}
		})
	}
}
