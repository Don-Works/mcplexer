// rank_internal_test.go — white-box coverage for the rerank math.
package memory

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRecencyFactor(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		age  time.Duration
		want float64
	}{
		{name: "fresh", age: 0, want: 1.0},
		{name: "future_clamped", age: -time.Hour, want: 1.0},
		{name: "one_half_life", age: recencyHalfLife,
			want: recencyFloor + (1.0-recencyFloor)*0.5},
		{name: "two_half_lives", age: 2 * recencyHalfLife,
			want: recencyFloor + (1.0-recencyFloor)*0.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recencyFactor(now, now.Add(-tc.age))
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("recencyFactor(age=%v) = %v, want %v", tc.age, got, tc.want)
			}
		})
	}

	// Decade-old rows asymptote to the floor — never zero.
	ancient := recencyFactor(now, now.Add(-10*365*24*time.Hour))
	if ancient < recencyFloor || ancient > recencyFloor+0.001 {
		t.Fatalf("ancient factor should sit at the floor, got %v", ancient)
	}
}

func TestRerankHitsPinnedBoost(t *testing.T) {
	now := time.Now().UTC()
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "unpinned", UpdatedAt: now}, Source: "fts"},
		{Entry: store.MemoryEntry{ID: "pinned", UpdatedAt: now, Pinned: true}, Source: "fts"},
	}
	out := rerankHits(hits, now, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(out))
	}
	// Equal recency: the 1.5x pinned boost must overcome one rank step
	// (1.5/62 > 1/61).
	if out[0].Entry.ID != "pinned" {
		t.Fatalf("pinned hit should rank first, got %q", out[0].Entry.ID)
	}
	if out[0].Source != "fts" {
		t.Fatalf("rerank must preserve Source, got %q", out[0].Source)
	}
	// Input slice must not be reordered in place.
	if hits[0].Entry.ID != "unpinned" {
		t.Fatal("rerankHits mutated its input slice order")
	}
}

// TestRerankHitsRecencyDemotesStale is the "recency must still MATTER" half of
// the contract: between two hits the retriever ranked as near-equals, the
// fresher one wins. This assertion is UNCHANGED by the bounded-nudge fix — a
// single tie-breaker term is deliberately sized to climb exactly one adjacent
// rank, and one rank is all this case asks for. (What the fix removed is the
// ability to climb TWENTY ranks; see TestRerankHitsRecencyIsBoundedNotDominant.)
func TestRerankHitsRecencyDemotesStale(t *testing.T) {
	now := time.Now().UTC()
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "stale", UpdatedAt: now.Add(-120 * 24 * time.Hour)}},
		{Entry: store.MemoryEntry{ID: "fresh", UpdatedAt: now}},
	}
	out := rerankHits(hits, now, nil)
	if out[0].Entry.ID != "fresh" {
		t.Fatalf("fresh hit should outrank 4-month-old hit, got %q first", out[0].Entry.ID)
	}
}

// mkHit builds a rerank input with an explicit retrieval signal. Passing the
// SAME score to every hit makes normalizedSignals flat (all 1.0), so base(i)
// reduces to the pure position score and the arithmetic below is exact.
func mkHit(id string, updated time.Time, pinned bool, score float64) store.MemoryHit {
	return store.MemoryHit{
		Entry:  store.MemoryEntry{ID: id, UpdatedAt: updated, Pinned: pinned},
		Source: "rrf",
		Score:  score,
	}
}

