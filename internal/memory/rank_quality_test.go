// rank_quality_test.go — service-layer coverage for the ranking-quality
// wave: weighted RRF, cross-encoder rerank wiring, stored-vector consumers
// (SpreadingActivation / SuggestionsFor), and the drain-on-shutdown Close.
package memory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// fakeEmbedder returns a deterministic 1536-dim vector derived from the
// input so two identical strings embed identically and the vec path is
// exercised end-to-end against the real sqlite-vec store.
type fakeEmbedder struct{ model string }

func (f fakeEmbedder) HasModel() bool { return true }

func (f fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, string, error) {
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		v := make([]float32, memory.EmbedDim)
		// Spread a few characters across the vector so distinct inputs get
		// distinct (but stable) embeddings.
		for j := 0; j < len(in) && j < memory.EmbedDim; j++ {
			v[j] = float32(in[j]) / 255.0
		}
		// Ensure non-zero magnitude even for short inputs.
		v[memory.EmbedDim-1] = 0.5
		out[i] = v
	}
	return out, f.model, nil
}

// fakeReranker scores docs by a configured preference: the doc whose
// content contains preferToken gets the top score regardless of input
// order, so a test can prove the cross-encoder reorders the pool.
type fakeReranker struct {
	preferToken string
	calls       int
}

func (r *fakeReranker) HasModel() bool { return true }

func (r *fakeReranker) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	r.calls++
	scores := make([]float64, len(docs))
	for i, d := range docs {
		if strings.Contains(d, r.preferToken) {
			scores[i] = 1.0
		} else {
			scores[i] = 0.1
		}
	}
	return scores, nil
}

func newVecSvc(t *testing.T) (*memory.Service, *sqlite.DB, fakeEmbedder) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	emb := fakeEmbedder{model: "fake-1536"}
	svc := memory.NewService(d, emb, nil)
	return svc, d, emb
}

// writeWithEmbedding writes a memory and synchronously stores its
// embedding (the production path embeds async; tests need it deterministic).
func writeWithEmbedding(t *testing.T, ctx context.Context, svc *memory.Service, d *sqlite.DB, emb fakeEmbedder, name, content string) string {
	t.Helper()
	id, err := svc.Write(ctx, memory.WriteOptions{Name: name, Content: content})
	if err != nil {
		t.Fatalf("Write %s: %v", name, err)
	}
	vecs, model, err := emb.Embed(ctx, []string{content})
	if err != nil || len(vecs) == 0 {
		t.Fatalf("embed %s: %v", name, err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, id, model, 1, vecs[0]); err != nil {
		t.Fatalf("upsert embedding %s: %v", name, err)
	}
	return id
}

// TestCrossEncoderRerankReordersPool proves the cross-encoder rerank
// stage runs on the fused/embedder path and reorders the result so the
// reranker's preferred doc surfaces first.
func TestCrossEncoderRerankReordersPool(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)
	rr := &fakeReranker{preferToken: "calibration"}
	svc.SetReranker(rr)

	writeWithEmbedding(t, ctx, svc, d, emb,
		"alpha", "probe alignment notes for the sensor array")
	wantID := writeWithEmbedding(t, ctx, svc, d, emb,
		"beta", "probe calibration requires a ten minute warmup")

	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "probe", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if rr.calls == 0 {
		t.Fatal("reranker was never invoked on the embedder path")
	}
	if len(hits) == 0 || hits[0].Entry.ID != wantID {
		t.Fatalf("cross-encoder should surface the preferred doc first, got %+v", hits)
	}
}

// TestCrossEncoderOrderSurvivesRecency is the HIGH-1 regression: the
// cross-encoder relevance order is the PRIMARY ranking key; recency + pin are
// a BOUNDED tie-breaker that cannot leap a clearly-more-relevant hit. We make
// a STALE doc the cross-encoder's #1 (it carries the preferToken) and a FRESH
// doc its #2, then assert the stale doc still ranks first after foldRecencyPin
// — a fresh-but-less-relevant doc must NOT jump it. Before the fix, recency's
// ~30x multiplier dominated the ~1.6x position spread and the fresh doc won.
func TestCrossEncoderOrderSurvivesRecency(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)
	rr := &fakeReranker{preferToken: "calibration"}
	svc.SetReranker(rr)

	now := time.Now().UTC()
	// STALE but most-relevant (carries the preferToken → cross-encoder #1).
	staleID := writeStaleWithEmbedding(t, ctx, svc, d, emb,
		"stale", "probe calibration requires a ten minute warmup",
		now.Add(-365*24*time.Hour))
	// FRESH but less-relevant (no preferToken → cross-encoder #2).
	freshID := writeStaleWithEmbedding(t, ctx, svc, d, emb,
		"fresh", "probe alignment notes for the sensor array", now)

	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "probe", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if rr.calls == 0 {
		t.Fatal("reranker was never invoked on the embedder path")
	}
	if len(hits) < 2 {
		t.Fatalf("expected both docs in the result, got %d: %+v", len(hits), hits)
	}
	if hits[0].Entry.ID != staleID {
		t.Fatalf("stale-but-most-relevant doc must stay #1; got #1=%s (stale=%s fresh=%s)",
			hits[0].Entry.ID, staleID, freshID)
	}
	if hits[1].Entry.ID != freshID {
		t.Fatalf("fresh-but-less-relevant doc must stay #2; got #2=%s", hits[1].Entry.ID)
	}
}

