// Package api — opencode_handlers.go exposes the local opencode-serve
// supervisor over the REST API the PWA consumes. The handler is a
// thin shim over opencode.Manager: it never holds business logic of
// its own so the manager's behavior (idempotent Start/Stop, model
// cache, sentinel ErrNotInstalled) drives both the HTTP surface and
// the MCP tool surface that may later be added on top of the same
// manager instance.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/opencode"
)

// opencodeManager is the small interface the handler depends on. It
// matches *opencode.Manager exactly, but defining it here lets unit
// tests inject a fake without touching the real subprocess layer.
type opencodeManager interface {
	Start(ctx context.Context) error
	Stop() error
	Status() opencode.Status
	ListModels(ctx context.Context) ([]string, error)
	RefreshModels(ctx context.Context) ([]string, error)
	CacheAge() time.Duration
}

// OpenCodeHandlers wires the opencode manager into HTTP handlers.
// Manager is required; the router only registers routes when it's
// non-nil. Exported so the main daemon package can construct it.
type OpenCodeHandlers struct {
	Manager opencodeManager
}

// Status serves GET /api/v1/opencode/status. Always 200 — the manager
// itself encodes "not installed" via Status.Installed=false rather
// than via an HTTP error code, because the dashboard wants to render
// "install opencode to enable this" UX rather than treat absence as a
// failure.
func (h *OpenCodeHandlers) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Manager.Status())
}

// Start serves POST /api/v1/opencode/start.
//
//   - 409 when the binary isn't on PATH (opencode.ErrNotInstalled) —
//     the operator's expected next step is to install opencode, not
//     to retry, so a permanent-looking 4xx is the right signal.
//   - 200 with the current status when already running (idempotent).
//   - 200 with the new status on a fresh successful start.
//   - 502 on any other start failure (readiness timeout, spawn
//     failure) — these are gateway-side errors the operator can't fix
//     by changing their request.
func (h *OpenCodeHandlers) Start(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Start(r.Context()); err != nil {
		if errors.Is(err, opencode.ErrNotInstalled) {
			writeError(w, http.StatusConflict, "opencode binary not on PATH")
			return
		}
		writeErrorDetail(w, http.StatusBadGateway,
			"failed to start opencode", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.Manager.Status())
}

// Stop serves POST /api/v1/opencode/stop. Idempotent — calling Stop
// when nothing is running is not an error; the response just reflects
// the current (stopped) status. 500 only on a true internal failure
// from the manager.
func (h *OpenCodeHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Stop(); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to stop opencode", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.Manager.Status())
}

// modelsResponse is the body of GET /api/v1/opencode/models. Cached
// flips to true when the response came from the in-memory 5-minute
// cache; the dashboard uses this to render a "refresh available"
// affordance for the operator.
type modelsResponse struct {
	Models []string `json:"models"`
	Cached bool     `json:"cached"`
}

// Models serves GET /api/v1/opencode/models. Returns 502 on any
// listing failure — the gateway successfully received the request but
// can't talk to the underlying CLI to fulfil it. `?refresh=1` (or
// `true`) busts the 5-minute cache and forces a live `opencode models`
// run — the path the dashboard's refresh affordance and "I just
// authenticated a new provider" flows use.
func (h *OpenCodeHandlers) Models(w http.ResponseWriter, r *http.Request) {
	// Snapshot cache age BEFORE the call so we can correctly classify
	// the response as cache-hit (age > 0 stays > 0) vs cache-miss
	// (age was 0 and went to a positive value).
	preAge := h.Manager.CacheAge()

	list := h.Manager.ListModels
	refresh := r.URL.Query().Get("refresh")
	if refresh == "1" || strings.EqualFold(refresh, "true") {
		list = h.Manager.RefreshModels
		preAge = 0 // forced run is by definition not a cache hit
	}

	models, err := list(r.Context())
	if err != nil {
		if errors.Is(err, opencode.ErrNotInstalled) {
			writeError(w, http.StatusConflict, "opencode binary not on PATH")
			return
		}
		writeErrorDetail(w, http.StatusBadGateway,
			"failed to list models", err.Error())
		return
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, modelsResponse{
		Models: models,
		Cached: preAge > 0,
	})
}
