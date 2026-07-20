package store

import (
	"testing"
	"time"
)

// burstCadenceSample builds `ticks` bursts of `perTick` arrivals spaced `intra`
// apart, one burst every `period`.
func burstCadenceSample(period, intra time.Duration, perTick, ticks int) []time.Duration {
	out := []time.Duration{}
	for i := 0; i < ticks; i++ {
		for j := 1; j < perTick; j++ {
			out = append(out, intra)
		}
		out = append(out, period-time.Duration(perTick-1)*intra)
	}
	return out
}

// TestSummarizeGapsReadsBurstsAsTicks is the regression for the defect that
// made recall zero. Every intra-burst spacing below was measured by a verifier
// against the OLD code and scored "irregular": p95/median came out at 298, 58
// and 8 against a cap of 3, because the gate measured individual arrivals when
// the cadence lives in the tick.
func TestSummarizeGapsReadsBurstsAsTicks(t *testing.T) {
	tests := []struct {
		name  string
		intra time.Duration
	}{
		{"back-to-back arrivals", time.Second},
		{"a few seconds apart", 5 * time.Second},
		{"loosely bunched", 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SummarizeGaps(burstCadenceSample(5*time.Minute, tt.intra, 3, 300))
			if !s.Bursty {
				t.Fatalf("Bursty = false; a %s-spaced burst every 5m is bunched", tt.intra)
			}
			if !s.Regular() {
				t.Errorf("Regular() = false (relMAD %.3f, p95ratio %.2f); a clockwork tick "+
					"must promote however its arrivals bunch", s.RelativeMAD, s.P95Ratio)
			}
			// The period must be the tick. Reading the intra-burst gap as the
			// period would size the absence window in seconds and alert on
			// every ordinary quiet stretch between ticks.
			if s.Median < 290 || s.Median > 310 {
				t.Errorf("Median = %.1fs; want the ~300s tick", s.Median)
			}
			if s.Count != 300 {
				t.Errorf("Count = %d; want 300 ticks", s.Count)
			}
			if s.RawCount != 900 {
				t.Errorf("RawCount = %d; want the 900 raw gaps preserved", s.RawCount)
			}
			if s.BurstSize != 3 {
				t.Errorf("BurstSize = %.1f; want 3", s.BurstSize)
			}
			if w := s.WindowFor(); w < 20*time.Minute {
				t.Errorf("WindowFor = %s; too tight for a 5-minute tick", w)
			}
		})
	}
}

// TestSummarizeGapsRejectsNoiseAsBursts is the precision half, and it matters
// more than the recall half: a false "your orders stopped!" gets the whole
// system muted, which is worse than missing the incident. Burst detection must
// not become a back door for promoting random arrivals.
func TestSummarizeGapsRejectsNoiseAsBursts(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 7, 42, 99} {
		s := SummarizeGaps(randomGaps(90*time.Second, 900, seed))
		if s.Bursty {
			t.Errorf("seed %d: exponential arrivals detected as bursts (median %.1fs)",
				seed, s.Median)
		}
		if s.Regular() {
			t.Errorf("seed %d: random arrivals scored regular (relMAD %.3f, p95ratio %.2f)",
				seed, s.RelativeMAD, s.P95Ratio)
		}
	}
}

// TestBurstSplitThresholdRequiresRealBimodality pins the guards that keep the
// split honest rather than opportunistic.
func TestBurstSplitThresholdRequiresRealBimodality(t *testing.T) {
	tests := []struct {
		name  string
		gaps  []time.Duration
		split bool
	}{
		{
			name:  "a clean 8x band splits",
			gaps:  burstCadenceSample(5*time.Minute, 30*time.Second, 3, 300),
			split: true,
		},
		{
			name: "a gentle spread does not",
			// Alternating 60s/90s: bunched-looking but only 1.5x apart, which
			// is ordinary scheduler jitter and belongs in one distribution.
			gaps:  burstCadenceSample(150*time.Second, 60*time.Second, 2, 400),
			split: false,
		},
		{
			name:  "exponential noise has no empty band",
			gaps:  randomGaps(90*time.Second, 900, 5),
			split: false,
		},
		{
			name: "too small a sample to claim a tick series",
			// Genuinely bimodal, but only 40 bursts — below the robustness
			// budget, so it is not treated as a cadence.
			gaps:  burstCadenceSample(5*time.Minute, time.Second, 3, 40),
			split: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummarizeGaps(tt.gaps).Bursty
			if got != tt.split {
				t.Errorf("Bursty = %v; want %v", got, tt.split)
			}
		})
	}
}

// TestSplitBurstsMeasuresStartToStart proves the period is burst-start to
// burst-start. The last-arrival-to-next-first gap understates it by the width
// of a burst, and every absence window would inherit that error.
func TestSplitBurstsMeasuresStartToStart(t *testing.T) {
	// 3 arrivals 1s apart every 300s, for 5 ticks. The sample ends on an
	// inter-burst gap, so it describes 16 arrivals: five full bursts plus the
	// first arrival of a sixth, hence six burst starts and five periods.
	ticks, sizes := SplitBursts(burstCadenceSample(300*time.Second, time.Second, 3, 5), 100)
	if len(ticks) != 5 {
		t.Fatalf("ticks = %d; want 5 periods between 6 burst starts", len(ticks))
	}
	for i, tk := range ticks {
		if tk != 300*time.Second {
			t.Errorf("tick[%d] = %s; want the true 300s period, not the 298s raw gap", i, tk)
		}
	}
	if len(sizes) != 6 {
		t.Fatalf("sizes = %d; want one per burst start", len(sizes))
	}
	for i, s := range sizes[:5] {
		if s != 3 {
			t.Errorf("burst[%d] size = %.0f; want 3", i, s)
		}
	}
	if sizes[5] != 1 {
		t.Errorf("trailing burst size = %.0f; want the single arrival that opened it", sizes[5])
	}
}

// TestBurstDetectionSurvivesAnOutage checks the interaction between burst
// clustering and the robustness the whole design rests on. A hung job
// contributes ONE enormous gap, and that gap must not be mistaken for the
// second mode of a bimodal sample — if it were, the "tick" would become
// outage-to-outage timing and the learner would be reading breakage as shape.
func TestBurstDetectionSurvivesAnOutage(t *testing.T) {
	gaps := burstCadenceSample(5*time.Minute, time.Second, 3, 300)
	clean := SummarizeGaps(gaps)
	withOutage := SummarizeGaps(append(gaps, 12*time.Hour))

	if !withOutage.Bursty {
		t.Fatal("a single outage destroyed burst detection")
	}
	if withOutage.Median != clean.Median {
		t.Errorf("period moved from %.1fs to %.1fs on one outage; the median must be immune",
			clean.Median, withOutage.Median)
	}
	if withOutage.WindowFor() != clean.WindowFor() {
		t.Errorf("window moved from %s to %s on one outage; a broken job must not widen "+
			"its own alarm threshold", clean.WindowFor(), withOutage.WindowFor())
	}
}
