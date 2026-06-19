// Package api — memory_handler.go exposes the cross-harness memory
// subsystem (migration 058 + internal/memory) over HTTP. The dashboard
// uses this surface to list, read, write, search, invalidate, soft-delete,
// and forget-by-source memory entries.
//
// All routes are scoped via store.SkillScope. The dashboard is treated as
// an admin client — every read uses SkillScope{IncludeAll: true} unless a
// workspace_id query parameter narrows it. Mirrors the worker-templates
// admin handler style.
//
// Routes:
//
//	GET    /api/v1/memory                       → list (filters in querystring)
//	GET    /api/v1/memory/count                 → {facts,notes} for current scope
//	GET    /api/v1/memory/{id}                  → fetch one
//	POST   /api/v1/memory                       → create
//	POST   /api/v1/memory/search                → recall
//	POST   /api/v1/memory/{id}/invalidate       → mark superseded
//	DELETE /api/v1/memory/{id}                  → soft delete
//	POST   /api/v1/memory/forget-by-source      → purge by session id
//
// Memory-offer routes live in memory_offers_handler.go.
package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// memoryHandler wraps the memory service + the raw store (for memory-offer
// operations the service does not yet expose). Both must be non-nil for
// the routes to register — router-side guard.
//
// auditor is optional (nil-safe). When set every REST mutation emits an
// audit row whose tool_name mirrors the equivalent memory__* MCP tool
// (memory__save, memory__invalidate, memory__forget, memory__pin,
// memory__unpin, memory__forget_by_source, memory__link_entity,
// memory__unlink_entity). The arguments payload mirrors the MCP shape so
// internal/audit/Redact (inside Logger.Record) runs over secret-looking
// substrings exactly like it does on the MCP path. REST↔MCP parity on
// the audit ledger is the load-bearing invariant — bug F053JE.
type memoryHandler struct {
	svc     *memory.Service
	store   store.MemoryStore
	auditor auditRecorder
}

// newMemoryHandler constructs a memoryHandler. Callers must ensure both
// svc and s are non-nil; the router enforces this. auditor may be nil
// (e.g. unit tests that don't assert the audit ledger) — in that case
// recordAudit is a no-op.
func newMemoryHandler(svc *memory.Service, s store.MemoryStore, auditor auditRecorder) *memoryHandler {
	return &memoryHandler{svc: svc, store: s, auditor: auditor}
}

// recordAudit lives in memory_audit.go.

// handleList serves GET /api/v1/memory.
//
// Querystring: kind, tags (comma-separated), limit, offset, include_invalid,
// workspace_id. workspace_id narrows scope to that single workspace ∪
// global. Unset = admin (IncludeAll).
func (h *memoryHandler) handleList(w http.ResponseWriter, r *http.Request) {
	f, err := parseMemoryFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list memories", err.Error())
		return
	}
	if rows == nil {
		rows = []store.MemoryEntry{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleCount serves GET /api/v1/memory/count → {facts, notes}.
//
// Respects ?workspace_id to narrow; otherwise counts everything.
func (h *memoryHandler) handleCount(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromQuery(r)
	facts, notes, err := h.svc.Count(r.Context(), scope)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to count memories", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"facts": facts, "notes": notes})
}

// handleGet serves GET /api/v1/memory/{id}.
func (h *memoryHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	entry, err := h.svc.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to fetch memory", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// entity refs, create/search/invalidate/pin/delete/forget-by-source and
// related request types + handlers moved to memory_entries.go to keep
// this file under the 300-line guideline (and pair with memory_filter.go
// + memory_*_handler.go siblings).
// The methods are defined across package files; build sees the whole package.
