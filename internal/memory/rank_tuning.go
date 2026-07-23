// rank_tuning.go — the ranking constants and the primitives they define.
// Split out of rank.go to keep each file under the repo's 300-line cap; the
// arithmetic that ties these numbers together lives in the comments here and
// is asserted in rank_internal_test.go.
package memory

import (
	"math"
	"time"
)

const (
	// minSaveContentChars is the floor for Service.Write content length
	// after trimming unless the save is pinned.
	minSaveContentChars = 8

	// recencyHalfLife halves a hit's recency factor every 30 days.
	recencyHalfLife = 30 * 24 * time.Hour

	// recencyFloor is the asymptote of recencyFactor: the factor decays
	// toward this instead of zero. NOTE both ranking paths now consume
	// recencyFactor through a [0,1] rescale ((f-floor)/(1-floor)), so the
	// floor CANCELS out of ranking arithmetic entirely — it survives only
	// as the documented range of recencyFactor itself (and for any caller
	// that wants a raw "how fresh is this" multiplier). Changing it no
	// longer changes any ordering.
	recencyFloor = 0.05

	// rerankBaseK is the RRF-style smoothing constant used to convert a
	// list position into a base relevance score: base(i) = 1/(k+i+1).
	// Matches the k=60 used by rrfFuse so the scales line up.
	rerankBaseK = 60

	// The rerankHits tie-breaker budgets, expressed in ADJACENT POSITION STEPS.
	// Each nudge term is worth at most its own budget in steps of climb at full
	// signal, and the budgets are deliberately UNEQUAL — see each constant.
	//
	// Why steps and not a multiplier — the bug this replaces. rerankHits used
	// to compute score = base * recencyFactor with recencyFactor ∈ (0.05,1],
	// i.e. a 20x dynamic range multiplied onto a base whose ENTIRE spread
	// across the pool is ~1.5x (position bases 1/61..1/81 at the production
	// pool depth of k*2 = 20, times the ≤1.176x signal blend). Recency
	// therefore had more authority than relevance and could drag a
	// maximally-fresh, LEAST-relevant hit from the bottom of the pool to #1.
	// That failure is scale-dependent: it needs a pool saturated with fresh
	// distractors, which is exactly what a large store produces.
	//
	// The replacement is additive and sized against the LOCAL gap between
	// adjacent position bases, step(i) = base(i) - base(i+1).
	//
	// THE INVARIANT, and why it is depth-INDEPENDENT. A hit at rank j can
	// overtake a better hit at rank i<j only if the gap between them is
	// smaller than j's nudge. The gap is sum_{m=i}^{j-1} step(m), and step is
	// monotonically decreasing, so that sum is ≥ (j-i)*step(j). A nudge of
	// c*step(j) therefore requires (j-i)*step(j) < c*step(j), i.e. j-i < c.
	//
	//   A term with a budget of c steps climbs AT MOST c ranks, at ANY depth,
	//   in a pool of ANY size.
	//
	// c is an UPPER bound; the realized climb is slightly smaller because
	// steps shrink with depth. Climbing m ranks from rank i needs
	// c > (step(i)+..+step(i+m-1))/step(i+m), which at the head of the pool is
	// 1.033 for one rank, 2.098 for two and 3.197 for three. So the realized
	// climbs are: 2.3 steps -> 2 ranks, 2.0 steps -> 1 rank. Both the bound and
	// the realized figures are asserted in rank_internal_test.go; quote the
	// realized number when describing behaviour, the bound when reasoning
	// about safety.
	//
	// This is the honest statement of the bound. An earlier revision of this
	// comment compared the nudge against the pool's absolute score spread,
	// which is NOT depth-independent (the spread shrinks as the pool shortens,
	// so that phrasing is false for pools of ≲6 hits) and which overstated the
	// combined budget as "one rank". The per-rank formulation above holds
	// everywhere; TestRerankHitsClimbBoundHoldsAtEveryDepth asserts it across
	// pool depths 1..100.

	// rankRecencySteps is recency's budget. 3.0 rather than ~1 because a
	// budget of c steps cannot express any preference finer than 1/c of its
	// range: at c=1.6 a term needed signal > 0.625 to move a single rank,
	// which for the old exponential curve meant recency had NO ordering effect
	// beyond ~19 days — a three-week cliff on a store holding months of
	// memories. That is over-correction: the first fix traded "recency
	// outranks relevance" for "recency is a binary is-it-newer-than-19-days
	// flag". 2.3 steps (2 realized ranks) plus the log-scaled rankRecency curve
	// grades freshness continuously from minutes to a year (see rankRecency).
	rankRecencySteps = 2.3

	// rankPinSteps is the pin budget. Pin is the user's explicit "this matters
	// more" affordance (memory__pin) and the only importance signal the system
	// takes directly from a human, so it gets authority equal to recency's.
	// Sizing history worth keeping: the pre-fix pinnedBoost was a 1.5x
	// MULTIPLIER, which satisfied 1.5/(61+i) > 1/61 for i < 30.5 — a pinned hit
	// reached #1 from anywhere in a ≤30-deep pool regardless of relevance. That
	// is too strong (pin should not beat relevance outright). The first fix
	// then made pin a 1.0-step term worth a single rank, which is too weak —
	// it made an explicit user signal a rounding error. A 2.3-step budget
	// (2 realized ranks) is the middle: a pinned memory lifts clear of its
	// near-peers, and still cannot drag a clearly-irrelevant hit to the top of
	// a saturated pool. Pin is deliberately still weaker than relevance — the
	// affordance is "weight this up", not "override the ranker".
	rankPinSteps = 2.3

	// rankRecallSteps is the co-recall budget. 2.0 steps, but only 1 realized
	// rank — the extra headroom is not extra authority, it is what the term
	// needs to reach its FIRST rank against realistic signal.
	//
	// Sizing this against the theoretical maximum signal is a trap I walked
	// into: one rank of climb costs 1.033 steps of budget, but the climbing
	// hit must ALSO overcome the incumbent's larger recency nudge (nudges scale
	// by each hit's own position step, and step shrinks with depth, so at equal
	// freshness the higher-ranked hit is nudged slightly harder). The real
	// threshold is C*s > 1.108, so a budget of 1.2 would have required
	// recallSignal > 0.92 — a value the saturating curve barely reaches, making
	// the term dead in practice while looking alive on paper. At 2.0 the term
	// engages from s > 0.554, which ordinary "recalled a few times recently"
	// traffic actually produces. Budgets must be sized against the signal
	// values that OCCUR, not the ones that are representable.
	rankRecallSteps = 2.0

	// rerankMaxNudgeSteps is the worst-case COMBINED climb: a maximally-fresh,
	// pinned, frequently-recalled hit versus an ancient, unpinned,
	// never-recalled one. 6.6 steps, realizing 5 ranks — reachable only when
	// every available importance signal agrees, which describes a memory the
	// user pinned, just wrote, AND keeps recalling.
	//
	// The 5-rank ceiling is load-bearing, not incidental:
	// TestRerankHitsRecencyIsBoundedNotDominant places a maxed-out climber SIX
	// ranks below a stale-but-most-relevant hit and requires the stale hit to
	// hold #1. A combined budget of 7.0+ realizes 6 ranks and breaks it. Raise
	// these budgets and that test is the thing that will tell you.
	//
	// Relevance still outranks the tie-breakers even at full stack: the
	// signal-magnitude blend (rankBlendAlpha) is worth
	// (1-alpha)/(k+1) = 9.30 steps at the head of the pool, and 6.6 < 9.3.
	// That margin is the safety property TestRerankHitsNudgeBudgetArithmetic
	// guards — if a future budget increase erases it, freshness starts
	// outvoting BM25/vector relevance and the original bug returns.
	rerankMaxNudgeSteps = rankRecencySteps + rankPinSteps + rankRecallSteps

	// foldPoolN is the worst-case pool depth foldRecencyPin sizes its
	// tie-breaker nudge against. Recall caps the cross-encoder pool at k*2;
	// k is 20 only for callers that pass k<=0 — the memory__recall tool
	// defaults k to 10 (internal/gateway/handler_memory.go), so the real
	// depth is usually 20, not 40. Keeping 40 here is deliberate and safe:
	// positionStep shrinks monotonically with depth, so sizing epsilon
	// against the gap at the DEEPEST position guarantees the nudge stays
	// sub-gap everywhere shallower too. See foldRecencyEpsilon.
	//
	// NOTE this constant bounds ONLY the cross-encoder fold. rerankHits (the
	// path production actually runs, since MCPLEXER_RERANK_BASE_URL is
	// normally unset) scales its nudge by the hit's OWN positionStep, so it
	// needs no pool-depth assumption at all.
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
	// position scoring lives in — the signal magnitude edges hits that landed
	// at (or near) the same list position, without letting one arm's raw score
	// scale swamp the fused order. Its authority is real and deliberately
	// LARGER than the tie-breakers': at pos≈1/61 the full signal span is
	// 0.15/61 ≈ 9.3 position steps, versus the 1.6-step recency/pin/recall
	// budget. Relevance magnitude outranks freshness; that ordering of
	// authorities is the whole point.
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

	// (The recall term used to carry its own multiplicative ceiling,
	// recallBoostMax = 0.02, sized to climb exactly one adjacent position
	// step. It now shares the additive rerankNudgeSteps budget with recency
	// and pin, which encodes the same "one rank, never two" intent uniformly
	// across all three tie-breakers instead of once per term. The zero-signal
	// degradation is unchanged: recallSignal returns 0 for a never-recalled
	// memory / empty log / tracking-off, so the nudge is exactly 0.)
)

