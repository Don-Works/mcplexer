package memory

import (
	"math"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	// minSaveContentChars is the floor for Service.Write content length
	// after trimming unless the save is pinned.
	minSaveContentChars = 8

	// recencyHalfLife halves a hit's recency factor every 30 days.
	recencyHalfLife = 30 * 24 * time.Hour

	// recencyFloor keeps very old memories rankable: the recency factor
	// asymptotes to this instead of zero, so relevance still dominates
	// between two equally stale rows.
	recencyFloor = 0.05

	// pinnedBoost multiplies a pinned hit's score.
	pinnedBoost = 1.5

	// rerankBaseK is the RRF-style smoothing constant used to convert a
	// list position into a base relevance score: base(i) = 1/(k+i+1).
	// Matches the k=60 used by rrfFuse so the scales line up.
	rerankBaseK = 60

	// foldPoolN is the worst-case pool depth foldRecencyPin sizes its
	// tie-breaker nudge against (Recall caps the cross-encoder pool at
	// k*2 with the default k=20 → 40). The smallest gap between adjacent
	// position bases base(i)-base(i+1) shrinks as i grows; sizing epsilon
	// against the gap at the DEEPEST position guarantees the nudge stays
	// sub-gap everywhere shallower too. See foldRecencyEpsilon.
	foldPoolN = 40

	// foldPinExtra is the EXTRA nudge weight a pinned hit gets on top of
	// its normalized-recency nudge in foldRecencyPin (the recency nudge is
	// already in [0,1]). Kept < 1 so the combined nudge for a pinned,
	// maximally-fresh hit is epsilon*(1+foldPinExtra), still strictly below
	// one position step (see foldRecencyEpsilon): a pin can break a near-tie
	// or lift a hit a single step, never leap a clearly-more-relevant doc.
	foldPinExtra = 0.8

	// rankBlendAlpha weights the position-derived base score versus the
	// retrieval signal magnitude (BM25 / vector distance) when computing a
	// hit's base relevance. base = alpha*posScore + (1-alpha)*normSignal.
	// alpha is high so the base stays in the same ~1/61..1/121 band the
	// recency (0.05,1.0] and pinnedBoost (1.5x) multipliers were tuned
	// against — the signal magnitude only breaks ties between hits that
	// landed at the same list position (e.g. when two arms each contribute
	// one hit, both at rank 0 of their list after fusion).
	rankBlendAlpha = 0.85

	// recallSaturationN is the recall count at which the frequency half of
	// the recall nudge saturates to ~1.0 via count/(count+recallSaturationN).
	// Kept small so a handful of recalls already reads as "frequently
	// recalled" — the nudge is a tie-breaker, not a popularity contest, so
	// the curve should flatten fast rather than reward raw volume.
	recallSaturationN = 3.0

	// recallRecencyHalfLife halves the recency half of the recall nudge
	// every 7 days, matching the recall-stats window the store aggregates
	// over (recallStatsWindow). A memory recalled today reads ~1.0; one last
	// recalled a week ago reads ~0.5; beyond the window the store returns no
	// stat at all and the term is zero.
	recallRecencyHalfLife = 7 * 24 * time.Hour

	// recallNudgeWeight blends the frequency and recency halves of the
	// recall signal: weight*frequency + (1-weight)*recency, both in [0,1].
	// 0.6 leans slightly toward "how often" over "how recently" since the
	// store already gates events to a recency window.
	recallNudgeWeight = 0.6

	// recallBoostMax is the maximum MULTIPLICATIVE lift the recall term adds
	// in rerankHits: score *= (1 + recallBoostMax*signal), signal∈[0,1].
	// Sized so a maximally-recalled hit can overcome EXACTLY ONE adjacent
	// position step but never two — at the shallowest (tightest-relative-gap)
	// pair, base(1)*(1+0.02)=1/62*1.02 > base(0)=1/61, yet
	// base(2)*1.02=1/63*1.02 < base(0). So a frequently-recalled hit can
	// break a near-tie / climb a single rank, never leap a clearly-higher
	// one — the same "tie-breaker, not re-ranker" discipline as pinnedBoost,
	// only far gentler (pin is 1.5x; recall tops out at 1.02x). Crucially the
	// signal is 0 for a never-recalled memory / empty log, so the factor is
	// exactly 1.0 and the score is byte-identical to the pre-AR4 value.
	recallBoostMax = 0.02
)

