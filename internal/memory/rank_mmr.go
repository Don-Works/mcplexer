// rank_mmr.go — Maximal Marginal Relevance diversification of a candidate
// pool, plus its cosine helper. Split out of rank.go for the 300-line cap.
package memory

import (
	"math"

	"github.com/don-works/mcplexer/internal/store"
)

// mmrLambda trades relevance (1.0) against diversity (0.0) in the MMR
// reorder. 0.7 keeps relevance dominant while still penalizing
// near-duplicate candidates so the cross-encoder sees a varied set.
const mmrLambda = 0.7

// mmrReorder greedily reorders hits by Maximal Marginal Relevance:
// at each step it picks the unselected candidate maximizing
//
//	lambda*rel(i) - (1-lambda)*max_{j in selected} cosine(vec_i, vec_j)
//
// vecs[i] is the stored embedding for hits[i] (nil when unavailable).
// rel(i) is derived from the incoming order (earlier = more relevant)
// via 1/(rerankBaseK+i+1), matching the position scoring used elsewhere.
// Candidates without a vector can't be compared for redundancy, so they
// are treated as maximally diverse (redundancy 0) and otherwise ranked by
// relevance. When NO hit has a vector the result is identity (no reorder).
func mmrReorder(hits []store.MemoryHit, vecs [][]float32, lambda float64) []store.MemoryHit {
	n := len(hits)
	if n <= 1 {
		return hits
	}
	anyVec := false
	for _, v := range vecs {
		if len(v) > 0 {
			anyVec = true
			break
		}
	}
	if !anyVec {
		return hits // degrade to identity
	}
	rel := make([]float64, n)
	for i := range hits {
		rel[i] = 1.0 / float64(rerankBaseK+i+1)
	}
	selected := make([]int, 0, n)
	picked := make([]bool, n)
	for len(selected) < n {
		bestIdx, bestScore := -1, math.Inf(-1)
		for i := 0; i < n; i++ {
			if picked[i] {
				continue
			}
			redundancy := 0.0
			for _, j := range selected {
				if len(vecs[i]) == 0 || len(vecs[j]) == 0 {
					continue
				}
				if c := cosineF32(vecs[i], vecs[j]); c > redundancy {
					redundancy = c
				}
			}
			score := lambda*rel[i] - (1.0-lambda)*redundancy
			if score > bestScore {
				bestScore, bestIdx = score, i
			}
		}
		if bestIdx < 0 {
			break
		}
		picked[bestIdx] = true
		selected = append(selected, bestIdx)
	}
	out := make([]store.MemoryHit, 0, n)
	for _, idx := range selected {
		out = append(out, hits[idx])
	}
	return out
}

// cosineF32 is the cosine similarity of two equal-length float32 vectors.
// Returns 0 when either is zero-length or zero-magnitude.
func cosineF32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
