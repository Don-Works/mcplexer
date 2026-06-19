package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

type routeHandler struct {
	svc     *config.Service
	store   store.RouteRuleStore
	engine  *routing.Engine     // optional; invalidates route cache on mutations
	manager *downstream.Manager // optional; broadcasts tools/list_changed on mutations
}

// notifyToolsChanged broadcasts tools/list_changed to connected clients so
// they re-run tools/list mid-session instead of waiting for reconnect.
func (h *routeHandler) notifyToolsChanged() {
	if h.manager != nil {
		h.manager.NotifyToolsChanged()
	}
}

func (h *routeHandler) list(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	rules, err := h.store.ListRouteRules(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list route rules")
		return
	}
	if rules == nil {
		rules = []store.RouteRule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *routeHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, err := h.store.GetRouteRule(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "route rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get route rule")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (h *routeHandler) bulkCreate(w http.ResponseWriter, r *http.Request) {
	var rules []store.RouteRule
	if err := decodeJSON(r, &rules); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(rules) == 0 {
		writeError(w, http.StatusBadRequest, "empty rules array")
		return
	}
	if len(rules) > 50 {
		writeError(w, http.StatusBadRequest, "too many rules (max 50)")
		return
	}
	if err := h.svc.BulkCreateRouteRules(r.Context(), rules); err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to create route rules", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	writeJSON(w, http.StatusCreated, rules)
}

func (h *routeHandler) create(w http.ResponseWriter, r *http.Request) {
	var rr store.RouteRule
	if err := decodeJSON(r, &rr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.CreateRouteRule(r.Context(), &rr); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "route rule already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create route rule", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	writeJSON(w, http.StatusCreated, rr)
}

func (h *routeHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	// Load existing record so partial updates work.
	existing, err := h.store.GetRouteRule(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "route rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get route rule")
		return
	}

	// Decode body on top of existing values.
	rr := *existing
	if err := decodeJSON(r, &rr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rr.ID = id

	if err := h.svc.UpdateRouteRule(ctx, &rr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "route rule not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update route rule", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	writeJSON(w, http.StatusOK, rr)
}

func (h *routeHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteRouteRule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "route rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete route rule")
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.notifyToolsChanged()
	w.WriteHeader(http.StatusNoContent)
}