// TestRerankHitsNudgeBudgetArithmetic writes the tie-breaker budget down as
// numbers, the way the comments in rank_tuning.go do. Three claims:
//
//  1. ONE STEP OF AUTHORITY — a hit one position down, at full signal on a
//     single term, out-scores the hit above it. Without this, pinning and the
//     recall nudge silently stop working (they become sub-step no-ops).
//  2. NEVER TWO — that same hit two positions down cannot out-score it. This
//     is what makes each term a tie-breaker rather than a re-ranker.
//  3. THE INVARIANT — the worst-case COMBINED nudge (fresh + pinned + hot vs
//     ancient + unpinned + cold) is strictly smaller than the relevance spread
//     of the pool it could overturn. This is the property the old
//     score = base * recencyFactor formulation violated: recencyFactor spanned
//     20x against a pool spread of ~1.5x, so recency outranked relevance.
func TestRerankHitsNudgeBudgetArithmetic(t *testing.T) {
	// The production pool depth: memory__recall defaults limit=10 and Recall
	// caps the fused pool at k*2 (internal/gateway/handler_memory.go,
	// internal/memory/registry.go). The FTS-only pool is deeper (50), and
	// deeper pools only make the bounds below tighter-satisfied, since
	// positionStep shrinks monotonically.
	const poolN = 20

	t.Run("every term can climb at least one rank", func(t *testing.T) {
		// A budget that cannot cover a single adjacent step is a dead term:
		// it would only ever reorder exact ties. rankRecallSteps is the
		// smallest budget and therefore the binding case.
		for _, tc := range []struct {
			name  string
			steps float64
		}{
			{"recency", rankRecencySteps},
			{"pin", rankPinSteps},
			{"recall", rankRecallSteps},
		} {
			for i := 0; i+1 < poolN; i++ {
				lift := tc.steps * positionStep(i+1)
				if lift <= positionStep(i) {
					t.Fatalf("%s i=%d: full-signal lift %.4e cannot cover one step %.4e — "+
						"the tie-break would be dead", tc.name, i, lift, positionStep(i))
				}
			}
		}
	})

	t.Run("realized climbs match the documented figures", func(t *testing.T) {
		// rank_tuning.go documents 3.0 steps -> 2 ranks and 1.2 -> 1 rank.
		// Behaviour claims in comments are load-bearing here, so pin them.
		for _, tc := range []struct {
			name  string
			steps float64
			ranks int
		}{
			{"recency", rankRecencySteps, 2},
			{"pin", rankPinSteps, 2},
			{"recall", rankRecallSteps, 1},
			{"combined", rerankMaxNudgeSteps, 5},
		} {
			got := realizedClimb(tc.steps, 0)
			if got != tc.ranks {
				t.Errorf("%s budget %.1f steps realizes %d ranks at the head of the "+
					"pool, comment claims %d", tc.name, tc.steps, got, tc.ranks)
			}
		}
	})

	t.Run("combined nudge cannot outvote relevance magnitude", func(t *testing.T) {
		// NOTE this deliberately does NOT compare the nudge against the pool's
		// head-to-tail score spread, the way an earlier revision did. That
		// spread shrinks as the pool shortens, so the comparison is false for
		// pools of <=6 hits and the assertion only ever passed because poolN
		// was hardcoded to 20. The depth-independent bound lives in
		// TestRerankHitsClimbBoundHoldsAtEveryDepth; what belongs HERE is the
		// comparison against the other relevance term, which is depth-free.
		maxNudge := rerankMaxNudgeSteps * positionStep(0)
		signalSpan := (1.0 - rankBlendAlpha) / float64(rerankBaseK+1)
		if maxNudge >= signalSpan {
			t.Fatalf("max combined nudge %.4e (%.1f steps) must be < the "+
				"signal-magnitude span %.4e (%.1f steps) — freshness would outvote "+
				"BM25/vector relevance and the original bug returns",
				maxNudge, maxNudge/positionStep(0),
				signalSpan, signalSpan/positionStep(0))
		}
	})
}

// ids renders a hit list's IDs in rank order, for failure messages.
func ids(hits []store.MemoryHit) string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Entry.ID
	}
	return strings.Join(out, ",")
}

// realizedClimb returns how many ranks a term with the given step budget can
// actually lift a hit from rank i, by the bound derived in rank_tuning.go:
// climbing m ranks needs budget > (step(i)+..+step(i+m-1))/step(i+m).
func realizedClimb(steps float64, i int) int {
	m, cum := 0, 0.0
	for {
		cum += positionStep(i + m)
		if steps <= cum/positionStep(i+m+1) {
			return m
		}
		m++
	}
}

// TestRerankHitsClimbBoundHoldsAtEveryDepth is the depth-independent form of
// the invariant. The previous revision asserted "max nudge < pool relevance
// spread" at a single hardcoded pool depth of 20; that statement is not
// depth-independent (the spread shrinks with the pool, so it is false for
// pools of <=6) and small pools are common — k=5 recalls, tag/entity-filtered
// recall, and any query where few rows match at all.
//
// The bound that IS true everywhere: a term with budget c climbs at most c
// ranks, because overtaking from rank j to rank i needs the gap
// sum_{m=i}^{j-1} step(m) >= (j-i)*step(j) to be under c*step(j).
func TestRerankHitsClimbBoundHoldsAtEveryDepth(t *testing.T) {
	for _, depth := range []int{1, 2, 3, 5, 6, 10, 20, 50, 100} {
		for i := 0; i < depth; i++ {
			if got := realizedClimb(rerankMaxNudgeSteps, i); float64(got) > rerankMaxNudgeSteps {
				t.Errorf("depth=%d i=%d: combined budget %.1f steps realized %d ranks, "+
					"exceeding its own bound", depth, i, rerankMaxNudgeSteps, got)
			}
		}
	}
}

