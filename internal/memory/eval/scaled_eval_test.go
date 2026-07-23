// scaled_eval_test.go — the production-shaped retrieval gate.
//
// TestRetrievalQualityGate (eval_test.go) runs 10 documents all written at test
// time. That corpus is structurally incapable of failing on recency dominance:
// the pool never saturates and every recencyFactor is 1.0. These scenarios add
// the three properties production actually has — a saturated candidate pool, a
// wide UpdatedAt spread, and distractors that share vocabulary with the query —
// and score the same hand-labeled relevance contract.
//
// Both arms are offline and deterministic. The FTS-only arm uses NoopEmbedder;
// the fused arm drives the FTS+vector path through rrfFuse using the in-package
// hashEmbedder.
package eval

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// scaledK matches the production default: memory__recall clamps limit to 10
// when the caller omits it (internal/gateway/handler_memory.go). The original
// gate's k=5 is not production-shaped, and on the fused path k also sets the
// pool depth (k*2), so using the wrong k changes what is being measured.
const scaledK = 10

// Noise volume per scenario. The FTS-only pool is a flat 50 rows regardless of
// k (SearchMemories defaults LIMIT 50), while the fused pool is k*2 = 20, so
// the vector arm needs far fewer distractors to saturate. Using a smaller
// population there keeps the fused scenarios ~3x faster without weakening what
// they measure — verified by the control arm, which still recovers every gold
// answer at this size.
const (
	ftsNoiseCount   = 490
	fusedNoiseCount = 180
)

// Scaled-scenario floors. Same contract as the small gate: a retriever that
// works must put the hand-labeled answer in the top 10 for a 10-query set with
// exactly one correct answer each.
const (
	scaledMinRecallAtK = 0.90
	scaledMinNDCG      = 0.85
	scaledMinMRR       = 0.85
	scaledMinP1        = 0.80
)

// maxRecencyDrift is how far an aged run may diverge from its own age-flattened
// control before we call recency a re-ranker again. The tie-breaker budget in
// rerankHits is sized in adjacent position steps, so freshness may shuffle
// near-neighbours; it may NOT move the answer. Anything above a few points of
// MRR means the recency term has escaped its budget.
//
// The bound is TWO-SIDED and that is load-bearing. A one-sided "flat must not
// beat aged" check passes an INVERTED recency term for free: stale-favouring
// ranking scores the aged run ABOVE its recency-inert control and the gate
// stays green. Recency correctly bounded means the two runs CONVERGE, so any
// divergence in either direction is a failure.
//
// This constant also replaced an inverted one. The gate was originally built
// against the buggy ranker, so it asserted that flattening ages must RECOVER
// >= 0.20 MRR — which encodes the bug as a requirement and can only pass while
// recency dominates. Convergence is the same evidence read the other way round
// and is the stronger regression: on the unfixed ranker the drift is an order
// of magnitude over this bound.
const maxRecencyDrift = 0.05

// maxPerQueryRecencyDrift bounds the SAME comparison per query. The aggregate
// bound alone is blind to a sign-cancelling failure: with gold documents split
// across the fresh and old bands (see corpus_scaled.go), a recency term pointed
// the wrong way helps half the queries by exactly as much as it hurts the other
// half, and the mean barely moves. Per query it cannot hide — a gold answer
// that changes rank at all shows up here. Reciprocal rank is the sharpest
// per-query signal available: any movement of the answer out of the top slot
// costs at least 0.5, far above this bound.
const maxPerQueryRecencyDrift = 0.05

