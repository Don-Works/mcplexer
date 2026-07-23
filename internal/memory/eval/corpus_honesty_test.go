// corpus_honesty_test.go audits the generated corpus itself. A gate that fails
// because the fixtures were tuned to fail is worthless, and a gate that PASSES
// because the fixtures are trivially solvable is worse. These tests assert the
// structural properties that make the scaled result admissible evidence.
package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// minContestedPoolNoise is the number of NOISE documents that must reach the
// FTS candidate pool for every labeled query. The pool is 50 rows deep, so this
// is a real crowd, not a token presence check.
//
// Why this exists: an earlier revision let noise into the pool only through
// generic connectives, which meant four of the ten queries ("payment
// idempotency double charge bug", "task lease demote disconnect", …) had a pool
// of ONE — the gold answer alone. For those queries the "500-document saturated
// pool" was a fiction and the 0.90/0.85/0.85/0.80 floors were being cleared
// against no competition at all.
const minContestedPoolNoise = 20

// TestScaledCorpusIsHonest asserts the properties that make the scaled gate
// meaningful, in order of how badly their absence would mislead a reviewer.
func TestScaledCorpusIsHonest(t *testing.T) {
	now := time.Now().UTC()
	c := ScaledCorpus(now, 0)
	gold := GoldKeys(c)
	topical := TopicalVocabulary(c.Queries)

	if len(topical) < 20 {
		t.Fatalf("expected a rich discriminative vocabulary, got %d tokens", len(topical))
	}
	t.Logf("corpus: %d docs (%d gold, %d noise), %d discriminative tokens",
		len(c.Memories), len(gold), len(c.Memories)-len(gold), len(topical))

	assertNoiseIsNeverAnAnswer(t, c, gold, topical)
	assertAgeBandsBreakTheConfound(t, c, gold, now)
	assertEveryQueryIsOpposedFromBothBands(t, c, gold, now)
	assertCorpusIsDeterministic(t, now, c)
}

// assertNoiseIsNeverAnAnswer replaces the old "noise shares no discriminative
// token" invariant, which is false by design now that rivals graft query
// vocabulary in. The invariant that still holds — and is the one that actually
// matters — is that noise is never CORRECT:
//
//  1. no noise key appears in any query's RelevantKeys, so a noise document
//     surfacing at rank 1 always scores as a failure; and
//  2. no noise document carries more than ONE discriminative token, so it can
//     crowd the pool and win on age, but can never out-match a gold answer on
//     query-term coverage. Without (2) the corpus could quietly manufacture a
//     failure by making a distractor genuinely more relevant than the label.
func assertNoiseIsNeverAnAnswer(t *testing.T, c Corpus, gold, topical map[string]struct{}) {
	t.Helper()
	var noiseN, contested int
	for _, m := range c.Memories {
		if _, isGold := gold[m.Key]; isGold {
			continue
		}
		noiseN++
		hits := topicalTokensIn(m, topical)
		if len(hits) > 1 {
			t.Errorf("noise %q carries %d discriminative tokens %v — a distractor may share at "+
				"most one, otherwise it can out-match the gold answer on coverage and the "+
				"corpus is manufacturing the failure", m.Key, len(hits), hits)
		}
		if len(hits) == 1 {
			contested++
		}
	}
	if noiseN < 400 {
		t.Errorf("noise population %d is too small to saturate the retrieval pool", noiseN)
	}
	if contested < 10*minRivalsPerQuery {
		t.Errorf("only %d noise documents share query vocabulary; the pool would be unopposed "+
			"for the queries with no generic connectives in them", contested)
	}
	t.Logf("noise: %d docs, %d of them vocabulary-contested", noiseN, contested)
}

