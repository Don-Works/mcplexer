// recency_direction_test.go pins the SIGN and the MAGNITUDE of the recency
// tie-breaker, the two properties the scaled gate cannot see on its own.
//
// TestScaledRetrieval proves recency does not outrank relevance. It cannot
// prove recency points the right way: once the term is bounded to ~1.6 adjacent
// position steps, an INVERTED rerankHits scores the scaled FTS arm at
// recall/nDCG/MRR/P@1 = 1.000 — identical to the correct one — because every
// gold document beats its distractors by far more than the tie-breaker's
// budget. Measured, not assumed: that is the observed result of running the
// scaled gate against a mutant whose recency term is negated.
//
// A bounded term is only observable at a tie, so these probes are ties. See
// corpus_tiebreak.go for the construction.
package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// probeK is deliberately small: every probe's candidate pool is exactly its own
// two documents, so anything above 2 only adds noise to the diagnostics.
const probeK = 5

// TestRecencyTieBreakDirection asserts both halves of the recency contract on
// the production (no cross-encoder) path:
//
//	DIRECTION  identical documents order fresh-first. Fails on an inverted term.
//	MARGIN     a stale two-term match outranks a fresh one-term match. Fails the
//	           moment recency grows enough authority to cross a rank.
func TestRecencyTieBreakDirection(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	corpus, probes := TieBreakCorpus(now)

	h, err := NewHarness(ctx, filepath.Join(t.TempDir(), "tiebreak.db"), corpus)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	defer func() { _ = h.Close() }()

	runProbes(t, ctx, h, probes)

	// Same claim as an aggregate, so a failure also reports a headline number.
	// Every probe labels exactly one correct answer, so P@1 must be a clean
	// 1.000 — there is no ranking jitter to absorb here, only the tie-breaker.
	results, _, err := h.RunQueries(ctx, corpus, probeK)
	if err != nil {
		t.Fatalf("RunQueries: %v", err)
	}
	m := Aggregate(results, probeK)
	t.Logf("tie-break probes: p@1=%.3f mrr=%.3f over %d probes", m.PrecisionAt1, m.MRR, m.NumQueries)
	if m.PrecisionAt1 < 1.0 {
		t.Errorf("tie-break P@1: got %.3f, want 1.000 — every probe is a two-document pool with "+
			"one correct answer, so anything below 1.000 is a tie-breaker defect", m.PrecisionAt1)
	}
}

// TestRecencyTieBreakWithCrossEncoder runs the same probes through the
// cross-encoder branch (crossEncoderReorder → foldRecencyPin), which
// memory.NewService leaves unreachable by default because it always installs
// NoopReranker. Harness.UseReranker is the seam.
//
// An embedder is required as well as a reranker: Recall returns early on the
// FTS-only fallback when the embed provider has no model, so the cross-encoder
// branch is downstream of a successful embed and a reranker alone never reaches
// it.
//
// The MARGIN claim is the load-bearing one here: it asserts the cross-encoder's
// relevance order survives the recency fold, which is the documented contract
// for that path. The DIRECTION claim is reported rather than asserted — see the
// t.Logf below for why the fold cannot express it.
func TestRecencyTieBreakWithCrossEncoder(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	corpus, probes := TieBreakCorpus(now)

	h, err := NewHarnessWithEmbedder(
		ctx, filepath.Join(t.TempDir(), "tiebreak-ce.db"), corpus, hashEmbedder{})
	if err != nil {
		t.Fatalf("NewHarnessWithEmbedder: %v", err)
	}
	defer func() { _ = h.Close() }()
	rr := &overlapReranker{}
	h.UseReranker(rr)

	var flipped int
	for _, p := range probes {
		first, second, ok := probeRanks(t, ctx, h, p)
		if !ok {
			continue
		}
		if strings.HasPrefix(p.WantFirst, "margin-") {
			if first > second {
				t.Errorf("[cross-encoder] probe %q: %s ranked %d, %s ranked %d — %s",
					p.Query, p.WantFirst, first, p.WantSecond, second, p.Why)
			}
			continue
		}
		if first > second {
			flipped++
		}
	}
	// Prove the seam actually reached the branch under test. Without this the
	// whole test would pass vacuously on the FTS-only fallback, which is
	// exactly how the cross-encoder path stayed uncovered in the first place.
	if rr.calls != len(probes) {
		t.Fatalf("cross-encoder was called %d times for %d probes — Recall did not take the "+
			"crossEncoderReorder branch, so this test proves nothing", rr.calls, len(probes))
	}
	t.Logf("[cross-encoder] direction probes: %d/%d ordered stale-first over %d rerank calls. "+
		"NOTE for the ranking owner, reported not asserted (rank.go is outside this package): "+
		"foldRecencyPin cannot reorder a cross-encoder result under ANY input. Swapping ranks i "+
		"and i+1 needs epsilon*(nudge(i+1)-nudge(i)) > positionStep(i); foldRecencyEpsilon() is "+
		"positionStep(38)*0.5/(1+foldPinExtra) = 2.81e-5 and the largest nudge difference is "+
		"1+foldPinExtra+1 = 2.8, giving 7.86e-5 — strictly below the TIGHTEST adjacent gap in "+
		"the same pool, positionStep(38) = 1.01e-4, and every shallower gap is wider still. The "+
		"recency/pin/recall terms on that path are provably inert, not merely bounded.",
		flipped, tieProbePairs, rr.calls)
}

