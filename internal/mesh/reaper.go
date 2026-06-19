package mesh

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultArchivedRetention is how long archived mesh rows are kept before
// the reaper prunes them. 30 days preserves a generous audit window of
// inter-agent comms while stopping unbounded table growth (archive is a
// status flip, so without pruning the table only ever grows).
//
// TODO(settings): surface as a Settings field (MeshArchivedRetentionDays)
// once the reaper grows a settings handle; for now overrides go through
// NewReaperWithRetention.
const DefaultArchivedRetention = 30 * 24 * time.Hour

// Reaper periodically archives expired messages and prunes old archived
// rows. Archive is a status flip — the row survives for the retention
// window (default 30d) so the mesh remains a useful audit trail of
// inter-agent comms — and only after that age does the sweep call
// DeleteArchivedMessages. Operator-initiated truncation can still call the
// store helper directly with a tighter cutoff.
type Reaper struct {
	store     store.MeshStore
	retention time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewReaper starts a background goroutine that ticks every 60 seconds,
// pruning archived rows older than DefaultArchivedRetention.
func NewReaper(ctx context.Context, s store.MeshStore) *Reaper {
	return NewReaperWithRetention(ctx, s, DefaultArchivedRetention)
}

// NewReaperWithRetention is NewReaper with an explicit archived-row
// retention window. retention <= 0 falls back to the default — a zero
// window would make every sweep delete rows the same tick they archive,
// silently destroying the audit trail.
func NewReaperWithRetention(ctx context.Context, s store.MeshStore, retention time.Duration) *Reaper {
	if retention <= 0 {
		retention = DefaultArchivedRetention
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &Reaper{
		store:     s,
		retention: retention,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	go r.run(ctx)
	return r
}

// Stop cancels the reaper goroutine and waits for it to finish.
func (r *Reaper) Stop() {
	r.cancel()
	<-r.done
}

func (r *Reaper) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *Reaper) sweep(ctx context.Context) {
	now := time.Now().UTC()

	archived, err := r.store.ArchiveExpiredMessages(ctx, now)
	if err != nil {
		slog.Warn("mesh reaper: archive expired", "error", err)
	} else if archived > 0 {
		slog.Info("mesh reaper: archived expired messages", "count", archived)
	}

	workerArchived, err := r.store.ArchiveOldWorkerFindings(ctx, now.Add(-24*time.Hour))
	if err != nil {
		slog.Warn("mesh reaper: archive old worker findings", "error", err)
	} else if workerArchived > 0 {
		slog.Info("mesh reaper: archived old worker findings", "count", workerArchived)
	}

	deleted, err := r.store.DeleteArchivedMessages(ctx, now.Add(-r.retention))
	if err != nil {
		slog.Warn("mesh reaper: prune archived", "error", err)
	} else if deleted > 0 {
		slog.Info("mesh reaper: pruned archived messages",
			"count", deleted, "retention", r.retention.String())
	}
}
