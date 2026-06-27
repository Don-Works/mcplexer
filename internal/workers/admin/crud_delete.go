package admin

import (
	"context"
	"errors"
	"strings"
)

// Delete hard-deletes a worker. Runs are intentionally preserved by the
// store layer; we don't touch them here. Emits worker_admin.delete with
// the worker name captured BEFORE the delete (the row is gone after).
func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id required")
	}
	// Best-effort name lookup for the audit payload. If the worker
	// doesn't exist the subsequent DeleteWorker will surface the right
	// error; we just log "name" empty in the audit row.
	name := ""
	if w, err := s.store.GetWorker(ctx, id); err == nil && w != nil {
		name = w.Name
	}
	if err := s.store.DeleteWorker(ctx, id); err != nil {
		s.emitAuditDelete(ctx, id, name, "error", err.Error())
		return err
	}
	s.emitAuditDelete(ctx, id, name, "ok", "")
	s.removeScheduleAfterDelete(ctx, id)
	return nil
}
