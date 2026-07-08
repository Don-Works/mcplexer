// monitoring_handler.go — REST surface for the Monitoring feature
// (remote hosts, log sources, alert channels; migration 128). Backs the
// per-workspace "Monitoring" page in the PWA. Validation (selector
// charset, secret-ref-only channel config) lives in the store layer so
// this surface and the MCP admin tools reject identically.
package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/store"
)

type monitoringHandler struct {
	store store.MonitoringStore
}

func workspaceIDParam(r *http.Request) string {
	return r.URL.Query().Get("workspace_id")
}

// writeMonitoringErr maps store errors onto HTTP statuses: sentinel
// not-founds → 404, FieldError validation → 400 with detail, else 500.
func writeMonitoringErr(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, store.ErrRemoteHostNotFound),
		errors.Is(err, store.ErrLogSourceNotFound),
		errors.Is(err, store.ErrMonitoringChannelNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		var fe *store.FieldError
		if errors.As(err, &fe) {
			writeErrorDetail(w, http.StatusBadRequest, "failed to "+action, fe.Error())
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to "+action, err.Error())
	}
}

// --- remote hosts ---

func (h *monitoringHandler) listHosts(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	hosts, err := h.store.ListRemoteHosts(r.Context(), wsID)
	if err != nil {
		writeMonitoringErr(w, err, "list remote hosts")
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (h *monitoringHandler) createHost(w http.ResponseWriter, r *http.Request) {
	// Default enabled=true: an absent flag stays true, explicit false wins.
	host := store.RemoteHost{Enabled: true}
	if err := decodeJSON(r, &host); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.CreateRemoteHost(r.Context(), &host); err != nil {
		writeMonitoringErr(w, err, "create remote host")
		return
	}
	writeJSON(w, http.StatusCreated, host)
}

func (h *monitoringHandler) getHost(w http.ResponseWriter, r *http.Request) {
	host, err := h.store.GetRemoteHost(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMonitoringErr(w, err, "get remote host")
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func (h *monitoringHandler) updateHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetRemoteHost(r.Context(), id)
	if err != nil {
		writeMonitoringErr(w, err, "get remote host")
		return
	}
	host := *existing
	if err := decodeJSON(r, &host); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	host.ID = id
	host.HostKeyPin = existing.HostKeyPin // pin is repin-only, never PATCHable
	if err := h.store.UpdateRemoteHost(r.Context(), &host); err != nil {
		writeMonitoringErr(w, err, "update remote host")
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func (h *monitoringHandler) deleteHost(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteRemoteHost(r.Context(), r.PathValue("id")); err != nil {
		writeMonitoringErr(w, err, "delete remote host")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *monitoringHandler) repinHost(w http.ResponseWriter, r *http.Request) {
	if err := h.store.SetRemoteHostPin(r.Context(), r.PathValue("id"), ""); err != nil {
		writeMonitoringErr(w, err, "repin remote host")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pin cleared"})
}

// --- log sources ---

func (h *monitoringHandler) listSources(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	sources, err := h.store.ListLogSources(r.Context(), wsID)
	if err != nil {
		writeMonitoringErr(w, err, "list log sources")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *monitoringHandler) createSource(w http.ResponseWriter, r *http.Request) {
	// Default enabled=true: an absent flag stays true, explicit false wins.
	src := store.LogSource{Enabled: true}
	if err := decodeJSON(r, &src); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.CreateLogSource(r.Context(), &src); err != nil {
		writeMonitoringErr(w, err, "create log source")
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

func (h *monitoringHandler) getSource(w http.ResponseWriter, r *http.Request) {
	src, err := h.store.GetLogSource(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMonitoringErr(w, err, "get log source")
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (h *monitoringHandler) updateSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetLogSource(r.Context(), id)
	if err != nil {
		writeMonitoringErr(w, err, "get log source")
		return
	}
	src := *existing
	if err := decodeJSON(r, &src); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	src.ID = id
	if err := h.store.UpdateLogSource(r.Context(), &src); err != nil {
		writeMonitoringErr(w, err, "update log source")
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (h *monitoringHandler) deleteSource(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteLogSource(r.Context(), r.PathValue("id")); err != nil {
		writeMonitoringErr(w, err, "delete log source")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- monitoring channels ---

func (h *monitoringHandler) listChannels(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	channels, err := h.store.ListMonitoringChannels(r.Context(), wsID)
	if err != nil {
		writeMonitoringErr(w, err, "list monitoring channels")
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

func (h *monitoringHandler) createChannel(w http.ResponseWriter, r *http.Request) {
	// Default enabled=true: an absent flag stays true, explicit false wins.
	c := store.MonitoringChannel{Enabled: true}
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.CreateMonitoringChannel(r.Context(), &c); err != nil {
		writeMonitoringErr(w, err, "create monitoring channel")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *monitoringHandler) getChannel(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.GetMonitoringChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMonitoringErr(w, err, "get monitoring channel")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *monitoringHandler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := h.store.GetMonitoringChannel(r.Context(), id)
	if err != nil {
		writeMonitoringErr(w, err, "get monitoring channel")
		return
	}
	c := *existing
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	c.ID = id
	if err := h.store.UpdateMonitoringChannel(r.Context(), &c); err != nil {
		writeMonitoringErr(w, err, "update monitoring channel")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *monitoringHandler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteMonitoringChannel(r.Context(), r.PathValue("id")); err != nil {
		writeMonitoringErr(w, err, "delete monitoring channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
