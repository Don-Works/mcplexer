package store

import (
	"math/rand"
	"time"
)

// Fixtures shared by the promotion-ladder tests. Split out of
// monitoring_baseline_learn_test.go to keep every file inside the 300-line gate.

// baselineTestNow is a fixed instant so every case is reproducible.
var baselineTestNow = time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)

// periodicGaps builds a clean scheduled cadence: period with bounded jitter,
// deterministic because the jitter walks a fixed pattern rather than a PRNG.
func periodicGaps(period time.Duration, jitter time.Duration, n int) []time.Duration {
	// A sixteen-step sweep across the jitter band. Spread deliberately, not
	// bimodal: a -j/0/+j/0 pattern makes the deviation distribution a knife
	// edge where a median can jump on a single extra sample, which would be a
	// property of the fixture rather than of the estimator.
	steps := []int{-16, -11, -7, -4, -2, -1, 0, 0, 0, 1, 2, 4, 7, 11, 16, 0}
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, period+time.Duration(steps[i%len(steps)])*jitter/16)
	}
	return out
}

// randomGaps builds exponentially distributed arrivals — the null hypothesis
// the promotion thresholds are set against. Seeded, so it is deterministic.
func randomGaps(mean time.Duration, n int, seed int64) []time.Duration {
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test fixture
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, time.Duration(r.ExpFloat64()*float64(mean)))
	}
	return out
}

// promotableCandidate is a signal that SHOULD be learned: a job running every
// ten minutes, continuously, for three days, with a month of clean day history.
// Individual tests break exactly one property to prove that property matters.
func promotableCandidate() BaselineCandidate {
	const span = 72 * time.Hour
	first := baselineTestNow.Add(-span)
	gaps := periodicGaps(10*time.Minute, 20*time.Second, 432)
	return BaselineCandidate{
		WorkspaceID: "ws-1", SourceID: "src-1", TemplateID: "tpl-orders-sync",
		Masked:    "order sync completed batch=<n> in <dur>",
		Gaps:      gaps,
		FirstSeen: first, LastSeen: baselineTestNow, LineCount: 433,
		HourBucketsSeen: 73, HourBucketsTotal: 73,
		DayHistoryDays: 28, DayGaps: 0,
		MatchSubstring:   "order sync completed batch=",
		SubstringMatches: 433, SubstringTemplateLines: 433,
		Health: SourceCollectionHealth{Enabled: true},
	}
}