// assertAgeBandsBreakTheConfound is the fix for the total age/relevance
// confound the first revision had: 100% of gold was old and 100% of noise was
// fresh, so the degenerate heuristic "prefer old documents" scored a perfect
// 1.000 and the gate could not distinguish correctly-bounded recency from
// INVERTED recency. Gold must therefore straddle the age distribution.
func assertAgeBandsBreakTheConfound(t *testing.T, c Corpus, gold map[string]struct{}, now time.Time) {
	t.Helper()
	var freshGold, oldGold int
	for _, m := range c.Memories {
		if _, isGold := gold[m.Key]; !isGold {
			continue
		}
		age := now.Sub(m.UpdatedAt)
		switch {
		case age >= goldFreshMinDays*day && age <= goldFreshMaxDays*day:
			freshGold++
		case age >= goldOldMinDays*day && age <= goldOldMaxDays*day:
			oldGold++
		default:
			t.Errorf("gold %q age %s is in neither gold band", m.Key, age)
		}
	}
	if freshGold == 0 || oldGold == 0 {
		t.Fatalf("gold must straddle the age distribution, got %d fresh / %d old — with the "+
			"labels on one side only, an age-ordering heuristic solves the corpus and the "+
			"gate cannot tell a bounded recency term from an inverted one", freshGold, oldGold)
	}
	if diff := freshGold - oldGold; diff > 1 || diff < -1 {
		t.Errorf("gold age split is lopsided: %d fresh vs %d old", freshGold, oldGold)
	}
	t.Logf("gold age split: %d fresh (%d-%dd), %d old (%d-%dd)",
		freshGold, goldFreshMinDays, goldFreshMaxDays, oldGold, goldOldMinDays, goldOldMaxDays)
}

// assertEveryQueryIsOpposedFromBothBands proves the falsification property
// directly: for EVERY labeled query there is a vocabulary-sharing distractor
// strictly fresher than every gold document, and one strictly staler than every
// gold document. "Prefer the freshest" and "prefer the stalest" therefore both
// lose on every query — only relevance-first ranking survives.
func assertEveryQueryIsOpposedFromBothBands(t *testing.T, c Corpus, gold map[string]struct{}, now time.Time) {
	t.Helper()
	minGold, maxGold := goldAgeEnvelope(c, gold, now)
	byToken := map[string][2]int{} // token -> {fresher-than-gold, staler-than-gold}
	for _, m := range c.Memories {
		if _, isGold := gold[m.Key]; isGold {
			continue
		}
		hits := topicalTokensIn(m, TopicalVocabulary(c.Queries))
		if len(hits) != 1 {
			continue
		}
		age := now.Sub(m.UpdatedAt)
		cnt := byToken[hits[0]]
		if age < minGold {
			cnt[0]++
		} else if age > maxGold {
			cnt[1]++
		}
		byToken[hits[0]] = cnt
	}
	for _, q := range c.Queries {
		var fresher, staler int
		for _, tok := range discriminativeTokens(q.Query) {
			fresher += byToken[tok][0]
			staler += byToken[tok][1]
		}
		if fresher == 0 || staler == 0 {
			t.Errorf("query %q is opposed by %d fresher-than-gold and %d staler-than-gold "+
				"distractors; it needs at least one of EACH or a one-directional age "+
				"heuristic solves it", q.Query, fresher, staler)
		}
	}
}

// goldAgeEnvelope returns the min and max age across all gold documents.
func goldAgeEnvelope(c Corpus, gold map[string]struct{}, now time.Time) (time.Duration, time.Duration) {
	minAge, maxAge := time.Duration(1<<62), time.Duration(0)
	for _, m := range c.Memories {
		if _, isGold := gold[m.Key]; !isGold {
			continue
		}
		age := now.Sub(m.UpdatedAt)
		if age < minAge {
			minAge = age
		}
		if age > maxAge {
			maxAge = age
		}
	}
	return minAge, maxAge
}

