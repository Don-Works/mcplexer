// backfill.go — embeddings backfill. When a vector provider is first wired
// (auto-detected at boot, or configured via the dashboard), the existing
// memory corpus has no vectors and stays invisible to semantic recall.
// Backfill walks every memory lacking a vector and embeds it, so old
// memories become searchable without the user re-saving anything.
//
// Idempotent + resumable: ListMemoriesNeedingEmbedding only returns rows
// without a vector, so a re-run picks up where a previous one stopped (or
// was interrupted by a restart). Progress is published lock-free for the
// dashboard.
package memory

import (
	"context"
	"errors"
)

// BackfillState is a snapshot of embeddings-backfill progress.
type BackfillState struct {
	// EmbedderActive reports whether a real (non-noop) vector provider is
	// wired — backfill is meaningless without one.
	EmbedderActive bool `json:"embedder_active"`
	// Running is true while a backfill goroutine is in flight.
	Running bool `json:"running"`
	// Pending = active memories that still lack a vector.
	Pending int `json:"pending"`
	// Embedded = active memories that already have a vector (total-pending).
	Embedded int `json:"embedded"`
	// Total = active memories.
	Total int `json:"total"`
}

// BackfillStatus returns the live backfill progress. Cheap: two COUNT
// queries plus an atomic load.
func (s *Service) BackfillStatus(ctx context.Context) BackfillState {
	if s == nil || s.store == nil {
		return BackfillState{}
	}
	pending, total, err := s.store.CountMemoriesNeedingEmbedding(ctx)
	if err != nil {
		return BackfillState{
			EmbedderActive: s.EmbedderActive(),
			Running:        s.backfillRunning.Load(),
		}
	}
	return BackfillState{
		EmbedderActive: s.EmbedderActive(),
		Running:        s.backfillRunning.Load(),
		Pending:        pending,
		Embedded:       total - pending,
		Total:          total,
	}
}

// StartBackfillAsync kicks off a backfill in the background if one isn't
// already running and a vector provider is wired. Returns true if it
// started a new run. Safe to call on every boot — it no-ops when there's
// nothing to do.
func (s *Service) StartBackfillAsync(ctx context.Context) bool {
	if s == nil || s.store == nil || !s.EmbedderActive() {
		return false
	}
	if !s.backfillRunning.CompareAndSwap(false, true) {
		return false // already running
	}
	go func() {
		defer s.backfillRunning.Store(false)
		_, _ = s.BackfillEmbeddings(ctx, 0)
	}()
	return true
}

// BackfillEmbeddings embeds every memory that lacks a vector, in batches,
// until none remain (or ctx is cancelled). Returns the number embedded.
// Per-row embedding failures are skipped, not fatal — they'll be retried
// on the next run. The no-progress guard breaks the loop if an entire
// batch fails (e.g. the endpoint went away mid-run) so we never spin.
func (s *Service) BackfillEmbeddings(ctx context.Context, batch int) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("memory: service not initialised")
	}
	emb := s.getEmbedder()
	if !emb.HasModel() {
		return 0, errors.New("memory: no embedding provider configured")
	}
	if batch <= 0 {
		batch = 64
	}
	if pending, total, err := s.store.CountMemoriesNeedingEmbedding(ctx); err == nil {
		s.backfillTotal.Store(int64(total))
		s.backfillDone.Store(int64(total - pending))
	}
	done := 0
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		targets, err := s.store.ListMemoriesNeedingEmbedding(ctx, batch)
		if err != nil {
			return done, err
		}
		if len(targets) == 0 {
			return done, nil
		}
		progressed := 0
		for _, t := range targets {
			vecs, model, err := emb.Embed(ctx, []string{t.Content})
			if err != nil || len(vecs) == 0 {
				continue
			}
			if err := s.store.UpsertMemoryEmbedding(ctx, t.ID, model, 1, vecs[0]); err != nil {
				continue
			}
			progressed++
			done++
			s.backfillDone.Add(1)
		}
		if progressed == 0 {
			// Whole batch failed to embed — the provider is unhealthy.
			// Bail instead of re-fetching the same rows forever.
			return done, errors.New("memory: backfill made no progress (embedding endpoint unavailable?)")
		}
	}
}
