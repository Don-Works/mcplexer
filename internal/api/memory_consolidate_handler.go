// memory_consolidate_handler.go — REST surface for the memory consolidator
// (the sleep-time worker that compacts duplicate notes and invalidates
// stale ones). The dashboard's /memory/consolidation page is the
// primary consumer.
//
// Design: the consolidator is a regular Worker materialised from the
// "memory-consolidator" template seed (internal/workertemplates/seeds/).
// Enable installs the template into the current workspace; the worker's
// own schedule_spec drives recurring runs; Run-Now fires an ad-hoc run.
//
// Routes (all under /api/v1/memory/consolidate):
//
//	GET    /api/v1/memory/consolidate/status      → enabled, last_run, stats
//	POST   /api/v1/memory/consolidate/enable      → install + enable
//	POST   /api/v1/memory/consolidate/disable     → pause the worker
//	POST   /api/v1/memory/consolidate/run         → ad-hoc run
//
// The "enabled" definition is "a Worker named memory-consolidator exists
// in the workspace AND its Enabled flag is true" — matching the schedule
// bridge's view. Disable pauses (sets Enabled=false) rather than
// deleting so the run history survives.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// consolidatorName is the well-known Worker name used for the per-workspace
// consolidator instance. Stable: the status / enable / disable endpoints
// all look this up by (workspace, name). Renaming requires a migration.
const consolidatorName = "memory-consolidator"

// consolidatorTemplate is the seed template name that gets materialised
// on enable. Mirrors the file at
// internal/workertemplates/seeds/memory-consolidator.json.
const consolidatorTemplate = "memory-consolidator"

// defaultConsolidatorSchedule is the cron spec applied when the caller
// doesn't override it. Nightly at 03:00 UTC keeps the embedding burst
// outside business hours for most users and avoids racing with
// interactive sessions. Operators can override on the worker detail page.
const defaultConsolidatorSchedule = "0 3 * * *"

// consolidateHandler wraps the worker-admin service + the auth-scope
// store so it can pick a sensible default secret scope when the caller
// doesn't supply one (the consolidator template advertises one
// secret-slot: model_api_key).
type consolidateHandler struct {
	workers    *workersadmin.Service
	scopeStore store.AuthScopeStore
}

func newConsolidateHandler(workers *workersadmin.Service, s store.AuthScopeStore) *consolidateHandler {
	return &consolidateHandler{workers: workers, scopeStore: s}
}