// TestScaledRetrieval runs the four scenarios ONCE and asserts every claim
// against them. The four runs are the expensive part (each seeds a fresh
// on-disk SQLite store with several hundred documents), so the assertions are
// subtests over shared results rather than separate top-level tests that
// re-seed identical corpora.
//
// The claims:
//
//	fts-only/production-floors      the aged, contested corpus is scored cleanly
//	fts-only/solvable-recency-inert the corpus is not intrinsically too hard —
//	                                with recency inert it still scores cleanly,
//	                                so an aged failure is never "hard corpus"
//	fts-only/recency-bounded        aged converges on its recency-inert control
//	fused/recall                    the gold answer survives into the top k
//	fused/recall-recency-inert      ...and does so with recency inert too
//	fused/recency-bounded           aged converges on its recency-inert control
//
// The fused arm asserts recall and convergence but NOT the absolute ordering
// floors: its vector side is hashEmbedder, a hashed bag-of-words projection
// with no synonymy, which injects irreducible noise into the ORDER of the top-k
// that has nothing to do with mcplexer's ranking. Even with recency made
// completely inert that arm tops out around nDCG 0.80 / MRR 0.74 / P@1 0.70, so
// asserting production ordering floors there would be asserting a property of
// the fake embedder. recall@k IS meaningful — it asks whether the gold answer
// survived into the top 10 at all, which is exactly what the recency multiplier
// used to destroy. The ORDERING claim for that arm is made by its
// recency-bounded subtest, which scores it against its own control rather than
// against an absolute number the instrument cannot reach.
func TestScaledRetrieval(t *testing.T) {
	ftsAged := runScaledScenario(t, "fts-only/aged", nil, ftsNoiseCount, false)
	ftsFlat := runScaledScenario(t, "fts-only/age-flattened", nil, ftsNoiseCount, true)
	fusedAged := runScaledScenario(t, "fused-rrf/aged", hashEmbedder{}, fusedNoiseCount, false)
	fusedFlat := runScaledScenario(t, "fused-rrf/age-flattened", hashEmbedder{}, fusedNoiseCount, true)

	t.Run("fts-only/production-floors", func(t *testing.T) {
		assertProductionFloors(t, "fts-only/aged", ftsAged)
	})
	t.Run("fts-only/solvable-recency-inert", func(t *testing.T) {
		assertProductionFloors(t, "fts-only/age-flattened", ftsFlat)
	})
	t.Run("fts-only/recency-bounded", func(t *testing.T) {
		assertRecencyBounded(t, "fts-only", ftsAged, ftsFlat)
	})
	t.Run("fused/recall", func(t *testing.T) {
		assertRecallFloor(t, "fused-rrf/aged", fusedAged)
	})
	t.Run("fused/recall-recency-inert", func(t *testing.T) {
		assertRecallFloor(t, "fused-rrf/age-flattened", fusedFlat)
	})
	t.Run("fused/recency-bounded", func(t *testing.T) {
		assertRecencyBounded(t, "fused-rrf", fusedAged, fusedFlat)
	})
}

// assertProductionFloors holds a scenario to the same relevance contract the
// small gate uses: a 10-query set with exactly one correct answer each must put
// that answer in the top 10.
func assertProductionFloors(t *testing.T, name string, m MetricReport) {
	t.Helper()
	assertRecallFloor(t, name, m)
	if m.NDCGAtK < scaledMinNDCG {
		t.Errorf("[%s] nDCG@%d: got %.3f, want >= %.3f", name, scaledK, m.NDCGAtK, scaledMinNDCG)
	}
	if m.MRR < scaledMinMRR {
		t.Errorf("[%s] MRR: got %.3f, want >= %.3f", name, m.MRR, scaledMinMRR)
	}
	if m.PrecisionAt1 < scaledMinP1 {
		t.Errorf("[%s] P@1: got %.3f, want >= %.3f", name, m.PrecisionAt1, scaledMinP1)
	}
}

func assertRecallFloor(t *testing.T, name string, m MetricReport) {
	t.Helper()
	if m.RecallAtK < scaledMinRecallAtK {
		t.Errorf("[%s] recall@%d: got %.3f, want >= %.3f", name, scaledK, m.RecallAtK, scaledMinRecallAtK)
	}
}

// assertRecencyBounded checks that the age spread costs — and gains — almost
// nothing: the aged run and its recency-inert control must agree on every
// metric to within maxRecencyDrift, and on every individual query's reciprocal
// rank to within maxPerQueryRecencyDrift. Everything else about the two runs is
// byte-identical, so any divergence is attributable to the recency term alone.
//
// Both halves matter. |aggregate| catches a recency term that dominates in the
// obvious direction; the per-query bound catches one that is merely POINTED THE
// WRONG WAY, whose aggregate effect cancels because the corpus deliberately
// puts half the gold answers in each age band.
func assertRecencyBounded(t *testing.T, path string, aged, flat MetricReport) {
	t.Helper()
	t.Logf("RECENCY COST %s: flattening ages moves mrr %.3f -> %.3f (delta %+.3f), "+
		"p@1 %.3f -> %.3f (delta %+.3f), recall@%d %.3f -> %.3f (delta %+.3f)",
		path, aged.MRR, flat.MRR, flat.MRR-aged.MRR,
		aged.PrecisionAt1, flat.PrecisionAt1, flat.PrecisionAt1-aged.PrecisionAt1,
		scaledK, aged.RecallAtK, flat.RecallAtK, flat.RecallAtK-aged.RecallAtK)

	for _, c := range []struct {
		metric     string
		aged, flat float64
	}{
		{"MRR", aged.MRR, flat.MRR},
		{"nDCG", aged.NDCGAtK, flat.NDCGAtK},
		{"P@1", aged.PrecisionAt1, flat.PrecisionAt1},
		{"recall@k", aged.RecallAtK, flat.RecallAtK},
	} {
		if drift := c.flat - c.aged; math.Abs(drift) > maxRecencyDrift {
			t.Errorf("[%s] recency has escaped its budget: the age spread moves %s by %+.3f "+
				"(aged %.3f vs recency-inert control %.3f, two-sided budget %.2f). A NEGATIVE "+
				"drift means the aged run BEAT its own control, i.e. the recency term is "+
				"pointed the wrong way; a positive one means it is dominating relevance. "+
				"Either way the tie-breaker has escaped its position-step bound in rerankHits.",
				path, c.metric, drift, c.aged, c.flat, maxRecencyDrift)
		}
	}
	assertPerQueryRecencyBounded(t, path, aged, flat)
}

