package baseline

import (
	"math"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Fixtures shared by the learner tests. Split out of learner_test.go to keep
// every file in this package inside the 300-line gate.

// fixedNow is the learner's clock in every test here. No wall clock is ever
// read, so a pass is reproducible to the second.
var fixedNow = time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)

// periodicGaps builds a scheduled cadence with bounded, deterministic jitter:
// ONE arrival per period, evenly spaced.
//
// It is deliberately NOT the shape of the real order-sync job, and the comment
// here used to claim it was — "5-minute tick, ~40 completions/hour" — while
// every caller passed a 10-minute period and a single arrival per tick, which
// is 6/hour and perfectly uniform. That mislabelling hid the defect this
// package exists to fix: uniform spacing was the only shape the regularity gate
// ever admitted, so the tests were green while real recall was zero. Use
// burstyGaps for the production shape.
func periodicGaps(period, jitter time.Duration, n int) []time.Duration {
	steps := []int{-16, -11, -7, -4, -2, -1, 0, 0, 0, 1, 2, 4, 7, 11, 16, 0}
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, period+time.Duration(steps[i%len(steps)])*jitter/16)
	}
	return out
}

// burstyGaps builds the shape the real order-sync job actually has: a tick
// every `period` in which several completions arrive bunched `intra` apart.
//
// perTickNum/perTickDen express a fractional burst size — the measured job runs
// ~3.3 completions per 5-minute tick (~40/hour) — so the sample contains a mix
// of 3-arrival and 4-arrival bursts rather than an artificially uniform one.
func burstyGaps(period, intra time.Duration, perTickNum, perTickDen, ticks int) []time.Duration {
	out := []time.Duration{}
	for i := 0; i < ticks; i++ {
		n := perTickNum / perTickDen
		if i%perTickDen < perTickNum%perTickDen {
			n++
		}
		for j := 1; j < n; j++ {
			out = append(out, intra)
		}
		out = append(out, period-time.Duration(n-1)*intra)
	}
	return out
}

// noisyGaps builds genuinely irregular arrivals: an exponential (Poisson)
// inter-arrival process, which is what a NON-scheduled template really looks
// like. Deterministic from the seed so a rejection is reproducible.
func noisyGaps(mean time.Duration, n int, seed int64) []time.Duration {
	out := make([]time.Duration, 0, n)
	state := uint64(seed)*2862933555777941757 + 3037000493
	for i := 0; i < n; i++ {
		state = state*6364136223846793005 + 1442695040888963407
		u := float64((state>>11)&(1<<52-1))/float64(uint64(1)<<52)*0.999998 + 1e-6
		out = append(out, time.Duration(-math.Log(u)*float64(mean)))
	}
	return out
}

func testSource() *store.LogSource {
	return &store.LogSource{ID: "src-1", WorkspaceID: "ws-1", Name: "orders-api", Enabled: true}
}

// cleanCandidate is a job that SHOULD be learned. Each test breaks exactly one
// property so the property under test is the only thing that can explain the
// change in outcome.
func cleanCandidate() store.BaselineCandidate {
	const span = 72 * time.Hour
	return store.BaselineCandidate{
		WorkspaceID: "ws-1", SourceID: "src-1", TemplateID: "tpl-orders",
		Masked:    "order sync completed batch=<n> in <dur>",
		Gaps:      periodicGaps(10*time.Minute, 20*time.Second, 432),
		FirstSeen: fixedNow.Add(-span), LastSeen: fixedNow, LineCount: 433,
		HourBucketsSeen: 73, HourBucketsTotal: 73,
		DayHistoryDays: 28, DayGaps: 0,
		MatchSubstring:   "order sync completed batch=",
		SubstringMatches: 433, SubstringTemplateLines: 433,
		Health: store.SourceCollectionHealth{Enabled: true},
	}
}

// newTestLearner wires a learner onto a fake store and a frozen clock.
func newTestLearner(f *fakeLearnerStore, candidates ...store.BaselineCandidate) *Learner {
	f.sources = []*store.LogSource{testSource()}
	f.candidates["src-1"] = candidates
	l := NewLearner(f)
	l.now = func() time.Time { return fixedNow }
	return l
}