// writeStaleWithEmbedding writes a memory directly via the store with an
// explicit UpdatedAt (the service path stamps wall-clock now), then upserts
// its embedding, so a test can control recency deterministically.
func writeStaleWithEmbedding(
	t *testing.T, ctx context.Context, _ *memory.Service, d *sqlite.DB,
	emb fakeEmbedder, name, content string, updatedAt time.Time,
) string {
	t.Helper()
	e := &store.MemoryEntry{
		Name: name, Kind: store.MemoryKindNote, Content: content,
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := d.WriteMemory(ctx, e); err != nil {
		t.Fatalf("WriteMemory %s: %v", name, err)
	}
	vecs, model, err := emb.Embed(ctx, []string{content})
	if err != nil || len(vecs) == 0 {
		t.Fatalf("embed %s: %v", name, err)
	}
	if err := d.UpsertMemoryEmbedding(ctx, e.ID, model, 1, vecs[0]); err != nil {
		t.Fatalf("upsert embedding %s: %v", name, err)
	}
	return e.ID
}

// TestWeightedRRFTunableNoCrash ensures SetFusionWeights is honoured and
// the weighted fusion path still returns hits (no regression / panic).
func TestWeightedRRFTunableNoCrash(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)
	svc.SetFusionWeights(2.0, 0.5)

	id := writeWithEmbedding(t, ctx, svc, d, emb,
		"go", "go is a statically typed compiled language")
	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "language", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var found bool
	for _, h := range hits {
		if h.Entry.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("weighted RRF dropped the matching hit: %+v", hits)
	}
}

// TestSuggestionsForSemanticUsesStoredVector covers item 6 on the
// SuggestionsFor side: with a stored embedding present, the semantic axis
// surfaces a vec-neighbour without re-embedding (the embedder would still
// work, but we assert the stored-vector path returns neighbours).
func TestSuggestionsForSemanticUsesStoredVector(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)

	seedID := writeWithEmbedding(t, ctx, svc, d, emb,
		"seed", "the quick brown fox jumps over the lazy dog today")
	neighID := writeWithEmbedding(t, ctx, svc, d, emb,
		"neigh", "the quick brown fox jumps over the lazy dog tonight")

	sugs, err := svc.SuggestionsFor(ctx, seedID, store.SkillScope{}, 10)
	if err != nil {
		t.Fatalf("SuggestionsFor: %v", err)
	}
	var sawSemantic bool
	for _, s := range sugs {
		if s.MemoryID == neighID && s.Source == "semantic" {
			sawSemantic = true
		}
	}
	if !sawSemantic {
		t.Fatalf("expected semantic neighbour %s via stored vector, got %+v", neighID, sugs)
	}
}

// TestSpreadingActivationUsesStoredVector covers item 6 on the
// SpreadingActivation side: the seed's stored embedding (not a re-embed)
// drives the KNN. We link a seed + a neighbour to two distinct entities,
// store both embeddings, and assert spreading from the seed's entity
// surfaces the neighbour's entity.
func TestSpreadingActivationUsesStoredVector(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)

	seedID := writeWithEmbedding(t, ctx, svc, d, emb,
		"seed", "the quick brown fox jumps over the lazy dog now")
	neighID := writeWithEmbedding(t, ctx, svc, d, emb,
		"neigh", "the quick brown fox jumps over the lazy dog soon")

	seedEnt := store.EntityRef{Kind: "task", ID: "SEED"}
	neighEnt := store.EntityRef{Kind: "task", ID: "NEIGH"}
	if err := svc.LinkEntity(ctx, seedID, seedEnt, ""); err != nil {
		t.Fatalf("LinkEntity seed: %v", err)
	}
	if err := svc.LinkEntity(ctx, neighID, neighEnt, ""); err != nil {
		t.Fatalf("LinkEntity neigh: %v", err)
	}

	out, err := svc.SpreadingActivation(ctx, seedEnt, store.SkillScope{}, 10, 20, 8)
	if err != nil {
		t.Fatalf("SpreadingActivation: %v", err)
	}
	var found bool
	for _, e := range out {
		// The store normalizes entity IDs to lowercase; compare case-insensitively.
		if strings.EqualFold(e.Kind, neighEnt.Kind) && strings.EqualFold(e.ID, neighEnt.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected neighbour entity %s:%s via stored-vector KNN, got %+v",
			neighEnt.Kind, neighEnt.ID, out)
	}
}

