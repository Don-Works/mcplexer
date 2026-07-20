// monitoring_incidents_handler.go — the read surface for incidents.
//
// "What is currently wrong?" had no answer outside the dashboard: incidents and
// their occurrence ledger were written by the triage and absence paths and read
// by nothing an operator or an agent could call. These endpoints are that
// answer, and they are GET-only — every mutation of an incident stays on the
// paths that own the policy (triage commit, task resolution, the evaluator).
//
// Workspace scoping mirrors the neighbouring monitoring endpoints exactly:
// workspace_id is required, the store scopes every query to it, and a
// cross-workspace id is reported with the same not-found sentinel a genuinely
// absent row would produce.
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type monitoringIncidentHandler struct {
	store store.MonitoringIncidentReadStore // nil = unsupported by this store
}

// list serves a workspace's incidents, most recently seen first.
func (h *monitoringIncidentHandler) list(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "incident reads not available on this daemon")
		return
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	filter, ok := incidentFilterParams(w, r, wsID)
	if !ok {
		return
	}
	incidents, err := h.store.ListMonitoringIncidents(r.Context(), filter)
	if err != nil {
		writeMonitoringErr(w, err, "list incidents")
		return
	}
	active := 0
	for _, i := range incidents {
		if i.Active {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incidents": incidents,
		"total":     len(incidents),
		"active":    active,
		// Served as data so a client never has to hard-code the vocabulary
		// it is expected to switch on.
		"class_kinds": []string{
			store.IncidentClassTemplate, store.IncidentClassCorrelation,
			store.IncidentClassAbsence, store.IncidentClassCollection,
			store.IncidentClassOther,
		},
	})
}

// get serves one incident plus its episode ledger, so the shape over time is
// visible rather than collapsed into a single last_seen.
func (h *monitoringIncidentHandler) get(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "incident reads not available on this daemon")
		return
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	incident, err := h.store.GetMonitoringIncident(r.Context(), wsID, r.PathValue("id"))
	if errors.Is(err, store.ErrMonitoringIncidentNotFound) {
		writeError(w, http.StatusNotFound, store.ErrMonitoringIncidentNotFound.Error())
		return
	}
	if err != nil {
		writeMonitoringErr(w, err, "get incident")
		return
	}
	limit, ok := monitoringLimitParam(w, r)
	if !ok {
		return
	}
	occurrences, err := h.store.ListMonitoringOccurrences(r.Context(), incident.ID, limit)
	if err != nil {
		writeMonitoringErr(w, err, "list occurrences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incident":    incident,
		"occurrences": occurrences,
	})
}

// incidentFilterParams parses the list filters. An unknown status or
// disposition is rejected rather than silently ignored: a filter that quietly
// does nothing is how an operator concludes there are no actionable incidents.
func incidentFilterParams(
	w http.ResponseWriter, r *http.Request, wsID string,
) (store.MonitoringIncidentFilter, bool) {
	q := r.URL.Query()
	filter := store.MonitoringIncidentFilter{WorkspaceID: wsID}
	switch status := q.Get("status"); status {
	case "", store.MonitoringIncidentStatusActive, store.MonitoringIncidentStatusInactive:
		filter.Status = status
	default:
		writeError(w, http.StatusBadRequest, "status must be active or inactive")
		return filter, false
	}
	if disposition := q.Get("disposition"); disposition != "" {
		if !store.ValidMonitoringDisposition(disposition) {
			writeError(w, http.StatusBadRequest,
				"disposition must be actionable|uncertain|evidence-gap|benign")
			return filter, false
		}
		filter.Disposition = disposition
	}
	if raw := q.Get("since"); raw != "" {
		since, err := parseSinceParam(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest,
				"since must be an RFC3339 timestamp or a Go duration like 24h")
			return filter, false
		}
		filter.Since = since
	}
	limit, ok := monitoringLimitParam(w, r)
	if !ok {
		return filter, false
	}
	filter.Limit = limit
	return filter, true
}

// parseSinceParam accepts both an absolute instant and a relative lookback.
// Scripts reach for "24h"; a dashboard paginating on a timestamp reaches for
// RFC3339, and refusing either would just push the conversion onto the caller.
func parseSinceParam(raw string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return time.Time{}, errors.New("invalid since")
	}
	return time.Now().UTC().Add(-d), nil
}

// monitoringLimitParam parses an optional positive limit. Zero means "the
// store's default"; the store clamps the maximum.
func monitoringLimitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return 0, false
	}
	return limit, true
}
