package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

type workspaceHandler struct {
	svc     *config.Service
	store   store.WorkspaceStore
	engine  *routing.Engine     // optional; invalidates route cache on mutations
	manager *downstream.Manager // optional; broadcasts tools/list_changed on mutations
}

// notifyToolsChanged broadcasts tools/list_changed to connected clients so
// they re-run tools/list mid-session instead of waiting for reconnect.
func (h *workspaceHandler) notifyToolsChanged() {
	if h.manager != nil {
		h.manager.NotifyToolsChanged()
	}
}

func (h *workspaceHandler) list(w http.ResponseWriter, r *http.Request) {
	workspaces, err := h.store.ListWorkspaces(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	if workspaces == nil {
		workspaces = []store.Workspace{}
	}
	writeJSON(w, http.StatusOK, workspaces)
}

func (h *workspaceHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, err := h.store.GetWorkspace(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get workspace")
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (h *workspaceHandler) create(w http.ResponseWriter, r *http.Request) {
	var ws store.Workspace
	if err := decodeJSON(r, &ws); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.CreateWorkspace(r.Context(), &ws); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "workspace already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create workspace", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	writeJSON(w, http.StatusCreated, ws)
}

func (h *workspaceHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	// Load existing record so partial updates preserve fields like created_at.
	existing, err := h.store.GetWorkspace(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get workspace")
		return
	}

	// Decode body on top of existing values.
	ws := *existing
	if err := decodeJSON(r, &ws); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ws.ID = id
	if err := h.svc.UpdateWorkspace(ctx, &ws); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update workspace", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	writeJSON(w, http.StatusOK, ws)
}

func (h *workspaceHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteWorkspace(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete workspace")
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	w.WriteHeader(http.StatusNoContent)
}
