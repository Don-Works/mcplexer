package memory

import "context"

// HasEmbedder reports whether a non-noop embedding provider is wired
// (i.e. vector search / spreading activation can actually produce
// results). Nil-safe.
func (s *Service) HasEmbedder() bool {
	return s != nil && s.embedder != nil && s.embedder.HasModel()
}

// EmbedQuery returns the embedding vector for a single query string plus
// the model name used. Returns (nil, "", nil) when no embedder is wired
// (graceful degradation — callers fall back to lexical/TF-IDF ranking).
// Used by the audit-search vector rerank tier. Nil-safe.
func (s *Service) EmbedQuery(ctx context.Context, query string) ([]float32, string, error) {
	if !s.HasEmbedder() {
		return nil, "", nil
	}
	vecs, model, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, "", err
	}
	if len(vecs) == 0 {
		return nil, model, nil
	}
	return vecs[0], model, nil
}

// EmbedDocs returns embedding vectors for a batch of documents in input
// order, plus the model name used. Returns (nil, "", nil) when no embedder
// is wired. Used by the audit-search vector rerank tier to embed the
// candidate pool in one round-trip. Nil-safe.
func (s *Service) EmbedDocs(ctx context.Context, docs []string) ([][]float32, string, error) {
	if !s.HasEmbedder() {
		return nil, "", nil
	}
	if len(docs) == 0 {
		return nil, "", nil
	}
	return s.embedder.Embed(ctx, docs)
}

// RecallTrackingEnabled reports whether AR4 recall-event logging is on
// (MCPLEXER_RECALL_TRACKING=1 at construction, or the test hook).
// Nil-safe.
func (s *Service) RecallTrackingEnabled() bool {
	return s != nil && s.recallEnabled
}