// assertCorpusIsDeterministic hashes the WHOLE corpus. The previous check
// compared only len(Memories) and the last document's Content, which any
// reordering, retagging, or timestamp change would have slipped straight past.
func assertCorpusIsDeterministic(t *testing.T, now time.Time, c Corpus) {
	t.Helper()
	want := corpusFingerprint(c)
	for i := 0; i < 3; i++ {
		if got := corpusFingerprint(ScaledCorpus(now, 0)); got != want {
			t.Fatalf("ScaledCorpus is not deterministic: run %d fingerprint %s, want %s", i, got, want)
		}
	}
	// The digest covers absolute timestamps, so it is stable for a given `now`
	// and expected to differ between runs — the claim is same-input-same-output,
	// not a golden value.
	t.Logf("corpus fingerprint (for now=%s): %s", now.Format(time.RFC3339), want)
}

// corpusFingerprint hashes every field of every document plus the query
// contract, so any drift in content, tags, ordering, timestamps or labels
// changes the digest.
func corpusFingerprint(c Corpus) string {
	h := sha256.New()
	for _, m := range c.Memories {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%t\n",
			m.Key, m.Name, m.Content, strings.Join(m.Tags, "\x01"),
			m.UpdatedAt.UTC().UnixNano(), m.Pinned)
	}
	for _, q := range c.Queries {
		fmt.Fprintf(h, "%s\x00%s\n", q.Query, strings.Join(q.RelevantKeys, "\x01"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// topicalTokensIn returns the sorted, de-duplicated discriminative tokens a
// document exposes to FTS (name + content + tags are all indexed columns —
// see migration 058).
func topicalTokensIn(m FixtureMemory, topical map[string]struct{}) []string {
	seen := map[string]struct{}{}
	text := m.Name + " " + m.Content + " " + strings.Join(m.Tags, " ")
	for _, tok := range tokens(text) {
		if _, ok := topical[tok]; ok {
			seen[tok] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for tok := range seen {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// TestScaledCorpusPoolCensus proves the pool is genuinely contested for EVERY
// labeled query by asking the store, not the generator. It seeds the corpus and
// runs the same FTS5 search Recall runs (SearchMemories with a default filter,
// LIMIT 50), then counts how many of those 50 candidates are noise.
//
// Reviewers found the previous corpus produced ftsPool=1 for four of the ten
// queries. This census fails loudly if that ever recurs, and logs the full
// per-query breakdown so the number is on the record rather than asserted.
func TestScaledCorpusPoolCensus(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	c := ScaledCorpus(now, 0)
	gold := GoldKeys(c)

	h, err := NewHarness(ctx, filepath.Join(t.TempDir(), "census.db"), c)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	defer func() { _ = h.Close() }()

	unopposed := 0
	for _, q := range c.Queries {
		hits, err := h.Store.SearchMemories(ctx, store.MemoryFilter{}, q.Query)
		if err != nil {
			t.Fatalf("SearchMemories %q: %v", q.Query, err)
		}
		poolGold, poolNoise, goldPresent := censusPool(h, hits, gold, q)
		t.Logf("pool census q=%-46q size=%2d gold=%d noise=%d goldInPool=%t",
			q.Query, len(hits), poolGold, poolNoise, goldPresent)
		if !goldPresent {
			t.Errorf("query %q: the labeled answer never reaches the FTS pool — the corpus is "+
				"unsolvable, not hard", q.Query)
		}
		if poolNoise < minContestedPoolNoise {
			unopposed++
			t.Errorf("query %q: only %d noise documents reach the %d-row FTS pool (want >= %d). "+
				"The floors would be clearing an empty field for this query.",
				q.Query, poolNoise, len(hits), minContestedPoolNoise)
		}
	}
	t.Logf("pool census: %d/%d queries unopposed", unopposed, len(c.Queries))
}

// censusPool splits one FTS candidate pool into gold and noise, and reports
// whether the query's own labeled answer is in it.
func censusPool(h *Harness, hits []store.MemoryHit, gold map[string]struct{}, q FixtureQuery) (int, int, bool) {
	want := q.relevantSet()
	var nGold, nNoise int
	present := false
	for _, hit := range hits {
		key := h.idToKey[hit.Entry.ID]
		if _, isGold := gold[key]; isGold {
			nGold++
			if _, ok := want[key]; ok {
				present = true
			}
			continue
		}
		nNoise++
	}
	return nGold, nNoise, present
}
