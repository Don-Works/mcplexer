// rank.go — the two terminal ranking passes. rerankHits is the production
// path (no cross-encoder configured); foldRecencyPin is the cross-encoder
// path. Both now share one discipline: relevance orders the list, recency /
// pin / recall are bounded tie-breakers sized in position steps. The
// constants and primitives live in rank_tuning.go, MMR in rank_mmr.go.
package memory

import (
	"math"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// rerankHits re-scores an already ranked hit list, then stable-sorts
// descending. RELEVANCE IS THE PRIMARY KEY; recency, pin and recall are
// BOUNDED ADDITIVE TIE-BREAKERS — the same discipline foldRecencyPin uses on
// the cross-encoder path, which until now was the only path that had it.
//
// Relevance comes from two sources, both preserved from the previous design:
//
//	pos(i)  = 1/(rerankBaseK+i+1)             — the incoming list order
//	base(i) = pos(i) * (alpha + (1-alpha)*norm(i))
//
// where norm(i) is the raw retrieval signal magnitude min-max normalized
// within this list (BM25 rank for FTS hits, sign-flipped because fts.rank is
// negative=better; 1/(1+distance) for vector hits; the RRF sum for fused
// hits). Blending the magnitude in is NOT the bug and is kept: it lets a
// BM25-strong hit edge out a BM25-weak hit that landed at a similar position.
//
// On top of base, the three tie-breakers contribute an additive nudge scaled
// by the LOCAL adjacent-position gap:
//
//	nudge(i) = normRecency(i) + pinned(i) + recallSignal(i)      each ∈ [0,1]
//	score(i) = base(i) + rerankNudgeSteps * positionStep(i) * nudge(i)
//
// Scaling by positionStep(i) rather than a constant is what makes the budget
// depth-independent: base gaps shrink as 1/i², so a fixed epsilon would be a
// gentle nudge at the head of the pool and a re-sort at its tail — precisely
// the regime a large store spends all its time in.
//
// THE INVARIANT, stated as a rank bound because that is the form that is
// actually true at every depth: a term with a budget of c position steps
// climbs AT MOST c ranks, in a pool of any size. Recency is worth 3 ranks,
// pin 3, recall 1, so the worst case — every importance signal maxed at once
// — is rerankMaxNudgeSteps = 7 ranks. rank_tuning.go derives the bound;
// TestRerankHitsClimbBoundHoldsAtEveryDepth asserts it over pool depths
// 1..100. (An earlier revision stated this as "the nudge is smaller than the
// pool's relevance spread", which is not depth-independent and is false for
// pools of ≲6 hits.)
//
// What this replaces: score = base * recencyFactor * pinnedBoost * (1+recall),
// where recencyFactor alone spanned 20x against a base spanning ~1.5x across
// the pool. Recency outranked relevance, so a fresh irrelevant hit could win
// from anywhere in the pool. Freshness still MATTERS — between two hits of
// equal relevance the fresher one wins, and a stale belief still loses to a
// fresher one sitting next to it — it just cannot outvote relevance any more.
//
// recall is the optional per-memory recall aggregate (AR4); nil, an empty
// map, or a map with no entry for a hit contributes exactly ZERO.
func rerankHits(hits []store.MemoryHit, now time.Time, recall map[string]store.MemoryRecallStat) []store.MemoryHit {
	if len(hits) == 0 {
		return hits
	}
	out := make([]store.MemoryHit, len(hits))
	copy(out, hits)
	norm := normalizedSignals(out)
	for i := range out {
		pos := 1.0 / float64(rerankBaseK+i+1)
		base := rankBlendAlpha*pos + (1.0-rankBlendAlpha)*pos*norm[i]
		// Each term carries its OWN budget in position steps (rank_tuning.go
		// explains why they differ: pin is a human decision, recency an
		// authored fact, recall an inferred statistic). Accumulate the budget
		// in steps, then scale by the local step once — scaling by the hit's
		// own step is what makes the rank bound depth-independent.
		steps := rankRecencySteps * rankRecency(now, out[i].Entry.UpdatedAt)
		if out[i].Entry.Pinned {
			steps += rankPinSteps
		}
		steps += rankRecallSteps * recallSignal(recall, out[i].Entry.ID, now)
		out[i].Score = base + steps*positionStep(i)
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
// and ranking degrades to today's exact behaviour. Both call sites multiply
// this value by a bounded, position-step-sized epsilon — never by a wide
// multiplier — so it can only ever break a near-tie.
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
			// Guard the pole at distance == -1: 1/(1+d) is ±Inf there, and a
			// single Inf poisons span, defeats the `span <= 0` check below
			// (every comparison against NaN is false) and turns EVERY hit's
			// score into NaN — silently randomising the whole result set. A
			// distance ≤ -1 is not physically meaningful for any metric the
			// store uses, so treat it as maximally similar rather than
			// propagating a pole.
			if d := 1.0 + h.Score; d > 0 {
				sig[i] = 1.0 / d // raw distance lower=better → similarity
			} else {
				sig[i] = math.MaxFloat64
			}
		default:
			sig[i] = h.Score // rrf already positive=better
		}
		// Belt and braces: a NaN reaching the min/max scan would make both
		// bounds NaN and NaN-poison every output.
		if math.IsNaN(sig[i]) {
			sig[i] = 0
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
		// !(span > 0) rather than span <= 0 so a non-finite span (Inf/NaN
		// survivor) also falls back to the flat-signal case instead of
		// producing NaN scores.
		if !(span > 0) || math.IsInf(span, 0) {
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
		nudge := normRecency(now, out[i].Entry.UpdatedAt)
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
	gap := positionStep(foldPoolN - 2)
	// 0.5 safety margin keeps the max nudge well under one full step.
	return gap * 0.5 / (1.0 + foldPinExtra)
}
