// monitoring_baseline_burst.go — recognising a BURSTY-BUT-CLOCKWORK signal.
//
// The regularity gate originally measured variance of raw inter-arrival gaps,
// which silently assumed one arrival per tick. Real scheduled jobs rarely look
// like that. The order-sync job this feature exists for emits roughly three
// completions bunched back-to-back every five minutes, so its gap sample is
// [1s, 1s, 298s, 1s, 1s, 298s, ...]: a median of 1s and a p95 of 298s, giving a
// p95/median ratio of 298 against a cap of 3. The most reliably periodic signal
// on the box scored as pure noise, and recall was therefore zero.
//
// The error was measuring the wrong thing, not measuring it too strictly. A
// burst is ONE logical arrival of a tick, so the cadence lives in the burst
// START times and the raw gaps are a mixture of two distributions. This file
// separates them and hands the tick series back to the ordinary statistics, so
// the promotion thresholds are applied UNCHANGED to the right series rather
// than loosened to accommodate the wrong one.
package store

import (
	"sort"
	"time"
)

// BurstSplitThreshold finds the gap length that separates intra-burst spacing
// from the inter-burst tick, or 0 when the sample is not bursty.
//
// The test is BIMODALITY, and it is deliberately a hard one. A bursty-clockwork
// sample has two tight clusters with an empty band between them, so somewhere
// in the sorted sample one step multiplies the gap length several-fold. A
// random (exponential) arrival process has no such band: its order statistics
// are densely packed, and consecutive ratios in the interior of a sample of
// hundreds sit near 1.00, nowhere near BaselineBurstSeparation. That is what
// stops this being a back door into promoting noise — a template only gets the
// burst treatment if it PROVES it arrives in clusters.
//
// Both ends of the sample are excluded from the search. A split near either
// edge would carve a handful of outliers off an otherwise unimodal sample and
// call the remainder a cadence; requiring BaselineMinDeltas gaps on each side
// means a claimed tick series is also large enough for a robust median.
func BurstSplitThreshold(sorted []float64) float64 {
	n := len(sorted)
	if n < 2*BaselineMinDeltas {
		return 0
	}
	best, idx := 0.0, -1
	for i := BaselineMinDeltas; i < n-BaselineMinDeltas; i++ {
		if sorted[i-1] <= 0 {
			continue
		}
		if r := sorted[i] / sorted[i-1]; r > best {
			best, idx = r, i
		}
	}
	if idx < 0 || best < BaselineBurstSeparation {
		return 0
	}
	return sorted[idx]
}

// SplitBursts clusters an arrival sample into bursts and returns the
// burst-START-to-burst-START periods plus the arrival count in each burst.
//
// Start-to-start is used rather than the raw inter-burst gap because it is the
// actual period of the schedule: the gap from the LAST arrival of one tick to
// the FIRST of the next understates the period by however long a burst runs.
// Feeding the understated value forward would size every absence window off a
// slightly wrong cadence.
//
// gaps must be in arrival order, which is the order the miner streams them in.
func SplitBursts(gaps []time.Duration, threshold float64) ([]time.Duration, []float64) {
	if threshold <= 0 || len(gaps) == 0 {
		return nil, nil
	}
	// Arrival times relative to the first arrival, which sits at zero.
	elapsed, size := 0.0, 1.0
	starts := []float64{0}
	sizes := []float64{}
	for _, g := range gaps {
		elapsed += g.Seconds()
		if g.Seconds() >= threshold {
			sizes = append(sizes, size)
			size = 1
			starts = append(starts, elapsed)
			continue
		}
		size++
	}
	sizes = append(sizes, size)
	ticks := make([]time.Duration, 0, len(starts))
	for i := 1; i < len(starts); i++ {
		ticks = append(ticks, time.Duration((starts[i]-starts[i-1])*float64(time.Second)))
	}
	return ticks, sizes
}

// burstCadence reduces a raw gap sample to the series the cadence actually
// lives in. It returns the original sample unchanged unless the sample proves
// bimodal AND the resulting tick series is itself large enough to summarise
// robustly — a handful of bursts is not a schedule.
func burstCadence(gaps []time.Duration, sorted []float64) ([]time.Duration, bool, float64) {
	threshold := BurstSplitThreshold(sorted)
	if threshold <= 0 {
		return gaps, false, 0
	}
	ticks, sizes := SplitBursts(gaps, threshold)
	if len(ticks) < BaselineMinDeltas {
		return gaps, false, 0
	}
	sort.Float64s(sizes)
	return ticks, true, MedianSorted(sizes)
}