// TestRerankHitsPinLiftsFromDepth covers the gap that let pin authority
// collapse ~20x unnoticed: every pre-existing pin test used a pinned hit that
// was ALSO the freshest, so it would have ranked first regardless and the test
// passed no matter how weak pin became. Here the pinned hit is the STALEST and
// starts below its peers, so only pin can move it.
func TestRerankHitsPinLiftsFromDepth(t *testing.T) {
	now := time.Now().UTC()
	// Every hit is equally ancient, so the recency term is flat across the pool
	// and pin is the ONLY thing that can move anything. (An earlier draft of
	// this test made the peers fresh, which silently cancelled pin out: the
	// peers' recency budget equalled the pinned hit's pin budget and nothing
	// moved. Isolate one variable.)
	ancient := now.Add(-400 * 24 * time.Hour)
	hits := make([]store.MemoryHit, 6)
	for i := range hits {
		hits[i] = store.MemoryHit{
			Entry:  store.MemoryEntry{ID: string(rune('a' + i)), UpdatedAt: ancient},
			Source: "rrf", Score: 1.0,
		}
	}
	hits[5].Entry.Pinned = true // worst starting position: last

	got := rerankHits(hits, now, nil)
	pos := -1
	for i, h := range got {
		if h.Entry.ID == "f" {
			pos = i
		}
	}
	if pos == 5 {
		t.Fatalf("pinned hit did not move at all — pin is inert, which is the "+
			"regression this test exists to catch (order: %s)", ids(got))
	}
	if pos == 0 {
		t.Fatalf("pinned-but-ancient hit reached #1 from the bottom of the pool — "+
			"pin is overriding relevance, not weighting it (order: %s)", ids(got))
	}
}

// TestRankRecencyGradesAcrossMonths guards against the over-correction the
// first fix introduced: with an exponential curve and a ~1.6-step budget,
// recency had no ordering effect past ~19 days, so a 20-day-old and a
// 400-day-old memory were ranking-identical on a store holding months of
// history. Each age band below must be separated from the next by a visible
// fraction of a rank.
func TestRankRecencyGradesAcrossMonths(t *testing.T) {
	now := time.Now().UTC()
	day := 24 * time.Hour
	ages := []time.Duration{0, day, 7 * day, 30 * day, 90 * day, 200 * day}

	prev := math.Inf(1)
	for i, age := range ages {
		ranks := rankRecencySteps * rankRecency(now, now.Add(-age))
		if ranks >= prev {
			t.Fatalf("age %v: recency must decrease monotonically, got %.3f after %.3f",
				age, ranks, prev)
		}
		// Skip the first band (nothing precedes it to compare against).
		if i > 0 {
			if gap := prev - ranks; gap < 0.10 {
				t.Errorf("age %v: only %.3f ranks separate this band from the previous "+
					"one — recency has gone flat here", age, gap)
			}
		}
		prev = ranks
	}
	// The specific cliff that motivated this test.
	twenty := rankRecencySteps * rankRecency(now, now.Add(-20*day))
	fourHundred := rankRecencySteps * rankRecency(now, now.Add(-400*day))
	if twenty-fourHundred < 1.0 {
		t.Fatalf("a 20-day-old and a 400-day-old memory differ by only %.3f ranks — "+
			"this is the three-week cliff regression", twenty-fourHundred)
	}
}

