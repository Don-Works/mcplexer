// rank_internal_test.go — white-box coverage for the rerank math.
package memory

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
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
