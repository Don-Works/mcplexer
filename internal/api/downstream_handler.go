package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// decodeArgs converts the JSON-encoded args field into a string slice for
// downstream command validation. An empty/null payload returns nil; a
// malformed payload propagates the json error so the handler returns 400.
func decodeArgs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}

type downstreamHandler struct {
	svc       *config.Service
	store     store.DownstreamServerStore
	engine    *routing.Engine     // optional; invalidates route cache on mutations
	manager   *downstream.Manager // optional; broadcasts tools/list_changed on mutations
	toolCache *cache.ToolCache    // optional; hot-applies cache_config changes
}

// notifyToolsChanged broadcasts tools/list_changed to connected clients so
// they re-run tools/list mid-session instead of waiting for reconnect.
func (h *downstreamHandler) notifyToolsChanged() {
	if h.manager != nil {
		h.manager.NotifyToolsChanged()
	}
}

// applyCacheConfig hot-applies a server's cache_config to the live ToolCache
// and invalidates any existing cached entries for that server so stale data
// is never served after a config change.
func (h *downstreamHandler) applyCacheConfig(ds *store.DownstreamServer) {
	if h.toolCache == nil {
		return
	}
	if len(ds.CacheConfig) == 0 || string(ds.CacheConfig) == "{}" {
		h.toolCache.RemoveConfig(ds.ID)
	} else {
		cfg, err := cache.ParseServerCacheConfig(ds.CacheConfig)
		if err != nil {
			slog.Warn("invalid cache config for server, removing custom config",
				"server", ds.ID, "error", err)
			h.toolCache.RemoveConfig(ds.ID)
		} else {
			h.toolCache.SetConfig(ds.ID, cfg)
		}
	}
	// Invalidate existing cached entries for this server so stale data
	// from the old config is never served.
	h.toolCache.InvalidateServer(ds.ID)
}

func (h *downstreamHandler) list(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.ListDownstreamServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list downstream servers")
		return
	}
	if servers == nil {
		servers = []store.DownstreamServer{}
	}
	writeJSON(w, http.StatusOK, servers)
}

func (h *downstreamHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	srv, err := h.store.GetDownstreamServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get downstream server")
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (h *downstreamHandler) create(w http.ResponseWriter, r *http.Request) {
	var ds store.DownstreamServer
	if err := decodeJSON(r, &ds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if ds.Transport == "stdio" {
		args, err := decodeArgs(ds.Args)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid args field: "+err.Error())
			return
		}
		if err := downstream.ValidateCommand(ds.Command, args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx := r.Context()
	if err := h.svc.CreateDownstreamServer(ctx, &ds); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "downstream server already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create downstream server", err.Error())
		return
	}

	// Provision any seeded auth scopes and route rules that depend on
	// this server (e.g., Aikido's client_credentials scope + routes).
	_ = config.SeedDefaultAuthScopes(ctx, h.store.(store.Store))
	_ = config.SeedDefaultRouteRules(ctx, h.store.(store.Store))

	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.applyCacheConfig(&ds)
	h.notifyToolsChanged()
	writeJSON(w, http.StatusCreated, ds)
}

func (h *downstreamHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	// Load existing record so partial updates work.
	existing, err := h.store.GetDownstreamServer(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get downstream server")
		return
	}

	// Decode body on top of existing values.
	ds := *existing
	if err := decodeJSON(r, &ds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ds.ID = id

	if ds.Transport == "stdio" {
		args, err := decodeArgs(ds.Args)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid args field: "+err.Error())
			return
		}
		if err := downstream.ValidateCommand(ds.Command, args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := h.svc.UpdateDownstreamServer(ctx, &ds); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update downstream server", err.Error())
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	h.applyCacheConfig(&ds)
	h.notifyToolsChanged()
	writeJSON(w, http.StatusOK, ds)
}

func (h *downstreamHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteDownstreamServer(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete downstream server")
		return
	}
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}
	if h.toolCache != nil {
		h.toolCache.RemoveConfig(id)
		h.toolCache.InvalidateServer(id)
	}
	h.notifyToolsChanged()
	w.WriteHeader(http.StatusNoContent)
}