// positionStep is the gap between the position bases of ranks i and i+1,
// 1/(k+i+1) - 1/(k+i+2). It is the natural unit for "one rank of climb" and
// shrinks with depth, so any tie-breaker sized against it keeps a constant
// authority measured in ranks rather than in score.
func positionStep(i int) float64 {
	if i < 0 {
		i = 0
	}
	return 1.0/float64(rerankBaseK+i+1) - 1.0/float64(rerankBaseK+i+2)
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

// normRecency rescales recencyFactor's (recencyFloor,1] range onto [0,1] so
// the additive nudge magnitude is independent of recencyFloor. 1.0 = updated
// now, 0.5 ~ one half-life old, -> 0 for ancient rows.
//
// Used by foldRecencyPin (the cross-encoder fold), which wants a sharp
// freshness signal because its whole nudge is sub-step by construction.
// rerankHits uses rankRecency instead — see there for why.
func normRecency(now, updatedAt time.Time) float64 {
	return (recencyFactor(now, updatedAt) - recencyFloor) / (1.0 - recencyFloor)
}

const (
	// rankRecencyTau sets where rankRecency's log curve is most sensitive.
	// At one day, the curve separates "this hour" from "yesterday" from "last
	// week" while still distinguishing "last quarter" from "last year".
	rankRecencyTau = 24 * time.Hour

	// rankRecencyCap is the age at which rankRecency reaches 0. A memory older
	// than this contributes no freshness nudge at all; it is not penalised
	// further, it simply stops competing on recency.
	rankRecencyCap = 365 * 24 * time.Hour
)

// rankRecency maps age onto [0,1] for the rerankHits nudge, LOGARITHMICALLY
// in age rather than by exponential half-life decay.
//
// Why not reuse normRecency. Exponential decay collapses: with a 30-day
// half-life, everything past ~2 half-lives sits in the bottom few percent of
// the range, so a 60-day-old and a 400-day-old memory are indistinguishable.
// Paired with a finite step budget that makes recency a cliff — the exact
// over-correction the first fix introduced (see rankRecencySteps). The
// underlying prior — "a more recently written memory is more likely to still
// be true" — is roughly logarithmic in age, not exponential: the difference
// between 1 day and 1 week is meaningful, and so is the difference between 3
// months and a year, but they are not the SAME size of meaningful.
//
// The curve, with tau = rankRecencyTau and cap = rankRecencyCap:
//
//	rankRecency(age) = 1 - ln(1 + age/tau) / ln(1 + cap/tau)
//
// which yields, at the 3.0-step rankRecencySteps budget:
//
//	age     value   ranks of climb
//	now     1.000   3.00
//	1d      0.883   2.65
//	7d      0.648   1.94
//	30d     0.418   1.25
//	90d     0.235   0.71
//	365d    0.000   0.00
//
// Every adjacent pair differs by a visible fraction of a rank, so freshness
// grades continuously instead of switching off at three weeks. Future-dated
// rows (clock skew) clamp to 1.0; ages beyond the cap clamp to 0.
func rankRecency(now, updatedAt time.Time) float64 {
	age := now.Sub(updatedAt)
	if age <= 0 {
		return 1.0
	}
	if age >= rankRecencyCap {
		return 0.0
	}
	tau := float64(rankRecencyTau)
	v := 1.0 - math.Log1p(float64(age)/tau)/math.Log1p(float64(rankRecencyCap)/tau)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
