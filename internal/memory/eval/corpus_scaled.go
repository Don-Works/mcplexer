package eval

import (
	"time"
)

// corpus_scaled.go builds a PRODUCTION-SHAPED corpus programmatically.
//
// Why the small DefaultCorpus cannot see the live ranking failure:
//
//  1. 10 documents never saturate the rerank pool (50 deep on the FTS-only
//     path, k*2 on the fused path), so ranking never has to choose between a
//     relevant document and a wall of near-miss candidates.
//  2. Every fixture is seeded at test time, so UpdatedAt ≈ now for all rows and
//     recencyFactor() returns 1.0 uniformly — the recency multiplier in
//     rerankHits is mathematically inert.
//
// ScaledCorpus fixes both while deliberately REFUSING to correlate age with
// relevance. An earlier revision aged 100% of the gold documents and kept 100%
// of the noise fresh; that shape is reproducible but it is also solvable by the
// degenerate heuristic "prefer old documents", so the gate could not tell a
// correctly-bounded recency term from an INVERTED one. Both now fail:
//
//	band            age              population
//	--------------  ---------------  ------------------------------------------
//	rival-fresh     0–47h            distractors, strictly fresher than any gold
//	gold-fresh      5–9d             half the labeled answers
//	gold-old        45–90d           the other half of the labeled answers
//	rival-old       120–200d         distractors, strictly older than any gold
//
// The four bands do not overlap, and every labeled query gets distractors in
// BOTH rival bands. "Prefer the freshest" therefore loses on the gold-fresh
// queries' fresh rivals and on every gold-old query; "prefer the stalest" loses
// symmetrically. Only ranking that puts relevance first survives.
//
// The noise is also given teeth: corpus_noise.go generates per-query "rivals"
// that share exactly ONE discriminative token with a labeled query while
// remaining semantically wrong answers, so every query is contested inside the
// FTS pool. Real production distractors share vocabulary — a noise population
// that shares none is unopposed, and the strong floors here would then be
// clearing an empty field.
//
// Everything here is neutral synthetic content — no real people, companies,
// hostnames or addresses (public repo).
const (
	// The four non-overlapping age bands. Expressed in the smallest unit each
	// band needs; spanAge converts. Keep them disjoint — the honesty test
	// asserts the gaps, because the "prefer old"/"prefer new" falsification
	// depends on every rival being strictly outside the gold envelope.
	rivalFreshMaxHours = 48 // [0h, 48h)
	goldFreshMinDays   = 5
	goldFreshMaxDays   = 9
	goldOldMinDays     = 45
	goldOldMaxDays     = 90
	rivalOldMinDays    = 120
	rivalOldMaxDays    = 200

	// defaultNoiseCount is sized so the FTS-only pool (50) and the fused pool
	// (k*2) are both saturated many times over for every probe.
	defaultNoiseCount = 490

	// minRivalsPerQuery guarantees each labeled query gets at least one
	// fresher-than-gold and one staler-than-gold distractor.
	minRivalsPerQuery = 2
)

// ScaledCorpus returns a production-shaped corpus: the DefaultCorpus gold
// documents split across the two gold age bands, per-query vocabulary-sharing
// rivals split across the two rival age bands, and a background population of
// topically-inert documents spread over all four. Pass noiseCount <= 0 for the
// default. Generation is fully deterministic — same now, same corpus.
func ScaledCorpus(now time.Time, noiseCount int) Corpus {
	if noiseCount <= 0 {
		noiseCount = defaultNoiseCount
	}
	base := DefaultCorpus()
	rivals := rivalNoise(base.Queries, now, rivalsPerQuery(noiseCount, len(base.Queries)))
	background := noiseCount - len(rivals)
	if background < 0 {
		background = 0
	}
	out := Corpus{
		Memories: make([]FixtureMemory, 0, len(base.Memories)+len(rivals)+background),
		Queries:  base.Queries,
	}
	out.Memories = append(out.Memories, agedGold(base.Memories, now)...)
	out.Memories = append(out.Memories, rivals...)
	out.Memories = append(out.Memories, backgroundNoise(now, background)...)
	return out
}

// rivalsPerQuery splits the noise budget roughly half into contested rivals
// and half into topically-inert background, floored so every query is still
// contested from both age bands even at a small noise budget.
func rivalsPerQuery(noiseCount, numQueries int) int {
	if numQueries <= 0 {
		return 0
	}
	per := noiseCount / (2 * numQueries)
	if per < minRivalsPerQuery {
		per = minRivalsPerQuery
	}
	return per
}

// agedGold splits the labeled documents between the gold-fresh and gold-old
// bands by index parity, spreading each half across its band. The split is the
// point: with gold on both sides of the age distribution, no age-ordering
// heuristic (in either direction) can recover the labels, so the gate measures
// relevance ranking and nothing else. The spread within each band keeps it
// honest at the edges — a fix must recover the 90-day-old answers too.
func agedGold(golds []FixtureMemory, now time.Time) []FixtureMemory {
	out := make([]FixtureMemory, len(golds))
	var nOld, nFresh int
	for i, g := range golds {
		if i%2 == 0 {
			g.UpdatedAt = now.Add(-spanAge(goldOldMinDays, goldOldMaxDays, nOld, day))
			nOld++
		} else {
			g.UpdatedAt = now.Add(-spanAge(goldFreshMinDays, goldFreshMaxDays, nFresh, day))
			nFresh++
		}
		out[i] = g
	}
	return out
}

const day = 24 * time.Hour

// spanAge spreads document i deterministically across the inclusive band
// [minUnits, maxUnits] measured in unit. The stride of 7 is coprime with every
// band width used here, so consecutive documents land at different ages
// instead of clumping at the band floor.
func spanAge(minUnits, maxUnits, i int, unit time.Duration) time.Duration {
	span := maxUnits - minUnits + 1
	if span < 1 {
		span = 1
	}
	if i < 0 {
		i = -i
	}
	return time.Duration(minUnits+(i*7)%span) * unit
}

// FlattenAges returns a copy of c with every document stamped at the same
// instant. It is the CONTROL for the scaled scenarios: identical documents,
// identical pool saturation, identical FTS/vector behaviour — the only removed
// variable is the age spread, which makes recencyFactor uniform and therefore
// inert. Any divergence between the aged run and this control — in EITHER
// direction — is attributable to the recency term and nothing else.
func FlattenAges(c Corpus, at time.Time) Corpus {
	out := Corpus{
		Memories: make([]FixtureMemory, len(c.Memories)),
		Queries:  c.Queries,
	}
	copy(out.Memories, c.Memories)
	for i := range out.Memories {
		out.Memories[i].UpdatedAt = at
	}
	return out
}

// GoldKeys returns the labeled (non-noise) keys of a corpus — the documents a
// working retriever must surface. Useful for diagnostics and for asserting the
// noise population never leaks into the relevance labels.
func GoldKeys(c Corpus) map[string]struct{} {
	out := make(map[string]struct{})
	for _, q := range c.Queries {
		for _, k := range q.RelevantKeys {
			out[k] = struct{}{}
		}
	}
	return out
}
