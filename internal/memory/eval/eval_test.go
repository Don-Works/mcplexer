// eval_test.go — the retrieval-quality CI gate. Seeds the labeled fixture
// corpus into a real on-disk SQLite store, drives memory.Service.Recall
// (FTS5-only, no network), and asserts recall@k / nDCG@k / MRR thresholds so
// a ranking or recall regression fails the build. Two lifecycle invariants
// (supersession + forget) are verified on the same store using existing
// service primitives.
package eval

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

const evalK = 5

// Thresholds for FTS5-only retrieval over DefaultCorpus. These are floors:
// the harness currently scores well above them (logged on every run), so the
// margin absorbs benign BM25 tie-breaking jitter while still failing on a
// real regression (e.g. the FTS query getting double-sanitized, scope
// filtering breaking, or recall silently returning empty).
const (
	minRecallAtK = 0.90
	minNDCGAtK   = 0.85
	minMRR       = 0.85
)

// Latency ceilings for the in-memory-grade on-disk store. Generous on
// purpose — CI machines are noisy and this gate is about correctness +
// catastrophic-regression detection, not microbenchmarking. A p95 in the
// hundreds of ms would signal a genuine pathology (missing index, N+1).
const (
	maxRetrieveP95 = 250 * time.Millisecond
	maxIngestP95   = 500 * time.Millisecond
)

func TestRetrievalQualityGate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "eval.db")
	corpus := DefaultCorpus()

	metrics, retrieveLat, ingestLat, err := Evaluate(ctx, dbPath, corpus, evalK)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	t.Logf("retrieval-quality @k=%d over %d queries: recall=%.3f ndcg=%.3f mrr=%.3f",
		metrics.K, metrics.NumQueries, metrics.RecallAtK, metrics.NDCGAtK, metrics.MRR)
	t.Logf("retrieve latency: p50=%s p95=%s max=%s (n=%d)",
		retrieveLat.P50, retrieveLat.P95, retrieveLat.Max, retrieveLat.Samples)
	t.Logf("ingest latency:   p50=%s p95=%s max=%s (n=%d)",
		ingestLat.P50, ingestLat.P95, ingestLat.Max, ingestLat.Samples)

	// Per-query diagnostics so a failing gate names the regressed query.
	for _, q := range metrics.PerQuery {
		if q.RecallAtK < minRecallAtK || q.NDCGAtK < minNDCGAtK || q.RR < minMRR {
			t.Logf("UNDERPERFORMING query %q: recall=%.3f ndcg=%.3f rr=%.3f",
				q.Query, q.RecallAtK, q.NDCGAtK, q.RR)
		}
	}

	if metrics.RecallAtK < minRecallAtK {
		t.Errorf("recall@%d regression: got %.3f, want >= %.3f", evalK, metrics.RecallAtK, minRecallAtK)
	}
	if metrics.NDCGAtK < minNDCGAtK {
		t.Errorf("nDCG@%d regression: got %.3f, want >= %.3f", evalK, metrics.NDCGAtK, minNDCGAtK)
	}
	if metrics.MRR < minMRR {
		t.Errorf("MRR regression: got %.3f, want >= %.3f", metrics.MRR, minMRR)
	}
	if retrieveLat.P95 > maxRetrieveP95 {
		t.Errorf("retrieve p95 regression: got %s, want <= %s", retrieveLat.P95, maxRetrieveP95)
	}
	if ingestLat.P95 > maxIngestP95 {
		t.Errorf("ingest p95 regression: got %s, want <= %s", ingestLat.P95, maxIngestP95)
	}
}