// assertPerQueryRecencyBounded compares the two runs query by query. The runs
// score the same queries in the same order, so PerQuery is index-aligned.
func assertPerQueryRecencyBounded(t *testing.T, path string, aged, flat MetricReport) {
	t.Helper()
	if len(aged.PerQuery) != len(flat.PerQuery) {
		t.Fatalf("[%s] per-query length mismatch: aged %d, flat %d",
			path, len(aged.PerQuery), len(flat.PerQuery))
	}
	worst := 0.0
	for i, a := range aged.PerQuery {
		f := flat.PerQuery[i]
		if a.Query != f.Query {
			t.Fatalf("[%s] per-query misalignment at %d: %q vs %q", path, i, a.Query, f.Query)
		}
		drift := math.Abs(f.RR - a.RR)
		if drift > worst {
			worst = drift
		}
		if drift > maxPerQueryRecencyDrift {
			t.Errorf("[%s] query %q moves rr %.3f -> %.3f (|delta| %.3f > %.2f) when the age "+
				"spread is removed; top hit was %q aged vs %q recency-inert. Half the gold "+
				"answers are FRESH and half are OLD, so a per-query swing that the aggregate "+
				"absorbs is the signature of a recency term pointed the wrong way.",
				path, a.Query, a.RR, f.RR, drift, maxPerQueryRecencyDrift, a.TopKey, f.TopKey)
		}
	}
	t.Logf("RECENCY COST %s: worst per-query |rr delta| = %.3f (budget %.2f)",
		path, worst, maxPerQueryRecencyDrift)
}

// runScaledScenario seeds the production-shaped corpus and scores it. emb nil
// selects the FTS5-only path.
func runScaledScenario(t *testing.T, name string, emb interface {
	HasModel() bool
	Embed(context.Context, []string) ([][]float32, string, error)
}, noiseCount int, flattenAges bool,
) MetricReport {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	corpus := ScaledCorpus(now, noiseCount)
	if flattenAges {
		corpus = FlattenAges(corpus, now)
	}

	h := newScaledHarness(t, ctx, corpus, emb)
	defer func() { _ = h.Close() }()

	results, lat, err := h.RunQueries(ctx, corpus, scaledK)
	if err != nil {
		t.Fatalf("RunQueries: %v", err)
	}
	m := Aggregate(results, scaledK)
	logScenario(t, name, corpus, m, summarizeLatency(lat), flattenAges)
	return m
}

func logScenario(t *testing.T, name string, corpus Corpus, m MetricReport, lat LatencyReport, flat bool) {
	t.Helper()
	ages := fmt.Sprintf("gold %d-%dd + %d-%dd, rivals <%dh + %d-%dd",
		goldOldMinDays, goldOldMaxDays, goldFreshMinDays, goldFreshMaxDays,
		rivalFreshMaxHours, rivalOldMinDays, rivalOldMaxDays)
	if flat {
		ages = "ALL docs same age (recency inert)"
	}
	t.Logf("scenario=%s corpus=%d docs (%d gold, %d noise; %s) queries=%d k=%d",
		name, len(corpus.Memories), len(GoldKeys(corpus)),
		len(corpus.Memories)-len(GoldKeys(corpus)), ages, m.NumQueries, m.K)
	t.Logf("scenario=%s recall@%d=%.3f ndcg@%d=%.3f mrr=%.3f p@1=%.3f",
		name, m.K, m.RecallAtK, m.K, m.NDCGAtK, m.MRR, m.PrecisionAt1)
	t.Logf("scenario=%s retrieve latency: p50=%s p95=%s max=%s (n=%d)",
		name, lat.P50, lat.P95, lat.Max, lat.Samples)
	for _, q := range m.PerQuery {
		t.Logf("  q=%-46q recall=%.2f ndcg=%.2f rr=%.2f top=%s",
			q.Query, q.RecallAtK, q.NDCGAtK, q.RR, q.TopKey)
	}
}

func newScaledHarness(t *testing.T, ctx context.Context, c Corpus, emb interface {
	HasModel() bool
	Embed(context.Context, []string) ([][]float32, string, error)
},
) *Harness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scaled.db")
	var (
		h   *Harness
		err error
	)
	if emb == nil {
		h, err = NewHarness(ctx, dbPath, c)
	} else {
		h, err = NewHarnessWithEmbedder(ctx, dbPath, c, emb)
	}
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h
}