// consolidateStatusResponse is the GET /status payload.
type consolidateStatusResponse struct {
	Enabled         bool   `json:"enabled"`
	Installed       bool   `json:"installed"`
	WorkerID        string `json:"worker_id,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	ScheduleSpec    string `json:"schedule_spec,omitempty"`
	LastRunStatus   string `json:"last_run_status,omitempty"`
	LastRunAt       string `json:"last_run_at,omitempty"`
	LastRunID       string `json:"last_run_id,omitempty"`
	RecentRuns      int    `json:"recent_runs"`
	NeedsSecretHint string `json:"needs_secret_hint,omitempty"`
}

// handleStatus serves GET /api/v1/memory/consolidate/status?workspace_id=...
func (h *consolidateHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	resp := consolidateStatusResponse{WorkspaceID: wsID}
	out, err := h.workers.Get(r.Context(), workersadmin.GetInput{
		Name:        consolidatorName,
		WorkspaceID: wsID,
	})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) {
			// Not installed yet — surface a 200 with installed=false so the
			// UI can branch.
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError,
			"consolidator status failed", err.Error())
		return
	}
	resp.Installed = true
	resp.Enabled = out.Worker.Enabled
	resp.WorkerID = out.Worker.ID
	resp.ScheduleSpec = out.Worker.ScheduleSpec
	resp.RecentRuns = len(out.RecentRuns)
	if n := len(out.RecentRuns); n > 0 {
		resp.LastRunStatus = out.RecentRuns[0].Status
		resp.LastRunAt = out.RecentRuns[0].StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		resp.LastRunID = out.RecentRuns[0].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// consolidateEnableRequest is the POST /enable body.
type consolidateEnableRequest struct {
	WorkspaceID   string `json:"workspace_id"`
	SecretScopeID string `json:"secret_scope_id,omitempty"`
	ScheduleSpec  string `json:"schedule_spec,omitempty"`
}

// handleEnable serves POST /api/v1/memory/consolidate/enable.
// Idempotent: if the worker already exists, flips Enabled=true and
// updates the schedule if one was supplied.
func (h *consolidateHandler) handleEnable(w http.ResponseWriter, r *http.Request) {
	var body consolidateEnableRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	wsID := strings.TrimSpace(body.WorkspaceID)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	schedule := strings.TrimSpace(body.ScheduleSpec)
	if schedule == "" {
		schedule = defaultConsolidatorSchedule
	}

	// Already installed? Just flip enabled + schedule.
	existing, err := h.workers.Get(r.Context(), workersadmin.GetInput{
		Name: consolidatorName, WorkspaceID: wsID,
	})
	if err == nil && existing != nil && existing.Worker != nil {
		updated, uerr := h.workers.Update(r.Context(), workersadmin.UpdateInput{
			ID:           existing.Worker.ID,
			ScheduleSpec: &schedule,
			Enabled:      boolPtrLocal(true),
		})
		if uerr != nil {
			writeErrorDetail(w, http.StatusInternalServerError,
				"consolidator enable failed", uerr.Error())
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}

	// Fresh install path — needs a secret scope. Pick one if not given.
	scopeID := strings.TrimSpace(body.SecretScopeID)
	if scopeID == "" {
		picked, perr := h.pickDefaultSecretScope(r.Context())
		if perr != nil {
			writeErrorDetail(w, http.StatusPreconditionRequired,
				"no model API key configured", perr.Error())
			return
		}
		scopeID = picked
	}

	enabled := true
	worker, err := h.workers.InstallFromTemplate(r.Context(), workersadmin.InstallFromTemplateInput{
		TemplateName:  consolidatorTemplate,
		WorkerName:    consolidatorName,
		WorkspaceID:   wsID,
		SecretScopeID: scopeID,
		ScheduleSpec:  schedule,
		Enabled:       &enabled,
	})
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest,
			"consolidator install failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// handleDisable serves POST /api/v1/memory/consolidate/disable.
// Pauses the worker (Enabled=false) but leaves it in place so the run
// history survives. Re-enable via /enable.
func (h *consolidateHandler) handleDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	existing, err := h.workers.Get(r.Context(), workersadmin.GetInput{
		Name: consolidatorName, WorkspaceID: body.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) {
			writeError(w, http.StatusNotFound, "consolidator is not installed")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError,
			"consolidator disable failed", err.Error())
		return
	}
	updated, err := h.workers.Pause(r.Context(), existing.Worker.ID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"consolidator pause failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleRunNow serves POST /api/v1/memory/consolidate/run.
// Triggers one ad-hoc run; the run id comes back so the UI can deep-link.
func (h *consolidateHandler) handleRunNow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	existing, err := h.workers.Get(r.Context(), workersadmin.GetInput{
		Name: consolidatorName, WorkspaceID: body.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) {
			writeError(w, http.StatusNotFound,
				"consolidator is not installed — call /enable first")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError,
			"consolidator lookup failed", err.Error())
		return
	}
	out, err := h.workers.RunNow(r.Context(), existing.Worker.ID)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"consolidator run failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pickDefaultSecretScope returns the id of the most-recently-updated
// auth_scope of type "api_key". Returns a typed error when none exists
// so the caller can surface a clear "configure a model API key first"
// message rather than an opaque 500.
func (h *consolidateHandler) pickDefaultSecretScope(ctx context.Context) (string, error) {
	if h.scopeStore == nil {
		return "", errors.New("auth-scope store not wired")
	}
	scopes, err := h.scopeStore.ListAuthScopes(ctx)
	if err != nil {
		return "", err
	}
	// Prefer api_key type; fall back to any populated scope as a
	// last resort. Most-recently-updated wins among matches.
	var best *store.AuthScope
	for i := range scopes {
		s := &scopes[i]
		if !strings.EqualFold(s.Type, "api_key") {
			continue
		}
		if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
			best = s
		}
	}
	if best == nil {
		return "", errors.New(
			"no api_key auth scope found — configure a model API key in Settings → Secrets first")
	}
	return best.ID, nil
}

// boolPtrLocal returns a pointer to v. Local because the api package
// already has a boolPtr in another file we'd risk colliding with.
func boolPtrLocal(v bool) *bool { return &v }