// TestRerankHitsRecencyIsBoundedNotDominant is the regression for the bug this
// file exists to prevent: on the PRODUCTION path (no cross-encoder), a fresh
// but less-relevant hit could be multiplied from the bottom of a pool to #1,
// because score = base * recencyFactor gave recency a 20x range against a
// ~1.5x base. Each case below fixes every variable except the one under test.
func TestRerankHitsRecencyIsBoundedNotDominant(t *testing.T) {
	now := time.Now().UTC()
	ancient := now.Add(-400 * 24 * time.Hour)
	hot := map[string]store.MemoryRecallStat{
		"climber": {MemoryID: "climber", RecentCount: 1000, LastRecalledAt: now},
	}

	t.Run("fresh cannot leap a clearly-higher stale hit", func(t *testing.T) {
		// staleTop at rank 0, climber (maximally fresh) five ranks down.
		// pos(0)-pos(5) = 1/61-1/66 = 1.24e-3; the recency nudge available at
		// rank 5 is 1.6*step(5) = 3.6e-4. Relevance wins by ~3.4x.
		in := []store.MemoryHit{mkHit("staleTop", ancient, false, 1.0)}
		for i := 1; i < 5; i++ {
			in = append(in, mkHit("mid", ancient, false, 1.0))
		}
		in = append(in, mkHit("climber", now, false, 1.0))
		out := rerankHits(in, now, nil)
		if out[0].Entry.ID != "staleTop" {
			t.Fatalf("stale-but-most-relevant must stay #1, got %q", out[0].Entry.ID)
		}
	})

	t.Run("fresh+pinned+hot cannot leap a clearly-higher stale hit", func(t *testing.T) {
		// Every tie-breaker maxed at once — the worst case the budget allows.
		// pos(0)-pos(6) = 1.47e-3 vs a combined nudge of 4.8*step(6) = 1.05e-3.
		in := []store.MemoryHit{mkHit("staleTop", ancient, false, 1.0)}
		for i := 1; i < 6; i++ {
			in = append(in, mkHit("mid", ancient, false, 1.0))
		}
		in = append(in, mkHit("climber", now, true, 1.0))
		out := rerankHits(in, now, hot)
		if out[0].Entry.ID != "staleTop" {
			t.Fatalf("stale-but-most-relevant must stay #1 against a maxed-out "+
				"tie-breaker stack, got %q", out[0].Entry.ID)
		}
	})

	// The production symptom in miniature: one old-but-correct hit at the head
	// of a 20-deep pool that is otherwise entirely fresh noise. Before the fix
	// the gold hit scored 1/61*0.05 = 2.7e-4 against fresh noise at the TAIL of
	// the pool scoring 1/80*1.0 = 1.25e-2, so it lost to every single
	// distractor and finished LAST. Two variants, because the honest answer
	// depends on whether the retriever separated gold from the noise at all.
	saturated := func(goldSignal, noiseSignal float64) []store.MemoryHit {
		in := []store.MemoryHit{mkHit("gold", now.Add(-60*24*time.Hour), false, goldSignal)}
		for i := 1; i < 20; i++ {
			in = append(in, mkHit("noise", now.Add(-time.Duration(i)*time.Hour), false, noiseSignal))
		}
		return in
	}

	t.Run("saturated fresh pool cannot displace a clearly-more-relevant old answer", func(t *testing.T) {
		// Gold carries a clearly stronger retrieval signal (what BM25 actually
		// produces for a topical match against unrelated prose). The
		// signal-blend gap is (1-alpha)*(1.0-0.0)*pos(0) ≈ 9.3 position steps,
		// far outside the 1.6-step recency budget.
		out := rerankHits(saturated(1.0, 0.2), now, nil)
		if out[0].Entry.ID != "gold" {
			t.Fatalf("a 60-day-old but clearly-more-relevant top hit must survive a "+
				"pool of 19 fresh distractors, got %q", out[0].Entry.ID)
		}
	})

	t.Run("flat-signal old answer slips at most one rank in a fresh pool", func(t *testing.T) {
		// Worst case for the old answer: the retriever gave it NO magnitude
		// advantage, so only its list position defends it and freshness is the
		// only differentiator left. By design recency then buys the fresh
		// neighbour exactly one rank — gold at 60 days (normRecency 0.25) vs a
		// fresh hit one step down costs 1.147 steps, above the 1.0 it must
		// cover; the hit TWO steps down needs 1.968 and only musters 1.10. So
		// gold lands at #2 and no further. Pre-fix it landed at #20.
		out := rerankHits(saturated(1.0, 1.0), now, nil)
		var goldRank = -1
		for i, h := range out {
			if h.Entry.ID == "gold" {
				goldRank = i
				break
			}
		}
		if goldRank != 1 {
			t.Fatalf("flat-signal gold must slip exactly one rank (to #2) in a fully "+
				"fresh pool, got rank #%d of %d", goldRank+1, len(out))
		}
	})

	t.Run("equal relevance still orders fresher first", func(t *testing.T) {
		// The other half of the contract: recency MATTERS. Identical signal,
		// stale listed first — the fresher hit must still take #1.
		in := []store.MemoryHit{
			mkHit("stale", now.Add(-90*24*time.Hour), false, 1.0),
			mkHit("fresher", now, false, 1.0),
		}
		out := rerankHits(in, now, nil)
		if out[0].Entry.ID != "fresher" {
			t.Fatalf("at equal relevance the fresher hit must win, got %q", out[0].Entry.ID)
		}
	})

	t.Run("stale belief loses to its fresher replacement", func(t *testing.T) {
		// A superseded fact and its replacement retrieve near-identically (same
		// wording, same signal). The replacement must surface first.
		in := []store.MemoryHit{
			mkHit("superseded", now.Add(-200*24*time.Hour), false, 1.0),
			mkHit("replacement", now.Add(-1*time.Hour), false, 1.0),
		}
		out := rerankHits(in, now, nil)
		if out[0].Entry.ID != "replacement" {
			t.Fatalf("the fresher replacement must outrank the superseded belief, got %q",
				out[0].Entry.ID)
		}
	})

	t.Run("relevance magnitude outranks freshness", func(t *testing.T) {
		// A clearly stronger retrieval signal on a STALE hit beats a weak
		// signal on a FRESH one, even with the fresh hit listed first. This is
		// the ordering of authorities the fix establishes: signal span at the
		// head of the pool is ~9.3 position steps, the nudge budget is 1.6.
		in := []store.MemoryHit{
			mkHit("freshWeak", now, false, 0.001),
			mkHit("staleStrong", ancient, false, 1.0),
		}
		out := rerankHits(in, now, nil)
		if out[0].Entry.ID != "staleStrong" {
			t.Fatalf("the clearly-more-relevant stale hit must win, got %q", out[0].Entry.ID)
		}
	})
}

