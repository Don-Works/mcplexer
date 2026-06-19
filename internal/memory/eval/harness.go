package eval

import (
	"context"
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

	keyToID map[string]string
	idToKey map[string]string

	// ingestLatencies records the wall time of each Write so the gate can
	// report ingest p50/p95 alongside retrieval latency.
	ingestLatencies []time.Duration
}

// NewHarness opens a fresh on-disk SQLite store at dbPath and seeds it with
// the given corpus. The caller is responsible for the path's lifecycle (a
// test typically uses t.TempDir()). The store handle is returned on the
// Harness so the caller can Close it.
func NewHarness(ctx context.Context, dbPath string, c Corpus) (*Harness, error) {
	db, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store at %s: %w", dbPath, err)
	}
	h := &Harness{
		Service: memory.NewService(db, memory.NoopEmbedder{}, nil),
		Store:   db,
		keyToID: make(map[string]string, len(c.Memories)),
		idToKey: make(map[string]string, len(c.Memories)),
	}
	if err := h.seed(ctx, c); err != nil {
		_ = db.Close()
		return nil, err
	}
	return h, nil
}

// seed writes every fixture memory, timing each ingest and recording the
// key↔id mapping.
func (h *Harness) seed(ctx context.Context, c Corpus) error {
	for _, m := range c.Memories {
		start := time.Now()
		id, err := h.Service.Write(ctx, memory.WriteOptions{
			Name:    m.Name,
			Kind:    store.MemoryKindNote,
			Content: m.Content,
			Tags:    m.Tags,
		})
		h.ingestLatencies = append(h.ingestLatencies, time.Since(start))
		if err != nil {
			return fmt.Errorf("seed memory %q: %w", m.Key, err)
		}
		h.keyToID[m.Key] = id
		h.idToKey[id] = m.Key
	}
	return nil
}

// ID returns the store-assigned id for a fixture key, or "" if unseeded.
func (h *Harness) ID(key string) string { return h.keyToID[key] }

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

// Close releases the underlying store.
func (h *Harness) Close() error {
	if h == nil || h.Store == nil {
		return nil
	}
	return h.Store.Close()
}

// Evaluate is the one-call convenience used by the CI gate: seed, run, and
// return the metric + latency reports. dbPath should be a caller-owned temp
// path. The harness is closed before returning.
func Evaluate(ctx context.Context, dbPath string, c Corpus, k int) (MetricReport, LatencyReport, LatencyReport, error) {
	h, err := NewHarness(ctx, dbPath, c)
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