// TestCloseDrainsRecallEvents covers item 7: enqueue N<batch recall events
// through the async path, Close, and assert the rows landed (Close drained
// + persisted the final partial batch).
func TestCloseDrainsRecallEvents(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	svc.EnableAsyncRecallTrackingForTest()

	idA, err := svc.Write(ctx, memory.WriteOptions{Name: "a", Content: "go is a great language"})
	if err != nil {
		t.Fatalf("Write A: %v", err)
	}
	idB, err := svc.Write(ctx, memory.WriteOptions{Name: "b", Content: "rust is a great language"})
	if err != nil {
		t.Fatalf("Write B: %v", err)
	}

	// One recall enqueues a small (<batch) set of events onto the async
	// channel; they have not necessarily been flushed yet.
	if _, err := svc.Recall(ctx, store.MemoryFilter{}, "language", 10); err != nil {
		t.Fatalf("Recall: %v", err)
	}

	// Close must drain the buffered events and persist the final batch.
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := svc.Close(); err != nil {
		t.Fatalf("Close (2nd): %v", err)
	}

	// Give the flush loop a beat to run its stopCh drain branch.
	deadline := time.Now().Add(2 * time.Second)
	var co []store.CoRecalledMemory
	for time.Now().Before(deadline) {
		co, err = svc.CoRecalled(ctx, idA, store.SkillScope{}, 10)
		if err != nil {
			t.Fatalf("CoRecalled: %v", err)
		}
		if len(co) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var found bool
	for _, c := range co {
		if c.MemoryID == idB {
			found = true
		}
	}
	if !found {
		t.Fatalf("Close did not drain+persist recall events: co-recall for %s empty (%+v)", idA, co)
	}
}

// TestReEmbedOnContentEdit proves wave-B2 item 3 via the PRODUCTION path:
// the brain Editor/Indexer persist an edit through store.UpdateMemory (which
// drops the stale memories_vec row on a content change) and then fire their
// ReEmbed hook — wired to memory.Service.ReEmbedAfterUpdate — so the dropped
// vector is REBUILT from the NEW content and KNN finds the row by its new
// meaning (not the old one). Before this wiring the row stayed FTS-only
// forever after an edit. (Service.UpdateMemory, exercised here previously, was
// dead production code — no caller — and was removed; the hook is the real
// shipping path.)
func TestReEmbedOnContentEdit(t *testing.T) {
	ctx := context.Background()
	svc, d, emb := newVecSvc(t)

	// Write + embed the original content.
	id := writeWithEmbedding(t, ctx, svc, d, emb,
		"note", "the original content is about sensor calibration")

	// Sanity: KNN for the OLD content finds it now.
	oldVec, model, _ := emb.Embed(ctx, []string{"the original content is about sensor calibration"})
	pre, err := d.VectorSearchMemories(ctx, store.MemoryFilter{}, model, oldVec[0], 5)
	if err != nil {
		t.Fatalf("pre KNN: %v", err)
	}
	if len(pre) == 0 || pre[0].Entry.ID != id {
		t.Fatalf("pre-edit KNN should find the memory by old content, got %+v", pre)
	}

	// Edit the content to a clearly different topic through the production
	// edit path: store.UpdateMemory drops the stale vector, then the brain's
	// ReEmbed hook (= Service.ReEmbedAfterUpdate) rebuilds it.
	const newContent = "the replacement content is about telescope alignment optics"
	row, err := d.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	row.Content = newContent
	if err := d.UpdateMemory(ctx, row); err != nil {
		t.Fatalf("store.UpdateMemory: %v", err)
	}
	svc.ReEmbedAfterUpdate(ctx, row)

	// The re-embed is async; poll a KNN for the NEW content until the memory
	// surfaces with its repopulated vector.
	newVec, _, _ := emb.Embed(ctx, []string{newContent})
	deadline := time.Now().Add(3 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		hits, herr := d.VectorSearchMemories(ctx, store.MemoryFilter{}, model, newVec[0], 5)
		if herr != nil {
			t.Fatalf("post KNN: %v", herr)
		}
		for _, h := range hits {
			if h.Entry.ID == id {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("re-embed did not repopulate the vector: KNN for new content "+
			"did not surface %s", id)
	}

	// And the stored model is set again (the edit cleared it, the re-embed
	// restores it) — confirms the vector was rebuilt, not merely searched.
	gotModel, vec, gerr := d.GetMemoryEmbedding(ctx, id)
	if gerr != nil || len(vec) == 0 || gotModel != model {
		t.Fatalf("expected repopulated vector under model %q, got model=%q len=%d err=%v",
			model, gotModel, len(vec), gerr)
	}
}
