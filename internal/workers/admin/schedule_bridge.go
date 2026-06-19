package admin

import (
	"context"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

// ScheduleBridge is the seam that lets the admin service keep the
// scheduler's ScheduledJob catalog in sync with the Worker catalog.
// The real implementation lives in the schedulebridge package (so the
// admin package doesn't import scheduler).
//
// EnsureForWorker creates a ScheduledJob with kind="worker" pointing at
// the worker, OR re-syncs an existing row when the worker's
// schedule_spec / enabled flag changed. RemoveForWorker tears that row
// down. Both must be idempotent: the admin surface fires them on
// every Create/Update/Delete/SetEnabled, and the daemon also calls
// EnsureForWorker on boot for every enabled worker.
type ScheduleBridge interface {
	EnsureForWorker(ctx context.Context, w *store.Worker) error
	RemoveForWorker(ctx context.Context, workerID string) error
}

// SetScheduleBridge attaches the optional ScheduleBridge after
// construction so callers that build the bridge with a reference to
// the scheduler can avoid an import cycle. Idempotent.
func (s *Service) SetScheduleBridge(b ScheduleBridge) {
	s.scheduleBridge = b
}

// syncScheduleAfterChange invokes EnsureForWorker on the bridge.
// Failures are logged but never bubble up — a scheduler that won't
// pick up the worker is recoverable (admin can fix the spec and call
// update); a CRUD that errors out because of bridge trouble would
// strand the row.
func (s *Service) syncScheduleAfterChange(ctx context.Context, w *store.Worker) {
	if s.scheduleBridge == nil || w == nil {
		return
	}
	if err := s.scheduleBridge.EnsureForWorker(ctx, w); err != nil {
		slog.Warn("worker schedule sync failed",
			"worker_id", w.ID, "schedule_spec", w.ScheduleSpec, "error", err)
	}
}

// removeScheduleAfterDelete tears down the bridge entry. Logged on
// failure, never bubbled — the Worker row is already gone.
func (s *Service) removeScheduleAfterDelete(ctx context.Context, id string) {
	if s.scheduleBridge == nil || id == "" {
		return
	}
	if err := s.scheduleBridge.RemoveForWorker(ctx, id); err != nil {
		slog.Warn("worker schedule remove failed",
			"worker_id", id, "error", err)
	}
}