// TestRerankHitsBM25MagnitudeBreaksTie covers the blend-magnitude change:
// two FTS hits sit at the SAME list position relative to each other only
// in the degenerate sense — here we place a BM25-strong and a BM25-weak
// hit so that, with identical recency, the stronger BM25 signal lifts the
// strong hit above the weak one even though both could otherwise tie. We
// assert the strong hit outranks the weak one when the weak one is GIVEN
// the better list position, proving the magnitude blend has real effect.
func TestRerankHitsBM25MagnitudeBreaksTie(t *testing.T) {
	now := time.Now().UTC()
	// Index 0 (better position) is the BM25-weak hit; index 1 (worse
	// position) is the BM25-strong hit. fts.rank is negative=better, so a
	// more-negative Score = stronger. The position gap is one step
	// (1/61 vs 1/62); the magnitude blend must be enough to flip them.
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "weak", UpdatedAt: now}, Source: "fts", Score: -0.1},
		{Entry: store.MemoryEntry{ID: "strong", UpdatedAt: now}, Source: "fts", Score: -100.0},
	}
	out := rerankHits(hits, now, nil)
	if out[0].Entry.ID != "strong" {
		t.Fatalf("BM25-strong hit should outrank BM25-weak hit despite worse "+
			"list position, got %q first (scores: %v)", out[0].Entry.ID, out)
	}
}

// TestMMRReorderDeduplicates verifies MMR pushes a near-duplicate of the
// top hit down in favour of a diverse third candidate. Vectors: hit0 and
// hit1 are near-identical; hit2 is orthogonal. With lambda<1 the second
// pick should be the orthogonal hit2, not the duplicate hit1.
func TestMMRReorderDeduplicates(t *testing.T) {
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "a"}},
		{Entry: store.MemoryEntry{ID: "a-dup"}},
		{Entry: store.MemoryEntry{ID: "b"}},
	}
	vecs := [][]float32{
		{1, 0, 0},
		{0.99, 0.01, 0}, // near-duplicate of a
		{0, 0, 1},       // orthogonal
	}
	out := mmrReorder(hits, vecs, 0.5)
	if out[0].Entry.ID != "a" {
		t.Fatalf("most relevant hit should stay first, got %q", out[0].Entry.ID)
	}
	if out[1].Entry.ID != "b" {
		t.Fatalf("MMR should pick the diverse hit second, got %q", out[1].Entry.ID)
	}
}

// TestMMRReorderIdentityWithoutVectors asserts MMR degrades to identity
// (no reorder) when no candidate carries a vector.
func TestMMRReorderIdentityWithoutVectors(t *testing.T) {
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "x"}},
		{Entry: store.MemoryEntry{ID: "y"}},
	}
	out := mmrReorder(hits, [][]float32{nil, nil}, 0.7)
	if out[0].Entry.ID != "x" || out[1].Entry.ID != "y" {
		t.Fatalf("expected identity order with no vectors, got %q,%q",
			out[0].Entry.ID, out[1].Entry.ID)
	}
}

