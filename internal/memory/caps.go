package memory

// HasEmbedder reports whether a non-noop embedding provider is wired
// (i.e. vector search / spreading activation can actually produce
// results). Nil-safe.
func (s *Service) HasEmbedder() bool {
	return s != nil && s.embedder != nil && s.embedder.HasModel()
}

// RecallTrackingEnabled reports whether AR4 recall-event logging is on
// (MCPLEXER_RECALL_TRACKING=1 at construction, or the test hook).
// Nil-safe.
func (s *Service) RecallTrackingEnabled() bool {
	return s != nil && s.recallEnabled
}
