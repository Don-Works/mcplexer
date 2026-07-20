// monitoring_baseline_stats.go — the robust statistics the baseline learner
// runs on inter-arrival gaps.
//
// Everything here is a ROBUST estimator (median, MAD, quantile) rather than the
// obvious mean/stddev, and that choice is the whole drift-resistance story.
//
// A job that runs every 10 minutes for a week and then hangs for 12 hours
// contributes ONE enormous gap to a sample of ~1000. That single value moves
// the mean by ~7% and the standard deviation by an order of magnitude — enough
// to make an outage look like a legitimate cadence change and be relearned as
// normal. It moves the median and the MAD by nothing at all, because they read
// the middle of the sorted sample and the outage is at the end of it.
package store

import (
	"math"
	"sort"
	"time"
)

// DurationSeconds converts a gap sample to float seconds. Kept explicit so the
// learner's arithmetic is all in one unit and comparisons cannot mix scales.
func DurationSeconds(d time.Duration) float64 { return d.Seconds() }

// SortedSeconds copies gaps into an ascending float-second slice. The copy is
// deliberate: callers hold their sample in arrival order for other checks and
// must not have it reordered underneath them.
func SortedSeconds(gaps []time.Duration) []float64 {
	out := make([]float64, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, DurationSeconds(g))
	}
	sort.Float64s(out)
	return out
}

// Quantile returns the q-th quantile of an ALREADY-SORTED sample using linear
// interpolation between order statistics. Returns 0 for an empty sample, which
// every caller treats as "no evidence" rather than "zero seconds".
func Quantile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

// MedianSorted returns the median of an already-sorted sample.
func MedianSorted(sorted []float64) float64 { return Quantile(sorted, 0.5) }

// MedianAbsoluteDeviation returns the median of |x - median|, the robust
// dispersion estimate. Unlike a standard deviation it has a breakdown point of
// 50%: half the sample would have to be outage-length before it moved.
func MedianAbsoluteDeviation(sorted []float64, median float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	deviations := make([]float64, 0, len(sorted))
	for _, v := range sorted {
		deviations = append(deviations, math.Abs(v-median))
	}
	sort.Float64s(deviations)
	return MedianSorted(deviations)
}

// BaselineStats is the full robust summary of one arrival sample.
//
// Every duration below describes the CADENCE series, which for a bursty signal
// is the tick rather than the individual arrival — see Bursty. Reporting the
// tick here rather than at the call sites is what keeps the promotion ladder,
// the confidence score and the absence window all measuring the same thing.
type BaselineStats struct {
	Count       int
	Median      float64 // seconds — the learned period
	P95         float64 // seconds
	MAD         float64 // seconds
	RelativeMAD float64 // MAD / median — dimensionless regularity
	P95Ratio    float64 // p95 / median — dimensionless tail weight

	// Bursty reports that arrivals bunch into clusters and the statistics
	// above therefore describe burst-to-burst timing. Count is then the number
	// of ticks observed, not the number of arrivals.
	Bursty bool
	// BurstSize is the median number of arrivals per tick; 1 when not bursty.
	BurstSize float64
	// RawCount is the raw inter-arrival sample size before burst clustering,
	// kept so an operator can see how much bunching was collapsed.
	RawCount int
}

// SummarizeGaps computes the robust summary of an inter-arrival sample.
//
// RelativeMAD and P95Ratio are both dimensionless on purpose: the same
// thresholds then apply to a 30-second job and an hourly one without any
// per-source tuning, which is what makes the learner deployable with nobody
// configuring anything.
//
// Bunched arrivals are collapsed to their ticks FIRST (see
// monitoring_baseline_burst.go). This changes which series is measured, never
// how strictly: a bursty candidate faces exactly the same thresholds, applied
// to its burst-to-burst timing. Sizing the window off the raw sample instead
// would be actively dangerous — the order-sync job's raw median is 1s, and a
// window derived from it would alert on every ordinary five-minute quiet.
func SummarizeGaps(gaps []time.Duration) BaselineStats {
	sorted := SortedSeconds(gaps)
	stats := BaselineStats{RawCount: len(gaps), BurstSize: 1}
	series, bursty, burstSize := burstCadence(gaps, sorted)
	if bursty {
		sorted = SortedSeconds(series)
		stats.Bursty, stats.BurstSize = true, burstSize
	}
	stats.Count = len(sorted)
	if len(sorted) == 0 {
		return stats
	}
	stats.Median = MedianSorted(sorted)
	stats.P95 = Quantile(sorted, 0.95)
	stats.MAD = MedianAbsoluteDeviation(sorted, stats.Median)
	if stats.Median > 0 {
		stats.RelativeMAD = stats.MAD / stats.Median
		stats.P95Ratio = stats.P95 / stats.Median
	}
	return stats
}

// Regular reports whether the sample looks scheduled rather than random.
//
// Both tests must pass. They fail differently and a real scheduler passes both
// comfortably: RelativeMAD catches a sample whose bulk is spread out, P95Ratio
// catches one whose bulk is tight but whose tail is not. For the exponential
// arrivals a non-scheduled template produces, the true values are 0.694 and
// 4.32 — each threshold sits well inside that, so a random template is rejected
// on both counts rather than squeaking past one.
func (s BaselineStats) Regular() bool {
	if s.Median <= 0 || s.Count < BaselineMinDeltas {
		return false
	}
	return s.RelativeMAD <= BaselineMaxRelativeMAD && s.P95Ratio <= BaselineMaxP95Ratio
}

// Confidence scores a candidate in [0,1] for operator display and ordering. It
// is NOT part of the promotion test — promotion is the explicit threshold
// ladder in the learner, so a single blended number can never let a candidate
// in the back door by being strong on one axis and disqualifying on another.
func (s BaselineStats) Confidence(cycles float64, occupancy float64) float64 {
	if s.Median <= 0 || s.Count == 0 {
		return 0
	}
	regularity := 1 - math.Min(1, s.RelativeMAD/BaselineMaxRelativeMAD)
	tail := 1 - math.Min(1, (s.P95Ratio-1)/(BaselineMaxP95Ratio-1))
	evidence := math.Min(1, cycles/(BaselineMinCycles*4))
	coverage := math.Max(0, math.Min(1, occupancy))
	score := 0.35*regularity + 0.25*tail + 0.25*evidence + 0.15*coverage
	return math.Round(score*1000) / 1000
}

// WindowFor sizes the absence window from the learned shape.
//
// The window is the direct false-positive knob, so it is the larger of "several
// missed runs" and "several times the observed tail" — and is then clamped to
// BaselineMaxWindowPeriodMultiple periods. That clamp is deliberate and load
// bearing: p95 is robust but not immune, and without the clamp a long enough
// degradation could keep widening its own alarm threshold until it never fired.
func (s BaselineStats) WindowFor() time.Duration {
	if s.Median <= 0 {
		return 0
	}
	byPeriod := s.Median * BaselineWindowPeriodMultiple
	byTail := s.P95 * BaselineWindowP95Multiple
	window := math.Max(byPeriod, byTail)
	if ceiling := s.Median * BaselineMaxWindowPeriodMultiple; window > ceiling {
		window = ceiling
	}
	sized := time.Duration(math.Round(window)) * time.Second
	// The floor is applied AFTER the ceiling on purpose: the ceiling exists to
	// stop a degrading job disarming its own alarm, the floor to stop a very
	// fast job alarming on ordinary lateness. They never fight, because the
	// floor only ever widens.
	if sized < BaselineMinPromotedWindow {
		sized = BaselineMinPromotedWindow
	}
	return sized
}
