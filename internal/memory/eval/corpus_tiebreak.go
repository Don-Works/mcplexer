package eval

import (
	"fmt"
	"time"
)

// corpus_tiebreak.go builds the probe corpus that pins the recency term's SIGN.
//
// Why the scaled corpus cannot do it. ScaledCorpus proves recency does not
// OUTRANK relevance, and it is deliberately built so no age heuristic can
// recover the labels. But once recency is correctly bounded to ~1.6 adjacent
// position steps, flipping its sign barely moves a corpus whose gold documents
// beat their distractors by a wide relevance margin: an inverted rerankHits
// scores the scaled FTS arm at recall/nDCG/MRR/P@1 = 1.000, exactly like the
// correct one. A bounded term is only observable where the relevance margin is
// of the same order as the term itself — i.e. at a tie.
//
// So this corpus manufactures ties, and only ties:
//
//	DIRECTION probes  two documents with byte-identical name, content and tags,
//	                  differing ONLY in UpdatedAt. BM25 cannot separate them, so
//	                  the tie-breaker alone decides the order. The fresher copy
//	                  must win — that is the other half of the ranking contract
//	                  ("between two hits of equal relevance the fresher one
//	                  wins"), and an inverted recency term fails every probe.
//	MARGIN probes     a stale document matching BOTH query terms against a fresh
//	                  one matching only the first. The relevance gap is one
//	                  clear rank, which is more than the tie-breaker's budget, so
//	                  the stale-but-relevant document must still win. This is the
//	                  bound read at the sharp end: it fails the moment recency
//	                  grows enough authority to cross a real relevance gap.
//
// Together the two families pin the term from both sides: strong enough to
// order a tie, too weak to cross a rank. Probe vocabulary is synthetic and
// unique per probe, so each probe's candidate pool is exactly its own two
// documents and no probe can interfere with another.

const (
	// tieProbePairs / marginProbePairs keep the probe corpus tiny — it is a
	// scalpel, not a saturation test, and it runs in well under a second.
	tieProbePairs    = 4
	marginProbePairs = 4

	// The stale side of every probe sits six recency half-lives back, so
	// normRecency reads ~0.016 against ~1.0 for the fresh side: the largest
	// signal difference the term can express.
	probeStaleDays   = 180
	probeFreshMinute = 1
)

// TieBreakProbe is one labeled ordering claim: for Query, the document keyed
// WantFirst must rank above the one keyed WantSecond.
type TieBreakProbe struct {
	Query      string
	WantFirst  string
	WantSecond string
	// Why explains the claim so a failure names the contract it broke rather
	// than just two keys.
	Why string
}

// TieBreakCorpus returns the probe corpus and the ordering claims it supports.
// The Corpus.Queries field carries the same probes in labeled form so the
// standard harness can seed and score it; RelevantKeys names the document that
// must come first.
func TieBreakCorpus(now time.Time) (Corpus, []TieBreakProbe) {
	var (
		mems   []FixtureMemory
		probes []TieBreakProbe
	)
	for i := 0; i < tieProbePairs; i++ {
		m, p := directionProbe(now, i)
		mems = append(mems, m...)
		probes = append(probes, p)
	}
	for i := 0; i < marginProbePairs; i++ {
		m, p := marginProbe(now, i)
		mems = append(mems, m...)
		probes = append(probes, p)
	}
	c := Corpus{Memories: mems}
	for _, p := range probes {
		c.Queries = append(c.Queries, FixtureQuery{
			Query: p.Query, RelevantKeys: []string{p.WantFirst},
		})
	}
	return c, probes
}

// directionProbe emits two documents that are identical in every FTS-visible
// field and differ only in age.
func directionProbe(now time.Time, i int) ([]FixtureMemory, TieBreakProbe) {
	tok := fmt.Sprintf("tieprobe%02d", i)
	name := fmt.Sprintf("tie-note-%02d", i)
	body := fmt.Sprintf("Duplicate note covering %s. These two copies are word for word identical.", tok)
	tags := []string{"probe", "duplicate"}
	fresh := FixtureMemory{
		Key: fmt.Sprintf("tie-fresh-%02d", i), Name: name, Content: body, Tags: tags,
		UpdatedAt: now.Add(-probeFreshMinute * time.Minute),
	}
	stale := FixtureMemory{
		Key: fmt.Sprintf("tie-stale-%02d", i), Name: name, Content: body, Tags: tags,
		UpdatedAt: now.Add(-probeStaleDays * day),
	}
	// Seed the stale copy FIRST so the store returns it ahead of the fresh one
	// on any BM25 tie: the probe then only passes if the tie-breaker actively
	// promotes the fresh copy, never because the incoming order already agreed.
	return []FixtureMemory{stale, fresh}, TieBreakProbe{
		Query:      tok,
		WantFirst:  fresh.Key,
		WantSecond: stale.Key,
		Why: "two documents identical in every indexed field must be ordered by freshness; " +
			"the stale copy winning means the recency term is pointed the wrong way",
	}
}

// marginProbe emits a stale two-term match against a fresh one-term match.
func marginProbe(now time.Time, i int) ([]FixtureMemory, TieBreakProbe) {
	head := fmt.Sprintf("marginprobe%02d", i)
	tail := fmt.Sprintf("marginextra%02d", i)
	answer := FixtureMemory{
		Key:  fmt.Sprintf("margin-answer-%02d", i),
		Name: fmt.Sprintf("margin-answer-%02d", i),
		Content: fmt.Sprintf("Reference note on %s and %s, written up in full at the time.",
			head, tail),
		Tags:      []string{"probe", "answer"},
		UpdatedAt: now.Add(-probeStaleDays * day),
	}
	distractor := FixtureMemory{
		Key:       fmt.Sprintf("margin-fresh-%02d", i),
		Name:      fmt.Sprintf("margin-fresh-%02d", i),
		Content:   fmt.Sprintf("Passing remark about %s, filed today with nothing settled.", head),
		Tags:      []string{"probe", "distractor"},
		UpdatedAt: now.Add(-probeFreshMinute * time.Minute),
	}
	return []FixtureMemory{answer, distractor}, TieBreakProbe{
		Query:      head + " " + tail,
		WantFirst:  answer.Key,
		WantSecond: distractor.Key,
		Why: "a stale document matching both query terms must outrank a fresh one matching " +
			"only the first; freshness may break a tie, never cross a rank of relevance",
	}
}
