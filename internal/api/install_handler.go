package api

import (
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/install"
)

type installHandler struct {
	manager *install.Manager
}

func (h *installHandler) status(w http.ResponseWriter, r *http.Request) {
	result, err := h.manager.Status()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *installHandler) install(w http.ResponseWriter, r *http.Request) {
	clientID := install.ClientID(r.PathValue("clientId"))
	if clientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing clientId"})
		return
	}

	info, err := h.manager.Install(clientID)
	if err != nil {
		status := classifyInstallError(err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *installHandler) uninstall(w http.ResponseWriter, r *http.Request) {
	clientID := install.ClientID(r.PathValue("clientId"))
	if clientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing clientId"})
		return
	}

	info, err := h.manager.Uninstall(clientID)
	if err != nil {
		status := classifyInstallError(err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *installHandler) preview(w http.ResponseWriter, r *http.Request) {
	clientID := install.ClientID(r.PathValue("clientId"))
	if clientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing clientId"})
		return
	}

	result, err := h.manager.Preview(clientID)
	if err != nil {
		status := classifyInstallError(err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func classifyInstallError(err error) int {
	msg := err.Error()
	if strings.Contains(msg, "unknown client") {
		return http.StatusNotFound
	}
	if strings.Contains(msg, "not detected") {
		return http.StatusBadRequest
	}
	if strings.Contains(msg, "not configured") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
