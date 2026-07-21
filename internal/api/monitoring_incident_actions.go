// monitoring_incident_actions.go — the operator's write surface on an incident.
//
// The read side (monitoring_incidents_handler.go) is GET-only by design; these
// POSTs are the deliberate exceptions, one per operator verb:
//
//	POST /monitoring/incidents/{id}/ack        — seen it, stop nagging, keep open
//	POST /monitoring/incidents/{id}/unack      — resume nagging
//	POST /monitoring/incidents/{id}/silence    — bounded quiet period (auto-expires)
//	POST /monitoring/incidents/{id}/unsilence  — end the quiet period now
//	POST /monitoring/incidents/{id}/dismiss    — it's over (benign resolution)
//	GET  /monitoring/suppressions              — what is muted right now, and why
//
// Every verb is workspace-scoped exactly like the neighbouring monitoring
// endpoints, requires an actor (so a suppression is always attributable), and
// maps store sentinels onto stable HTTP codes. Silence validates a positive,
// bounded duration — an unbounded silence is a permanent mute and is refused.
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type monitoringIncidentActionHandler struct {
	store      store.MonitoringIncidentActionStore // nil = unsupported by this store
	resolution store.MonitoringResolutionStore     // nil = dismissed list omitted
}

// incidentActionBody is the shared request body. Fields are per-verb optional:
// ack/unack/unsilence need only actor; silence adds duration; dismiss adds
// status_text. Actor is always required.
type incidentActionBody struct {
	Actor      string `json:"actor"`
	Session    string `json:"session,omitempty"`
	Duration   string `json:"duration,omitempty"`
	StatusText string `json:"status_text,omitempty"`
}

func (h *monitoringIncidentActionHandler) ack(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.actionRef(w, r)
	if !ok {
		return
	}
	view, err := h.store.AckMonitoringIncident(r.Context(), ref)
	h.writeActionResult(w, view, err)
}

func (h *monitoringIncidentActionHandler) unack(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.actionRef(w, r)
	if !ok {
		return
	}
	view, err := h.store.UnackMonitoringIncident(r.Context(), ref)
	h.writeActionResult(w, view, err)
}

func (h *monitoringIncidentActionHandler) unsilence(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.actionRef(w, r)
	if !ok {
		return
	}
	view, err := h.store.UnsilenceMonitoringIncident(r.Context(), ref)
	h.writeActionResult(w, view, err)
}

func (h *monitoringIncidentActionHandler) silence(w http.ResponseWriter, r *http.Request) {
	ref, body, ok := h.actionRefWithBody(w, r)
	if !ok {
		return
	}
	dur, err := time.ParseDuration(strings.TrimSpace(body.Duration))
	if err != nil || dur <= 0 {
		writeError(w, http.StatusBadRequest,
			"silence requires a positive, bounded duration like \"2h\"")
		return
	}
	view, err := h.store.SilenceMonitoringIncident(r.Context(), store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: ref, Duration: dur,
	})
	h.writeActionResult(w, view, err)
}

func (h *monitoringIncidentActionHandler) dismiss(w http.ResponseWriter, r *http.Request) {
	ref, body, ok := h.actionRefWithBody(w, r)
	if !ok {
		return
	}
	resolution, err := h.store.DismissMonitoringIncident(r.Context(), store.MonitoringIncidentDismissInput{
		MonitoringIncidentActionRef: ref, StatusText: body.StatusText,
	})
	if err != nil {
		h.writeActionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolution": resolution})
}

// suppressions is the visibility path: currently-acked/silenced incidents plus
// live dismissals (benign resolutions), so "what is muted and why" is one call.
func (h *monitoringIncidentActionHandler) suppressions(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "incident actions not available on this daemon")
		return
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	limit, ok := monitoringLimitParam(w, r)
	if !ok {
		return
	}
	suppressed, err := h.store.ListSuppressedMonitoringIncidents(r.Context(), wsID, time.Now().UTC(), limit)
	if err != nil {
		writeMonitoringErr(w, err, "list suppressed incidents")
		return
	}
	dismissed := []*store.MonitoringResolution{}
	if h.resolution != nil {
		dismissed, err = h.resolution.ListMonitoringResolutions(r.Context(), wsID, true, limit)
		if err != nil {
			writeMonitoringErr(w, err, "list dismissed incidents")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"suppressed": suppressed,
		"dismissed":  dismissed,
		"total":      len(suppressed) + len(dismissed),
	})
}

// actionRef validates the store, workspace, incident id and actor, returning a
// populated ref. It decodes the body but discards it — used by the verbs that
// need no body field beyond actor.
func (h *monitoringIncidentActionHandler) actionRef(
	w http.ResponseWriter, r *http.Request,
) (store.MonitoringIncidentActionRef, bool) {
	ref, _, ok := h.actionRefWithBody(w, r)
	return ref, ok
}

// actionRefWithBody is actionRef plus the decoded body, for silence and dismiss.
func (h *monitoringIncidentActionHandler) actionRefWithBody(
	w http.ResponseWriter, r *http.Request,
) (store.MonitoringIncidentActionRef, incidentActionBody, bool) {
	var body incidentActionBody
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "incident actions not available on this daemon")
		return store.MonitoringIncidentActionRef{}, body, false
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return store.MonitoringIncidentActionRef{}, body, false
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be a JSON object with an actor")
		return store.MonitoringIncidentActionRef{}, body, false
	}
	if strings.TrimSpace(body.Actor) == "" {
		writeError(w, http.StatusBadRequest, "actor required")
		return store.MonitoringIncidentActionRef{}, body, false
	}
	ref := store.MonitoringIncidentActionRef{
		WorkspaceID: wsID, IncidentID: r.PathValue("id"),
		Actor: body.Actor, Session: body.Session,
	}
	return ref, body, true
}

func (h *monitoringIncidentActionHandler) writeActionResult(
	w http.ResponseWriter, view *store.MonitoringIncidentView, err error,
) {
	if err != nil {
		h.writeActionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": view})
}

// writeActionErr maps the action sentinels onto stable codes; a not-found is 404,
// a bad silence or missing actor is 400, everything else defers to the shared
// monitoring error writer (which hides internal detail behind a request id).
func (h *monitoringIncidentActionHandler) writeActionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMonitoringIncidentNotFound):
		writeError(w, http.StatusNotFound, store.ErrMonitoringIncidentNotFound.Error())
	case errors.Is(err, store.ErrMonitoringSilenceUnbounded),
		errors.Is(err, store.ErrMonitoringActionActorRequired):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeMonitoringErr(w, err, "apply incident action")
	}
}
