package baseline

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestLearnPromotesTheRealBurstyShape is the recall regression, driven through
// the whole learner rather than the statistics alone.
//
// The fixture is the MEASURED production shape — a 5-minute tick carrying ~3.3
// completions bunched a second apart, ~40/hour, flat 24/7 — not the uniform
// one-arrival-per-tick shape the older fixture used while claiming to be this.
// Against the old regularity gate this exact sample scored p95/median = 298
// with a cap of 3 and was rejected as noise, which is why the detector would
// not have fired for the incident it was built for.
func TestLearnPromotesTheRealBurstyShape(t *testing.T) {
	c := cleanCandidate()
	// 7 days of retained history at a 5-minute tick.
	const span = 7 * 24 * time.Hour
	c.Gaps = burstyGaps(5*time.Minute, time.Second, 10, 3, 2016)
	c.FirstSeen, c.LastSeen = fixedNow.Add(-span), fixedNow
	c.HourBucketsSeen, c.HourBucketsTotal = 169, 169
	c.LineCount = int64(len(c.Gaps) + 1)
	c.SubstringMatches, c.SubstringTemplateLines = c.LineCount, c.LineCount

	f := newFakeLearnerStore()
	newTestLearner(f, c).Learn(context.Background())

	if len(f.created) != 1 {
		b := f.baselines["tpl-orders"]
		reason := "no baseline recorded"
		if b != nil {
			reason = string(b.Decision) + ": " + b.Reason
		}
		t.Fatalf("created %d rules from the real production shape; want 1 — %s",
			len(f.created), reason)
	}
	rule := f.created[0]
	// A window sized off the 1s intra-burst gap would collapse to the
	// 5-minute floor and fire on every ordinary quiet stretch between ticks.
	if rule.WindowSeconds < int64(20*time.Minute/time.Second) {
		t.Errorf("window = %ds; too tight for a 5-minute tick — the intra-burst gap "+
			"was mistaken for the period", rule.WindowSeconds)
	}
	b := f.baselines["tpl-orders"]
	if b == nil || b.Decision != store.BaselinePromoted {
		t.Fatalf("baseline decision = %v; want promoted", b)
	}
	if b.PeriodSeconds < 290 || b.PeriodSeconds > 310 {
		t.Errorf("learned period = %.1fs; want the ~300s tick", b.PeriodSeconds)
	}
	t.Logf("PROMOTED: %s", b.Reason)
}

// TestLearnStillRefusesGenuineNoise is the precision counterpart. Burst
// detection must not have opened a door for random arrivals: a false "your
// orders stopped!" gets the whole system muted, which is worse than missing
// the incident it was meant to catch.
func TestLearnStillRefusesGenuineNoise(t *testing.T) {
	for _, seed := range []int64{1, 7, 42} {
		c := cleanCandidate()
		c.Gaps = noisyGaps(10*time.Minute, 432, seed)

		f := newFakeLearnerStore()
		newTestLearner(f, c).Learn(context.Background())

		if len(f.created) != 0 {
			t.Errorf("seed %d: created a rule from random arrivals", seed)
		}
		if b := f.baselines["tpl-orders"]; b == nil ||
			b.Decision != store.BaselineRejectIrregular {
			t.Errorf("seed %d: decision = %v; want irregular", seed, b)
		}
	}
}
