package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// Harness owns a real on-disk SQLite store + a memory.Service wired with the
// default NoopEmbedder, so retrieval exercises the FTS5 floor end-to-end
// with no network and no API key. It tracks the fixture-key ↔ store-id
// mapping so query labels (which reference stable keys) can be scored
// against the ids the store actually assigns.
type Harness struct {
	Service *memory.Service
	Store   *sqlite.DB

	embedder memory.EmbedProvider

	keyToID map[string]string
	idToKey map[string]string

	// ingestLatencies records the wall time of each Write so the gate can
	// report ingest p50/p95 alongside retrieval latency.
	ingestLatencies []time.Duration
}

// NewHarness opens a fresh on-disk SQLite store at dbPath and seeds it with
// the given corpus, using the default NoopEmbedder (FTS5-only retrieval — no
// network, no API key). The caller is responsible for the path's lifecycle (a
// test typically uses t.TempDir()). The store handle is returned on the
// Harness so the caller can Close it.
func NewHarness(ctx context.Context, dbPath string, c Corpus) (*Harness, error) {
	return NewHarnessWithEmbedder(ctx, dbPath, c, memory.NoopEmbedder{})
}

// NewHarnessWithEmbedder is NewHarness with an explicit embedding provider.
// When emb.HasModel() is true the harness synchronously stores a vector for
// every seeded document, so Recall takes the FUSED path (FTS5 + vector KNN
// through rrfFuse) that production runs — the NoopEmbedder harness returns
// early and never exercises fusion at all. Pass a deterministic offline
// provider; this package must never make a network call.
func NewHarnessWithEmbedder(
	ctx context.Context, dbPath string, c Corpus, emb memory.EmbedProvider,
) (*Harness, error) {
	db, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store at %s: %w", dbPath, err)
	}
	h := &Harness{
		Service:  memory.NewService(db, emb, nil),
		Store:    db,
		embedder: emb,
		keyToID:  make(map[string]string, len(c.Memories)),
		idToKey:  make(map[string]string, len(c.Memories)),
	}
	if err := h.seed(ctx, c); err != nil {
		_ = db.Close()
		return nil, err
	}
	return h, nil
}

// seed writes every fixture memory, timing each ingest and recording the
// key↔id mapping. Fixtures with a zero UpdatedAt go through
// memory.Service.Write (the full production write path); backdated fixtures
// go straight to the store, which is the only layer that honours a
// caller-supplied timestamp.
func (h *Harness) seed(ctx context.Context, c Corpus) error {
	for _, m := range c.Memories {
		start := time.Now()
		id, err := h.seedOne(ctx, m)
		h.ingestLatencies = append(h.ingestLatencies, time.Since(start))
		if err != nil {
			return fmt.Errorf("seed memory %q: %w", m.Key, err)
		}
		h.keyToID[m.Key] = id
		h.idToKey[id] = m.Key
	}
	return nil
}

// seedOne writes a single fixture and returns its store-assigned id.
func (h *Harness) seedOne(ctx context.Context, m FixtureMemory) (string, error) {
	if m.UpdatedAt.IsZero() {
		id, err := h.Service.Write(ctx, memory.WriteOptions{
			Name: m.Name, Kind: store.MemoryKindNote,
			Content: m.Content, Tags: m.Tags, Pinned: m.Pinned,
		})
		if err != nil {
			return "", err
		}
		return id, h.embedOne(ctx, id, m)
	}
	e := &store.MemoryEntry{
		Name: m.Name, Kind: store.MemoryKindNote, Content: m.Content,
		TagsJSON: tagsJSON(m.Tags), Pinned: m.Pinned,
		CreatedAt: m.UpdatedAt, UpdatedAt: m.UpdatedAt, TValidStart: m.UpdatedAt,
	}
	if err := h.Store.WriteMemory(ctx, e); err != nil {
		return "", err
	}
	if err := h.embedOne(ctx, e.ID, m); err != nil {
		return "", err
	}
	// (There used to be a workaround here restoring updated_at after the
	// embedding upsert, because UpsertMemoryEmbedding stamped updated_at =
	// now() and silently un-backdated every row on the fused path. That defect
	// is fixed at its source in (*sqlite.DB).UpsertMemoryEmbedding, which no
	// longer writes updated_at at all — see
	// TestUpsertMemoryEmbeddingPreservesUpdatedAt. The workaround is gone with
	// it; if backdating ever breaks again, suspect the store, not the harness.)
	return e.ID, nil
}

// embedOne synchronously stores the document vector when a real provider is
// configured. The production write path embeds asynchronously, which a
// deterministic gate cannot race against.
func (h *Harness) embedOne(ctx context.Context, id string, m FixtureMemory) error {
	if !h.hasVectors() {
		return nil
	}
	vecs, model, err := h.embedder.Embed(ctx, []string{m.Content})
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(vecs) == 0 || model == "" {
		return fmt.Errorf("embed: provider returned no vector")
	}
	return h.Store.UpsertMemoryEmbedding(ctx, id, model, 1, vecs[0])
}

