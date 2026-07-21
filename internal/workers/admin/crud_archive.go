package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Archive disables a worker and marks it retired while preserving the worker
// row, run history, and audit trail. Archived workers are hidden from default
// lists and cannot be scheduled, mesh-triggered, resumed, or run-now'd.
func (s *Service) Archive(ctx context.Context, id, reason string) (*store.Worker, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id required")
	}
	reason = strings.TrimSpace(reason)
	w, err := s.store.GetWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	previousEnabled := w.Enabled
	if w.ArchivedAt != nil {
		s.emitAuditArchive(ctx, id, w.Name, reason, previousEnabled, "ok", "")
		s.removeScheduleAfterDelete(ctx, id)
		if err := s.cancelRunningRunsForDisabledWorker(ctx, id); err != nil {
			return nil, err
		}
		return w, nil
	}
	now := s.clock.Now().UTC()
	w.Enabled = false
	w.ArchivedAt = &now
	w.ArchivedReason = reason
	if err := s.store.UpdateWorker(ctx, w); err != nil {
		s.emitAuditArchive(ctx, id, w.Name, reason, previousEnabled, "error", err.Error())
		return nil, err
	}
	stored, err := s.store.GetWorker(ctx, id)
	if err != nil {
		s.emitAuditArchive(ctx, id, w.Name, reason, previousEnabled, "error", err.Error())
		return nil, err
	}
	s.emitAuditArchive(ctx, id, stored.Name, reason, previousEnabled, "ok", "")
	s.removeScheduleAfterDelete(ctx, id)
	if err := s.cancelRunningRunsForDisabledWorker(ctx, id); err != nil {
		return nil, err
	}
	return stored, nil
}
