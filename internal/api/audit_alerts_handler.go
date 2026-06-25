package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// auditAlertsHandler backs the alert + saved-search REST surface:
//
//	GET    /audit/alerts                — merged anomalies + security events
//	GET    /audit/saved-searches        — list
//	POST   /audit/saved-searches        — create
//	PATCH  /audit/saved-searches/{id}   — update
//	DELETE /audit/saved-searches/{id}   — delete (204)
//
// notifyBus is optional — when wired, the saved-search evaluator (driven
// by a ticker in serve.go) publishes a notification on fire. The handler
// itself only does CRUD + read; firing is the evaluator's job.
type auditAlertsHandler struct {
	store     store.AuditStore
	notifyBus *notify.Bus // optional
}

const auditAlertsDefaultWindowSec = 3600

func (h *auditAlertsHandler) alerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ws := q.Get("workspace_id")
	windowSec := auditAlertsDefaultWindowSec
	if v := q.Get("window_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			windowSec = n
		}
	}
	window := time.Duration(windowSec) * time.Second

	anomalies, err := h.store.AuditAnomalies(r.Context(), ws, window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute anomalies")
		return
	}
	security, err := h.store.AuditSecurityEvents(r.Context(), ws, window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute security events")
		return
	}
	merged := append(anomalies, security...)
	if merged == nil {
		merged = []store.AuditAlert{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts":       merged,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *auditAlertsHandler) listSaved(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListSavedSearches(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list saved searches")
		return
	}
	if list == nil {
		list = []store.SavedSearch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// savedSearchBody is the create/update request shape.
type savedSearchBody struct {
	Name           string         `json:"name"`
	Q              string         `json:"q"`
	Filter         map[string]any `json:"filter"`
	ThresholdCount int            `json:"threshold_count"`
	WindowSec      int            `json:"window_sec"`
	WorkspaceID    string         `json:"workspace_id"`
	Enabled        *bool          `json:"enabled"`
}

func (h *auditAlertsHandler) createSaved(w http.ResponseWriter, r *http.Request) {
	var b savedSearchBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if b.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	s := &store.SavedSearch{
		Name:           b.Name,
		Q:              b.Q,
		Filter:         b.Filter,
		ThresholdCount: b.ThresholdCount,
		WindowSec:      b.WindowSec,
		WorkspaceID:    b.WorkspaceID,
		Enabled:        b.Enabled == nil || *b.Enabled, // default enabled
	}
	if err := h.store.CreateSavedSearch(r.Context(), s); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create saved search")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": s})
}

func (h *auditAlertsHandler) updateSaved(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetSavedSearch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "saved search not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load saved search")
		return
	}
	var b savedSearchBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// PATCH semantics: apply only the fields present. Zero-value scalars
	// from the body overwrite — the frontend sends the full row on edit.
	if b.Name != "" {
		existing.Name = b.Name
	}
	existing.Q = b.Q
	existing.Filter = b.Filter
	if b.ThresholdCount > 0 {
		existing.ThresholdCount = b.ThresholdCount
	}
	if b.WindowSec > 0 {
		existing.WindowSec = b.WindowSec
	}
	existing.WorkspaceID = b.WorkspaceID
	if b.Enabled != nil {
		existing.Enabled = *b.Enabled
	}
	if err := h.store.UpdateSavedSearch(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update saved search")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": existing})
}

func (h *auditAlertsHandler) deleteSaved(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.DeleteSavedSearch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "saved search not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete saved search")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
