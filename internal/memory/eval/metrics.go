package eval

import (
	"math"
	"sort"
	"time"
)

// RankedResult is one query's outcome: the relevant-key set it was scored
// against and the ranked list of result keys the retriever returned (rank 0
// = best). Keys that are not in the corpus label space are still listed so
// nDCG/recall penalize off-target hits by pushing relevant ones down.
type RankedResult struct {
	Query        string
	RelevantKeys map[string]struct{}
	RankedKeys   []string // ordered best→worst, length capped at k by caller
}

// recallAtK is |relevant ∩ top-k| / |relevant|. Returns 1.0 for a query
// with no relevant documents (vacuously satisfied) so it never drags the
// mean down — such a query is a labeling bug the caller should catch, not a
// retrieval failure.
func recallAtK(r RankedResult, k int) float64 {
	if len(r.RelevantKeys) == 0 {
		return 1.0
	}
	found := 0
	for i, key := range r.RankedKeys {
		if i >= k {
			break
		}
		if _, ok := r.RelevantKeys[key]; ok {
			found++
		}
	}
	return float64(found) / float64(len(r.RelevantKeys))
}

// ndcgAtK is normalized discounted cumulative gain at k with binary
// relevance (gain 1 for relevant, 0 otherwise). DCG uses the standard
// log2(rank+2) discount. The ideal DCG places every relevant doc (capped at
// k) at the top. Returns 1.0 when there are no relevant docs.
func ndcgAtK(r RankedResult, k int) float64 {
	if len(r.RelevantKeys) == 0 {
		return 1.0
	}
	dcg := 0.0
	for i, key := range r.RankedKeys {
		if i >= k {
			break
		}
		if _, ok := r.RelevantKeys[key]; ok {
			dcg += 1.0 / math.Log2(float64(i)+2.0)
		}
	}
	idealN := len(r.RelevantKeys)
	if idealN > k {
		idealN = k
	}
	idcg := 0.0
	for i := 0; i < idealN; i++ {
		idcg += 1.0 / math.Log2(float64(i)+2.0)
	}
	if idcg == 0 {
		return 1.0
	}
	return dcg / idcg
}

// reciprocalRank is 1/(rank of first relevant hit), or 0 if none surfaced
// within the ranked list. Rank is 1-based for the MRR convention.
func reciprocalRank(r RankedResult) float64 {
	for i, key := range r.RankedKeys {
		if _, ok := r.RelevantKeys[key]; ok {
			return 1.0 / float64(i+1)
		}
	}
	return 0.0
}

// precisionAt1 is 1.0 when the top-ranked hit is relevant, else 0.0. It is the
// sharpest expression of the production symptom ("the #1 result is an unrelated
// document"): mean nDCG@5 degrades gently when only the top slot is poisoned,
// whereas P@1 collapses immediately. Returns 1.0 for a query with no relevant
// documents, matching the vacuous-satisfaction convention above.
func precisionAt1(r RankedResult) float64 {
	if len(r.RelevantKeys) == 0 {
		return 1.0
	}
	if len(r.RankedKeys) == 0 {
		return 0.0
	}
	if _, ok := r.RelevantKeys[r.RankedKeys[0]]; ok {
		return 1.0
	}
	return 0.0
}

// MetricReport is the aggregate retrieval-quality snapshot the gate asserts
// against. All scores are means over the query set, in [0,1].
type MetricReport struct {
	K            int
	NumQueries   int
	RecallAtK    float64
	NDCGAtK      float64
	MRR          float64
	PrecisionAt1 float64
	// PerQuery preserves the individual outcomes so a failing gate can name
	// the exact query that regressed instead of just a sunk aggregate.
	PerQuery []QueryScore
}

// QueryScore is one query's contribution to the aggregate. TopKey records the
// rank-0 result so a failing gate can name the document that displaced the
// correct answer instead of only reporting a sunk number.
type QueryScore struct {
	Query     string
	RecallAtK float64
	NDCGAtK   float64
	RR        float64
	P1        float64
	TopKey    string
}

// Aggregate folds a slice of ranked results into a MetricReport at cutoff k.
func Aggregate(results []RankedResult, k int) MetricReport {
	rep := MetricReport{K: k, NumQueries: len(results)}
	if len(results) == 0 {
		return rep
	}
	var sumRecall, sumNDCG, sumRR, sumP1 float64
	for _, r := range results {
		rk := recallAtK(r, k)
		nd := ndcgAtK(r, k)
		rr := reciprocalRank(r)
		p1 := precisionAt1(r)
		sumRecall += rk
		sumNDCG += nd
		sumRR += rr
		sumP1 += p1
		top := ""
		if len(r.RankedKeys) > 0 {
			top = r.RankedKeys[0]
		}
		rep.PerQuery = append(rep.PerQuery, QueryScore{
			Query: r.Query, RecallAtK: rk, NDCGAtK: nd, RR: rr, P1: p1, TopKey: top,
		})
	}
	n := float64(len(results))
	rep.RecallAtK = sumRecall / n
	rep.NDCGAtK = sumNDCG / n
	rep.MRR = sumRR / n
	rep.PrecisionAt1 = sumP1 / n
	return rep
}

// LatencyReport summarizes per-query retrieval latency. p50/p95 use the
// nearest-rank percentile method over the sorted sample.
type LatencyReport struct {
	Samples int
	P50     time.Duration
	P95     time.Duration
	Max     time.Duration
}

// summarizeLatency computes p50/p95/max over a latency sample. An empty
// sample yields a zero report.
func summarizeLatency(samples []time.Duration) LatencyReport {
	if len(samples) == 0 {
		return LatencyReport{}
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return LatencyReport{
		Samples: len(sorted),
		P50:     percentile(sorted, 50),
		P95:     percentile(sorted, 95),
		Max:     sorted[len(sorted)-1],
	}
}

// percentile returns the p-th percentile (nearest-rank) of a pre-sorted
// ascending sample. p is in [0,100].
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank: ceil(p/100 * N), 1-based.
	rank := int(math.Ceil(float64(p)/100.0*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