// rerankHits re-scores an already ranked hit list with recency and pinned
// factors, then stable-sorts descending. The input order carries the
// retrieval relevance; position i maps to a position score 1/(rerankBaseK+i+1).
// On top of that we blend the raw retrieval signal magnitude (BM25 rank
// for FTS hits — sign-flipped because fts.rank is negative=better; and
// 1/(1+distance) for vector hits) normalized min-max within this hit
// list, so a BM25-strong hit edges out a BM25-weak hit at the same list
// position. Source tags are preserved and Score is replaced with the
// adjusted value.
//
// recall is the optional per-memory recall aggregate (AR4); pass nil (or an
// empty map, or a map with no entry for a hit) to contribute ZERO recall
// nudge — the function then reduces to its pre-AR4 behaviour exactly. The
// recall term is a BOUNDED multiplicative boost (1 + recallBoostMax*signal,
// signal∈[0,1]) sized to break a near-tie / climb a single adjacent rank,
// never leap a clearly-more-relevant hit. It rides alongside the recency/pin
// multipliers but is far gentler than either. See recallSignal + recallBoostMax.
func rerankHits(hits []store.MemoryHit, now time.Time, recall map[string]store.MemoryRecallStat) []store.MemoryHit {
	if len(hits) == 0 {
		return hits
	}
	out := make([]store.MemoryHit, len(hits))
	copy(out, hits)
	norm := normalizedSignals(out)
	for i := range out {
		pos := 1.0 / float64(rerankBaseK+i+1)
		// Keep the blended base in the same band as the pure position
		// score: posScale maps normSignal∈[0,1] onto [pos_last, pos_first]
		// so recency/pin multipliers retain their relative weight.
		base := rankBlendAlpha*pos + (1.0-rankBlendAlpha)*pos*norm[i]
		factor := recencyFactor(now, out[i].Entry.UpdatedAt)
		if out[i].Entry.Pinned {
			factor *= pinnedBoost
		}
		// recall boost: bounded multiplicative lift. 1.0 (no-op) when recall
		// has no entry for this id — the common case (empty log / tracking
		// off) → score byte-identical to the pre-AR4 value.
		factor *= 1.0 + recallBoostMax*recallSignal(recall, out[i].Entry.ID, now)
		out[i].Score = base * factor
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Score > out[b].Score
	})
	return out
}

// recallSignal maps a memory's recall aggregate to a bounded [0,1] nudge:
// a blend of saturating frequency (count/(count+recallSaturationN)) and
// recency (half-life decay of the last-recalled timestamp). Returns 0 when
// recall is nil, has no entry for id, or the entry carries no count — so a
// never-recalled memory (or an empty/off recall log) contributes nothing
// and ranking degrades to today's exact behaviour. Mirrors the tie-breaker
// discipline of foldRecencyPin: the value is multiplied by a sub-step
// epsilon at the call site, never used as a wide multiplier.
func recallSignal(recall map[string]store.MemoryRecallStat, id string, now time.Time) float64 {
	if len(recall) == 0 {
		return 0
	}
	st, ok := recall[id]
	if !ok || st.RecentCount <= 0 {
		return 0
	}
	freq := float64(st.RecentCount) / (float64(st.RecentCount) + recallSaturationN)
	recency := 0.0
	if !st.LastRecalledAt.IsZero() {
		age := now.Sub(st.LastRecalledAt)
		if age <= 0 {
			recency = 1.0
		} else {
			recency = math.Pow(0.5, age.Hours()/recallRecencyHalfLife.Hours())
		}
	}
	return recallNudgeWeight*freq + (1.0-recallNudgeWeight)*recency
}

