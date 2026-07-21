package embedding

import (
	"context"
	"math"
	"sort"
	"sync"
)

type EmbedProvider interface {
	Embed(ctx context.Context, inputs []string) (vectors [][]float32, model string, err error)
	HasModel() bool
}

type NoopEmbedder struct{}

func (NoopEmbedder) Embed(_ context.Context, _ []string) ([][]float32, string, error) {
	return nil, "", nil
}

func (NoopEmbedder) HasModel() bool { return false }

type HybridIndex struct {
	mu       sync.RWMutex
	tfidf    *Index
	embedder EmbedProvider
	docs     []Document
	entries  []storeEntry
}

type storeEntry struct {
	ID     string
	Text   string
	Vector []float32
}

func NewHybrid(docs []Document, embedder EmbedProvider) *HybridIndex {
	h := &HybridIndex{
		docs:     docs,
		embedder: embedder,
	}

	h.tfidf = NewIndex(docs)

	if embedder != nil && embedder.HasModel() && len(docs) > 0 {
		texts := make([]string, len(docs))
		for i, d := range docs {
			texts[i] = d.Text
		}
		vecs, model, _ := embedder.Embed(context.Background(), texts)
		if len(vecs) == len(docs) {
			h.entries = make([]storeEntry, len(docs))
			for i, d := range docs {
				h.entries[i] = storeEntry{ID: d.ID, Text: d.Text, Vector: vecs[i]}
			}
			_ = model
		}
	}

	if len(h.entries) == 0 {
		h.entries = make([]storeEntry, len(docs))
		for i, d := range docs {
			h.entries[i] = storeEntry{ID: d.ID, Text: d.Text}
		}
	}

	return h
}

func (h *HybridIndex) Search(query string, limit int) []HybridHit {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	tfidfHits := h.tfidf.Search(query, limit*2)

	if h.embedder != nil && h.embedder.HasModel() && len(h.entries) > 0 {
		queryVecs, _, err := h.embedder.Embed(context.Background(), []string{query})
		if err == nil && len(queryVecs) > 0 {
			vecHits := h.searchVector(queryVecs[0], limit*2)
			return h.fuseResults(tfidfHits, vecHits, limit)
		}
	}

	if len(tfidfHits) > limit {
		tfidfHits = tfidfHits[:limit]
	}
	hits := make([]HybridHit, len(tfidfHits))
	for i, r := range tfidfHits {
		hits[i] = HybridHit(r)
	}
	return hits
}

func (h *HybridIndex) searchVector(query []float32, limit int) []HybridHit {
	type scored struct {
		id    string
		idx   int
		score float64
	}
	var results []scored

	h.mu.RLock()
	defer h.mu.RUnlock()

	for i, v := range h.entries {
		if len(v.Vector) == 0 || len(query) == 0 || len(v.Vector) != len(query) {
			continue
		}
		sim := cosineSimFloat(query, v.Vector)
		if sim > 0 {
			results = append(results, scored{id: v.ID, idx: i, score: sim})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	hits := make([]HybridHit, len(results))
	for i, r := range results {
		hits[i] = HybridHit{ID: r.id, Score: r.score}
	}
	return hits
}

func (h *HybridIndex) fuseResults(tfidfHits []SearchResult, vecHits []HybridHit, limit int) []HybridHit {
	tfidfMap := make(map[string]float64, len(tfidfHits))
	for _, r := range tfidfHits {
		tfidfMap[r.ID] = r.Score
	}

	fused := make([]HybridHit, 0, len(vecHits)+len(tfidfHits))
	seen := make(map[string]bool)

	for _, vh := range vecHits {
		if seen[vh.ID] {
			continue
		}
		seen[vh.ID] = true
		tfidfScore := tfidfMap[vh.ID]
		fusedScore := fuseScore(tfidfScore, vh.Score)
		fused = append(fused, HybridHit{ID: vh.ID, Score: fusedScore})
	}

	for _, th := range tfidfHits {
		if !seen[th.ID] {
			seen[th.ID] = true
			fused = append(fused, HybridHit(th))
		}
	}

	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})

	if len(fused) > limit {
		fused = fused[:limit]
	}
	return fused
}

func cosineSimFloat(a, b []float32) float64 {
	var dot, normA, normB float64
	for i, va := range a {
		normA += float64(va) * float64(va)
		if i < len(b) {
			dot += float64(va) * float64(b[i])
		}
	}
	for _, vb := range b {
		normB += float64(vb) * float64(vb)
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func fuseScore(tfidf, vector float64) float64 {
	if tfidf <= 0 {
		return vector
	}
	if vector <= 0 {
		return tfidf
	}
	return (tfidf + vector) / 2
}

type HybridHit struct {
	ID    string
	Score float64
}