// rerankServer spins an httptest server returning the supplied raw JSON body
// with the supplied status, so a test can drive HTTPReranker.Rerank through
// every response shape.
func rerankServer(t *testing.T, status int, body string) *HTTPReranker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	r, err := NewHTTPReranker(srv.URL, "test-model", "")
	if err != nil {
		t.Fatalf("NewHTTPReranker: %v", err)
	}
	return r
}

// TestHTTPRerankerCoverage is the MED-4 regression: a response that does not
// cover EVERY doc exactly once (empty, partial, out-of-range, negative,
// duplicate, or a different field shape) must ERROR rather than zero-fill, so
// the caller falls back instead of trusting a corrupted order.
func TestHTTPRerankerCoverage(t *testing.T) {
	ctx := context.Background()
	docs := []string{"a", "b", "c"}

	tests := []struct {
		name    string
		body    string
		wantErr bool
		want    []float64 // checked when !wantErr
	}{
		{
			name: "full coverage in order",
			body: `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5},{"index":2,"relevance_score":0.1}]}`,
			want: []float64{0.9, 0.5, 0.1},
		},
		{
			name: "full coverage scrambled scattered back to input order",
			body: `{"results":[{"index":2,"relevance_score":0.1},{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5}]}`,
			want: []float64{0.9, 0.5, 0.1},
		},
		{name: "empty results errors", body: `{"results":[]}`, wantErr: true},
		{name: "missing doc partial errors", body: `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5}]}`, wantErr: true},
		{name: "out-of-range index errors", body: `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5},{"index":7,"relevance_score":0.1}]}`, wantErr: true},
		{name: "negative index errors", body: `{"results":[{"index":-1,"relevance_score":0.9},{"index":1,"relevance_score":0.5},{"index":2,"relevance_score":0.1}]}`, wantErr: true},
		{name: "duplicate index errors", body: `{"results":[{"index":0,"relevance_score":0.9},{"index":0,"relevance_score":0.5},{"index":1,"relevance_score":0.1}]}`, wantErr: true},
		{name: "different field shape no results errors", body: `{"data":[{"index":0,"score":0.9}]}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := rerankServer(t, http.StatusOK, tc.body)
			got, err := r.Rerank(ctx, "q", docs)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got scores=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("score[%d]=%v want %v (full=%v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestHTTPRerankerHTTPError covers the >=400 path (vendor 500 etc.).
func TestHTTPRerankerHTTPError(t *testing.T) {
	r := rerankServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	if _, err := r.Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Fatal("want error on HTTP 500, got nil")
	}
}

// TestHTTPRerankerEmptyDocs returns (nil,nil) for no docs — nothing to rank.
func TestHTTPRerankerEmptyDocs(t *testing.T) {
	r := rerankServer(t, http.StatusOK, `{"results":[]}`)
	got, err := r.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("empty docs should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("empty docs should return nil scores, got %v", got)
	}
}

func TestCosineF32(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical unit vectors", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"zero vector a", []float32{0, 0, 0}, []float32{1, 2, 3}, 0},
		{"zero vector b", []float32{1, 2, 3}, []float32{0, 0, 0}, 0},
		{"length mismatch", []float32{1, 2}, []float32{1, 2, 3}, 0},
		{"empty a", []float32{}, []float32{1}, 0},
		{"both empty", []float32{}, []float32{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineF32(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cosineF32=%v want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizedSignalsPerSource(t *testing.T) {
	mk := func(src string, score float64) store.MemoryHit {
		return store.MemoryHit{Source: src, Score: score}
	}

	// fts/list: fts.rank more-negative = better → most-negative normalizes to 1.
	t.Run("fts negative-better flipped", func(t *testing.T) {
		out := normalizedSignals([]store.MemoryHit{mk("fts", -5.0), mk("fts", -1.0)})
		if out[0] != 1.0 || out[1] != 0.0 {
			t.Fatalf("fts norm=%v want [1 0]", out)
		}
	})
	// vec: raw DISTANCE lower = better → smaller distance normalizes to 1.
	t.Run("vec raw-distance lower-better", func(t *testing.T) {
		out := normalizedSignals([]store.MemoryHit{mk("vec", 0.1), mk("vec", 2.0)})
		if out[0] != 1.0 || out[1] != 0.0 {
			t.Fatalf("vec norm=%v want [1 0] (closer distance must be best)", out)
		}
	})
	// rrf/default: already positive, higher = better.
	t.Run("rrf positive-better", func(t *testing.T) {
		out := normalizedSignals([]store.MemoryHit{mk("rrf", 0.9), mk("rrf", 0.1)})
		if out[0] != 1.0 || out[1] != 0.0 {
			t.Fatalf("rrf norm=%v want [1 0]", out)
		}
	})
	// signal-flat list → all 1.0 (blend reduces to pure position).
	t.Run("flat list all ones", func(t *testing.T) {
		out := normalizedSignals([]store.MemoryHit{mk("rrf", 0.5), mk("rrf", 0.5)})
		if out[0] != 1.0 || out[1] != 1.0 {
			t.Fatalf("flat norm=%v want [1 1]", out)
		}
	})
	// single hit → 1.0.
	t.Run("single hit", func(t *testing.T) {
		out := normalizedSignals([]store.MemoryHit{mk("fts", -3.0)})
		if len(out) != 1 || out[0] != 1.0 {
			t.Fatalf("single norm=%v want [1]", out)
		}
	})
}

// TestFoldRecencyPinBoundedNudge proves the HIGH-1 invariant directly at the
// helper level: a fresh-but-lower-ranked hit cannot leap a stale higher-ranked
// hit, ties are stable, and the epsilon bound is strictly sub-step.
func TestFoldRecencyPinBoundedNudge(t *testing.T) {
	now := time.Now().UTC()
	mk := func(id string, updated time.Time, pinned bool) store.MemoryHit {
		return store.MemoryHit{
			Entry:  store.MemoryEntry{ID: id, UpdatedAt: updated, Pinned: pinned},
			Source: "rrf",
		}
	}

	t.Run("fresh cannot leap clearly-higher stale", func(t *testing.T) {
		in := []store.MemoryHit{
			mk("staleTop", now.Add(-400*24*time.Hour), false),
			mk("fresh", now, false),
			mk("mid", now.Add(-200*24*time.Hour), false),
		}
		out := foldRecencyPin(in, now, nil)
		if out[0].Entry.ID != "staleTop" {
			t.Fatalf("staleTop must stay #1, got %s", out[0].Entry.ID)
		}
		if out[1].Entry.ID != "fresh" {
			t.Fatalf("fresh must stay #2, got %s", out[1].Entry.ID)
		}
	})

	t.Run("pinned cannot leap clearly-higher stale", func(t *testing.T) {
		// Even a PINNED fresh hit at #2 cannot leap a clearly-more-relevant #1.
		in := []store.MemoryHit{
			mk("staleTop", now.Add(-400*24*time.Hour), false),
			mk("pinnedFresh", now, true),
		}
		out := foldRecencyPin(in, now, nil)
		if out[0].Entry.ID != "staleTop" {
			t.Fatalf("pin must not leap a clearly-higher hit, got #1=%s", out[0].Entry.ID)
		}
	})

	t.Run("deterministic stable on equal recency", func(t *testing.T) {
		in := []store.MemoryHit{mk("a", now, false), mk("b", now, false)}
		out := foldRecencyPin(in, now, nil)
		if out[0].Entry.ID != "a" || out[1].Entry.ID != "b" {
			t.Fatalf("equal recency must preserve incoming order, got %s,%s",
				out[0].Entry.ID, out[1].Entry.ID)
		}
	})

	t.Run("empty no-op", func(t *testing.T) {
		if out := foldRecencyPin(nil, now, nil); out != nil {
			t.Fatalf("nil input should return nil, got %v", out)
		}
	})

	t.Run("epsilon strictly sub-step", func(t *testing.T) {
		eps := foldRecencyEpsilon()
		maxNudge := eps * (1.0 + foldPinExtra)
		i := foldPoolN - 2
		gap := 1.0/float64(rerankBaseK+i+1) - 1.0/float64(rerankBaseK+i+2)
		if maxNudge >= gap {
			t.Fatalf("max nudge %.3e must be < smallest base gap %.3e", maxNudge, gap)
		}
	})

	t.Run("epsilon sub-step WITH recall term", func(t *testing.T) {
		// Worst-case combined nudge once the recall signal (max 1.0) is added
		// on top of recency (max 1.0) + pin (foldPinExtra) must STILL stay
		// below one position step, or the recall term could re-sort rather
		// than tie-break.
		eps := foldRecencyEpsilon()
		maxNudge := eps * (1.0 + foldPinExtra + 1.0)
		i := foldPoolN - 2
		gap := 1.0/float64(rerankBaseK+i+1) - 1.0/float64(rerankBaseK+i+2)
		if maxNudge >= gap {
			t.Fatalf("max nudge WITH recall %.3e must be < smallest base gap %.3e", maxNudge, gap)
		}
	})
}

// TestRecallSignalZeroDegradations proves the recall term degrades to a
// no-op for every "no data" shape, so ranking is unchanged when the recall
// log is empty or tracking is off.
func TestRecallSignalZeroDegradations(t *testing.T) {
	now := time.Now().UTC()
	if got := recallSignal(nil, "x", now); got != 0 {
		t.Fatalf("nil map must give 0, got %v", got)
	}
	if got := recallSignal(map[string]store.MemoryRecallStat{}, "x", now); got != 0 {
		t.Fatalf("empty map must give 0, got %v", got)
	}
	m := map[string]store.MemoryRecallStat{"y": {MemoryID: "y", RecentCount: 5, LastRecalledAt: now}}
	if got := recallSignal(m, "x", now); got != 0 {
		t.Fatalf("absent id must give 0, got %v", got)
	}
	m["z"] = store.MemoryRecallStat{MemoryID: "z", RecentCount: 0}
	if got := recallSignal(m, "z", now); got != 0 {
		t.Fatalf("zero-count stat must give 0, got %v", got)
	}
}

// TestRecallSignalBounded proves the signal stays in [0,1] and grows with
// frequency + recency, so the additive nudge can never blow past its
// epsilon budget.
func TestRecallSignalBounded(t *testing.T) {
	now := time.Now().UTC()
	saturated := map[string]store.MemoryRecallStat{
		"hot": {MemoryID: "hot", RecentCount: 1000, LastRecalledAt: now},
	}
	if got := recallSignal(saturated, "hot", now); got <= 0 || got > 1.0 {
		t.Fatalf("saturated signal must be in (0,1], got %v", got)
	}
	light := map[string]store.MemoryRecallStat{
		"warm": {MemoryID: "warm", RecentCount: 1, LastRecalledAt: now},
	}
	hot := recallSignal(saturated, "hot", now)
	warm := recallSignal(light, "warm", now)
	if hot <= warm {
		t.Fatalf("more recalls should signal stronger: hot=%v warm=%v", hot, warm)
	}
}

// TestRerankHitsRecallTieBreaks proves the AR4 recall nudge lifts a
// frequently-recalled memory above an equal-relevance never-recalled one,
// AND that with an empty/nil recall map the ranking is byte-identical to the
// pre-AR4 behaviour.
func TestRerankHitsRecallTieBreaks(t *testing.T) {
	now := time.Now().UTC()
	// Two hits with identical retrieval signal + recency. Without recall the
	// stable sort keeps incoming order (a then b).
	hits := []store.MemoryHit{
		{Entry: store.MemoryEntry{ID: "never", UpdatedAt: now}, Source: "fts", Score: -1.0},
		{Entry: store.MemoryEntry{ID: "often", UpdatedAt: now}, Source: "fts", Score: -1.0},
	}

	// Empty log: ranking must equal the no-recall ranking exactly.
	base := rerankHits(hits, now, nil)
	withEmpty := rerankHits(hits, now, map[string]store.MemoryRecallStat{})
	if len(base) != len(withEmpty) {
		t.Fatalf("length mismatch")
	}
	for i := range base {
		if base[i].Entry.ID != withEmpty[i].Entry.ID || base[i].Score != withEmpty[i].Score {
			t.Fatalf("empty recall log must not change ranking: %v vs %v", base, withEmpty)
		}
	}
	if base[0].Entry.ID != "never" {
		t.Fatalf("baseline (no recall) keeps incoming order, got #1=%s", base[0].Entry.ID)
	}

	// With recall events for "often", it should overtake "never".
	recall := map[string]store.MemoryRecallStat{
		"often": {MemoryID: "often", RecentCount: 10, LastRecalledAt: now},
	}
	withRecall := rerankHits(hits, now, recall)
	if withRecall[0].Entry.ID != "often" {
		t.Fatalf("frequently-recalled memory should rank first, got #1=%s (%+v)",
			withRecall[0].Entry.ID, withRecall)
	}
}