func (h *Harness) hasVectors() bool {
	return h.embedder != nil && h.embedder.HasModel()
}

// tagsJSON renders fixture tags into the store's tags_json column format.
// Returns nil for no tags so the column stays NULL, matching Service.Write.
func tagsJSON(tags []string) json.RawMessage {
	if len(tags) == 0 {
		return nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// ID returns the store-assigned id for a fixture key, or "" if unseeded.
func (h *Harness) ID(key string) string { return h.keyToID[key] }

// Key returns the fixture key for a store-assigned id, or "" if the id is
// outside the seeded fixture space.
func (h *Harness) Key(id string) string { return h.idToKey[id] }

// UseReranker installs a cross-encoder rerank provider on the harness service.
// Without it memory.NewService always wires NoopReranker (HasModel() == false),
// so Recall never takes the crossEncoderReorder → foldRecencyPin branch and
// that whole path is invisible to this package. Reranking only affects Recall,
// never seeding, so installing it after construction is equivalent to
// constructing with it. Pass a deterministic offline provider — this package
// must never make a network call.
func (h *Harness) UseReranker(r memory.RerankProvider) {
	if h == nil || h.Service == nil {
		return
	}
	h.Service.SetReranker(r)
}

// RunQueries executes every fixture query through Service.Recall at cutoff
// k, translating the returned memory ids back into fixture keys for scoring.
// It also returns the per-query retrieval latency sample.
func (h *Harness) RunQueries(
	ctx context.Context, c Corpus, k int,
) ([]RankedResult, []time.Duration, error) {
	results := make([]RankedResult, 0, len(c.Queries))
	latencies := make([]time.Duration, 0, len(c.Queries))
	for _, q := range c.Queries {
		start := time.Now()
		hits, err := h.Service.Recall(ctx, store.MemoryFilter{}, q.Query, k)
		latencies = append(latencies, time.Since(start))
		if err != nil {
			return nil, nil, fmt.Errorf("recall %q: %w", q.Query, err)
		}
		ranked := make([]string, 0, len(hits))
		for _, hit := range hits {
			if key, ok := h.idToKey[hit.Entry.ID]; ok {
				ranked = append(ranked, key)
			} else {
				// An id outside the fixture space (should not happen on a
				// freshly-seeded store) — record a sentinel so it occupies a
				// rank slot and pushes relevant hits down, matching how a
				// real off-target result would score.
				ranked = append(ranked, "__unknown__")
			}
		}
		results = append(results, RankedResult{
			Query:        q.Query,
			RelevantKeys: q.relevantSet(),
			RankedKeys:   ranked,
		})
	}
	return results, latencies, nil
}

// IngestLatency summarizes the seed-time write latencies.
func (h *Harness) IngestLatency() LatencyReport {
	return summarizeLatency(h.ingestLatencies)
}

// Close drains the service's background recall loop and releases the store.
// Closing the Service matters once a test binary builds several harnesses:
// memory.NewService always starts a flush/drain goroutine that only
// Service.Close terminates.
func (h *Harness) Close() error {
	if h == nil {
		return nil
	}
	if h.Service != nil {
		_ = h.Service.Close()
	}
	if h.Store == nil {
		return nil
	}
	return h.Store.Close()
}

// Evaluate is the one-call convenience used by the CI gate: seed, run, and
// return the metric + latency reports. dbPath should be a caller-owned temp
// path. The harness is closed before returning.
func Evaluate(ctx context.Context, dbPath string, c Corpus, k int) (MetricReport, LatencyReport, LatencyReport, error) {
	return EvaluateWith(ctx, dbPath, c, k, memory.NoopEmbedder{})
}

// EvaluateWith is Evaluate with an explicit embedding provider, so a scenario
// can choose the FTS5-only path (NoopEmbedder) or the fused FTS+vector path.
func EvaluateWith(
	ctx context.Context, dbPath string, c Corpus, k int, emb memory.EmbedProvider,
) (MetricReport, LatencyReport, LatencyReport, error) {
	h, err := NewHarnessWithEmbedder(ctx, dbPath, c, emb)
	if err != nil {
		return MetricReport{}, LatencyReport{}, LatencyReport{}, err
	}
	defer func() { _ = h.Close() }()

	results, latencies, err := h.RunQueries(ctx, c, k)
	if err != nil {
		return MetricReport{}, LatencyReport{}, LatencyReport{}, err
	}
	return Aggregate(results, k), summarizeLatency(latencies), h.IngestLatency(), nil
}