// TestLifecycleInvariantSupersession verifies the memory__invalidate /
// supersession contract: after Service.Invalidate stamps a row as superseded
// (pointing at its replacement), the superseded row is excluded from default
// recall while the replacement surfaces — yet the bi-temporal trail is
// preserved (the superseded row stays visible to an IncludeInvalid history
// query). Uses two notes so Invalidate is the explicit, load-bearing
// primitive under test (fact-bucket auto-invalidation on Write is covered
// separately below).
func TestLifecycleInvariantSupersession(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "lifecycle.db")
	h, err := NewHarness(ctx, dbPath, Corpus{}) // empty corpus; we write directly
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	defer func() { _ = h.Close() }()

	ws := "ws-life"
	scope := store.SkillScope{WorkspaceIDs: []string{ws}}

	oldID, err := h.Service.Write(ctx, memory.WriteOptions{
		Name: "deploy-target-old", Kind: store.MemoryKindNote,
		Content:     "deploy target is the staging cluster east region",
		WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("write old note: %v", err)
	}
	newID, err := h.Service.Write(ctx, memory.WriteOptions{
		Name: "deploy-target-new", Kind: store.MemoryKindNote,
		Content:     "deploy target is the production cluster west region",
		WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("write new note: %v", err)
	}

	// memory__invalidate: mark the old note superseded by the new one.
	if err := h.Service.Invalidate(ctx, oldID, newID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	hits, err := h.Service.Recall(ctx, store.MemoryFilter{Scope: scope}, "deploy target cluster region", evalK)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, hit := range hits {
		if hit.Entry.ID == oldID {
			t.Fatalf("superseded row %s must be excluded from default recall, got it in results", oldID)
		}
	}
	if !containsID(hits, newID) {
		t.Fatalf("replacement row %s must surface in default recall; hits=%d", newID, len(hits))
	}

	// Default list also hides the superseded row.
	active, err := h.Service.List(ctx, store.MemoryFilter{Scope: scope})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	for _, r := range active {
		if r.ID == oldID {
			t.Fatalf("superseded row %s must be excluded from default list", oldID)
		}
	}

	// History view (IncludeInvalid) must still carry the superseded row, with
	// its invalidated_by pointing at the replacement — the trail is preserved.
	all, err := h.Service.List(ctx, store.MemoryFilter{Scope: scope, IncludeInvalid: true})
	if err != nil {
		t.Fatalf("List history: %v", err)
	}
	var oldRow *store.MemoryEntry
	for i := range all {
		if all[i].ID == oldID {
			oldRow = &all[i]
		}
	}
	if oldRow == nil {
		t.Fatalf("superseded row %s missing from history view", oldID)
	}
	if oldRow.TValidEnd == nil {
		t.Fatal("superseded row must have t_valid_end stamped")
	}
	if oldRow.InvalidatedBy != newID {
		t.Fatalf("invalidated_by should point at replacement %s, got %q", newID, oldRow.InvalidatedBy)
	}

	// Fact-bucket auto-invalidation: a second fact write in the same
	// (workspace, worker, name) bucket supersedes the prior active fact, so
	// default recall surfaces exactly one — the new value.
	const factName = "primary-region"
	if _, err := h.Service.Write(ctx, memory.WriteOptions{
		Name: factName, Kind: store.MemoryKindFact, Content: "primary region is us-east-1",
		WorkspaceID: &ws, WorkerID: "w1",
	}); err != nil {
		t.Fatalf("write fact v1: %v", err)
	}
	factNewID, err := h.Service.Write(ctx, memory.WriteOptions{
		Name: factName, Kind: store.MemoryKindFact, Content: "primary region is eu-west-2",
		WorkspaceID: &ws, WorkerID: "w1",
	})
	if err != nil {
		t.Fatalf("write fact v2: %v", err)
	}
	factActive, err := h.Service.List(ctx, store.MemoryFilter{
		Scope: scope, Kind: store.MemoryKindFact, Name: factName,
	})
	if err != nil {
		t.Fatalf("List fact active: %v", err)
	}
	if len(factActive) != 1 || factActive[0].ID != factNewID {
		t.Fatalf("fact bucket should leave exactly the new active row, got %d rows", len(factActive))
	}
}

// TestLifecycleInvariantForget verifies that after Service.Forget (the
// memory__forget primitive), the row returns nothing from recall, list, or
// get — a hard exclusion, not just a default-view hide.
func TestLifecycleInvariantForget(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget.db")
	h, err := NewHarness(ctx, dbPath, Corpus{})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	defer func() { _ = h.Close() }()

	id, err := h.Service.Write(ctx, memory.WriteOptions{
		Name: "transient", Kind: store.MemoryKindNote,
		Content: "this debugging note about the flaky websocket reconnect is temporary",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// Sanity: it is recallable before forget.
	pre, err := h.Service.Recall(ctx, store.MemoryFilter{}, "websocket reconnect flaky", evalK)
	if err != nil {
		t.Fatalf("pre-forget recall: %v", err)
	}
	if !containsID(pre, id) {
		t.Fatalf("row %s should be recallable before forget", id)
	}

	if err := h.Service.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	post, err := h.Service.Recall(ctx, store.MemoryFilter{}, "websocket reconnect flaky", evalK)
	if err != nil {
		t.Fatalf("post-forget recall: %v", err)
	}
	if containsID(post, id) {
		t.Fatalf("forgotten row %s must not surface in recall", id)
	}

	// Default list excludes it too.
	rows, err := h.Service.List(ctx, store.MemoryFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Fatalf("forgotten row %s must not appear in default list", id)
		}
	}
}

func containsID(hits []store.MemoryHit, id string) bool {
	for _, h := range hits {
		if h.Entry.ID == id {
			return true
		}
	}
	return false
}