// normalizedSignals min-max-normalizes the retrieval signal magnitude of
// each hit into [0,1] within the list, where higher always means better.
// Per Source:
//   - "fts"|"list": Score is fts.rank where more-negative is better, so the
//     signal is -Score.
//   - "vec": Score is the RAW vector distance where LOWER is better (the
//     store puts the distance, not a similarity, in vec hits). Convert to a
//     higher-is-better similarity via 1/(1+distance) so the min-max below
//     orders it correctly. (Latent today: rrfFuse overwrites Source to "rrf"
//     before rerankHits runs, so vec hits rarely reach here — but the
//     explicit case keeps the contract honest if a raw vec list is fed in.)
//   - default ("rrf"|...): an already-positive relevance score (RRF sum).
//
// When the list is signal-flat (all equal, or a single hit) every entry
// normalizes to 1.0 so the blend reduces to the pure position score.
func normalizedSignals(hits []store.MemoryHit) []float64 {
	n := len(hits)
	sig := make([]float64, n)
	for i, h := range hits {
		switch h.Source {
		case "fts", "list":
			sig[i] = -h.Score // fts.rank negative=better → flip
		case "vec":
			sig[i] = 1.0 / (1.0 + h.Score) // raw distance lower=better → similarity
		default:
			sig[i] = h.Score // rrf already positive=better
		}
	}
	minV, maxV := sig[0], sig[0]
	for _, v := range sig {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	out := make([]float64, n)
	for i, v := range sig {
		if span <= 0 {
			out[i] = 1.0
			continue
		}
		out[i] = (v - minV) / span
	}
	return out
}

// foldRecencyPin folds recency + pin onto an already-ordered hit list (the
// output of a cross-encoder rerank) as a BOUNDED tie-breaker, NOT a
// re-ranker. The cross-encoder relevance order is the PRIMARY key: each hit
// keeps its position base 1/(rerankBaseK+i+1), and recency + pin contribute
// only a small ADDITIVE nudge:
//
//	score(i) = base(i) + epsilon*(normalizedRecency(i) + pinBump(i))
//
// epsilon is sized (foldRecencyEpsilon) so the largest possible nudge —
// a pinned, just-updated hit, epsilon*(1+foldPinExtra) — is strictly
// SMALLER than the gap between any two adjacent position bases. The nudge
// can therefore only reorder hits whose cross-encoder ranks are a NEAR-TIE
// (separated by less than the nudge); a fresh-but-irrelevant doc can never
// leap a clearly-more-relevant one. Pinned hits get a slightly larger (but
// still sub-step) bump so they surface among near-equals without jumping
// far. normalizedRecency is recencyFactor mapped to [0,1] so the additive
// scale is independent of recencyFloor. Deterministic + stable-sorted, so
// genuine ties preserve the incoming (cross-encoder) order.
//
// Unlike rerankHits this does NOT blend the bi-encoder signal magnitude —
// the cross-encoder score IS the relevance signal here, so re-reading the
// stale RRF/vec Score would fight it.
//
// recall is the optional per-memory recall aggregate (AR4). Its [0,1]
// signal is added to the same sub-step nudge as recency/pin, so a
// frequently-recalled near-tie surfaces among equals without leaping a
// clearly-more-relevant cross-encoder hit. nil/empty recall (the common
// case) contributes zero — identical to the pre-AR4 fold. The combined
// nudge ceiling rises from (1+foldPinExtra) to (1+foldPinExtra+1); since
// foldRecencyEpsilon already divides by a safety margin, the worst-case
// nudge stays comfortably below one position step (asserted in tests).
func foldRecencyPin(hits []store.MemoryHit, now time.Time, recall map[string]store.MemoryRecallStat) []store.MemoryHit {
	if len(hits) == 0 {
		return hits
	}
	out := make([]store.MemoryHit, len(hits))
	copy(out, hits)
	epsilon := foldRecencyEpsilon()
	for i := range out {
		base := 1.0 / float64(rerankBaseK+i+1)
		// recencyFactor ∈ (recencyFloor, 1]; rescale to [0,1] so the nudge
		// magnitude doesn't depend on the floor.
		normRecency := (recencyFactor(now, out[i].Entry.UpdatedAt) - recencyFloor) /
			(1.0 - recencyFloor)
		nudge := normRecency
		if out[i].Entry.Pinned {
			nudge += foldPinExtra
		}
		nudge += recallSignal(recall, out[i].Entry.ID, now)
		out[i].Score = base + epsilon*nudge
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Score > out[b].Score
	})
	return out
}

// foldRecencyEpsilon returns the additive nudge weight for foldRecencyPin.
// It is the smallest gap between adjacent position bases over the worst-case
// pool depth (the gap shrinks with depth, so the deepest pair is the tightest
// bound), divided by the maximum possible nudge multiplier (1+foldPinExtra)
// and a safety margin so even a pinned, maximally-fresh hit nudges strictly
// LESS than one position step. This is what makes recency/pin a tie-breaker
// rather than a re-sort.
func foldRecencyEpsilon() float64 {
	// gap between base(foldPoolN-2) and base(foldPoolN-1) — the tightest
	// adjacent gap in the pool.
	i := foldPoolN - 2
	if i < 0 {
		i = 0
	}
	gap := 1.0/float64(rerankBaseK+i+1) - 1.0/float64(rerankBaseK+i+2)
	// 0.5 safety margin keeps the max nudge well under one full step.
	return gap * 0.5 / (1.0 + foldPinExtra)
}

// recencyFactor maps the age of updated_at to (recencyFloor, 1]: 1.0 for
// just-updated, halving every recencyHalfLife, floored.
func recencyFactor(now, updatedAt time.Time) float64 {
	age := now.Sub(updatedAt)
	if age <= 0 {
		return 1.0
	}
	decay := math.Pow(0.5, age.Hours()/recencyHalfLife.Hours())
	return recencyFloor + (1.0-recencyFloor)*decay
}

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
