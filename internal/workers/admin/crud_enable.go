// Package admin — crud_enable.go owns the enable/disable lifecycle
// (SetEnabled, Pause, Resume). Split off from crud.go to honour the
// 300-line file budget.
//
// All three entry points funnel into setEnabledWithVerb so the
// idempotency + audit behaviour stays single-sourced; only the audit
// event name differs (worker_admin.set_enabled vs .pause vs .resume).
package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// SetEnabled toggles Enabled on the worker; emits worker_admin.set_enabled.
// For the named Pause / Resume tool aliases, callers should use the
// Pause / Resume methods instead — they emit the more specific
// worker_admin.pause / worker_admin.resume verbs.
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) (*store.Worker, error) {
	return s.setEnabledWithVerb(ctx, id, enabled, auditEventAdminSetEnabled)
}

// Pause sets enabled=false and emits worker_admin.pause. Idempotent.
// Use this from the mcplexer__pause_worker dispatch path so the audit
// row distinguishes "operator paused" from "operator flipped via
// update_worker(enabled=false)".
func (s *Service) Pause(ctx context.Context, id string) (*store.Worker, error) {
	return s.setEnabledWithVerb(ctx, id, false, auditEventAdminPause)
}

// Resume sets enabled=true and emits worker_admin.resume. Idempotent.
func (s *Service) Resume(ctx context.Context, id string) (*store.Worker, error) {
	return s.setEnabledWithVerb(ctx, id, true, auditEventAdminResume)
}

// setEnabledWithVerb is the shared implementation behind SetEnabled,
// Pause, and Resume. The verb parameter selects which audit event name
// is emitted so dashboards can distinguish the three call sites.
func (s *Service) setEnabledWithVerb(
	ctx context.Context, id string, enabled bool, verb string,
) (*store.Worker, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id required")
	}
	w, err := s.store.GetWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	previous := w.Enabled
	if w.Enabled == enabled {
		// Idempotent no-op: still audit so reviewers see the operator
		// asked. Status="ok"; previous_enabled == enabled signals
		// "nothing changed" to the dashboard.
		s.emitAuditSetEnabled(ctx, id, verb, enabled, previous, "ok", "")
		if enabled {
			s.syncScheduleAfterChange(ctx, w)
		} else {
			s.removeScheduleAfterDelete(ctx, id)
		}
		return w, nil
	}
	w.Enabled = enabled
	if err := s.store.UpdateWorker(ctx, w); err != nil {
		s.emitAuditSetEnabled(ctx, id, verb, enabled, previous, "error", err.Error())
		return nil, err
	}
	stored, err := s.store.GetWorker(ctx, id)
	if err != nil {
		s.emitAuditSetEnabled(ctx, id, verb, enabled, previous, "error", err.Error())
		return nil, err
	}
	s.emitAuditSetEnabled(ctx, id, verb, enabled, previous, "ok", "")
	if enabled {
		s.syncScheduleAfterChange(ctx, stored)
	} else {
		s.removeScheduleAfterDelete(ctx, id)
	}
	return stored, nil
}