// runProbes scores every ordering claim and reports the rank of each side.
func runProbes(t *testing.T, ctx context.Context, h *Harness, probes []TieBreakProbe) {
	t.Helper()
	for _, p := range probes {
		first, second, ok := probeRanks(t, ctx, h, p)
		if !ok {
			continue
		}
		t.Logf("probe q=%-24q %s rank=%d, %s rank=%d", p.Query, p.WantFirst, first, p.WantSecond, second)
		if first > second {
			t.Errorf("probe %q: %s ranked %d but %s ranked %d — %s",
				p.Query, p.WantFirst, first, p.WantSecond, second, p.Why)
		}
	}
}

// probeRanks recalls one probe query and returns the 1-based ranks of the two
// documents it compares. ok is false (with the failure already reported) when
// either document is missing from the result.
func probeRanks(t *testing.T, ctx context.Context, h *Harness, p TieBreakProbe) (int, int, bool) {
	t.Helper()
	hits, err := h.Service.Recall(ctx, store.MemoryFilter{}, p.Query, probeK)
	if err != nil {
		t.Fatalf("Recall %q: %v", p.Query, err)
	}
	ranks := make(map[string]int, len(hits))
	for i, hit := range hits {
		if key := h.Key(hit.Entry.ID); key != "" {
			ranks[key] = i + 1
		}
	}
	first, okFirst := ranks[p.WantFirst]
	second, okSecond := ranks[p.WantSecond]
	if !okFirst || !okSecond {
		t.Errorf("probe %q: expected both %s and %s in the pool, got ranks %v",
			p.Query, p.WantFirst, p.WantSecond, ranks)
		return 0, 0, false
	}
	return first, second, true
}

// overlapReranker is a deterministic, offline stand-in for a cross-encoder: it
// scores each document by the fraction of query tokens it contains. Documents
// with identical text score identically, which is exactly the tie the direction
// probes need the recency fold to break.
type overlapReranker struct{ calls int }

func (*overlapReranker) HasModel() bool { return true }

func (r *overlapReranker) Rerank(_ context.Context, query string, docs []string) ([]float64, error) {
	r.calls++
	qTokens := tokens(query)
	out := make([]float64, len(docs))
	for i, doc := range docs {
		present := make(map[string]struct{}, 32)
		for _, tok := range tokens(doc) {
			present[tok] = struct{}{}
		}
		hit := 0
		for _, tok := range qTokens {
			if _, ok := present[tok]; ok {
				hit++
			}
		}
		if len(qTokens) > 0 {
			out[i] = float64(hit) / float64(len(qTokens))
		}
	}
	return out, nil
}
