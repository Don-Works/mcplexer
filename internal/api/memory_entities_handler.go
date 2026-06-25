// memory_entities_handler.go — REST surface for the entity-link axis
// (migration 076). Powers the dashboard's entity picker, the
// /memory/about/:kind/:id route, and the "Top entities" tile.
//
// Routes registered in router.go:
//
//	GET    /api/v1/memory/entities           → list distinct entities
//	GET    /api/v1/memory/{id}/entities      → list links for one memory
//	POST   /api/v1/memory/{id}/entities      → add a link
//	DELETE /api/v1/memory/{id}/entities      → remove a link (body holds the ref)
package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// handleListEntities serves GET /api/v1/memory/entities.
//
// Querystring: kind (exact), limit, offset, workspace_id.
// Returns []store.EntitySummary ordered by memory_count DESC then
// last_linked_at DESC.
func (h *memoryHandler) handleListEntities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.EntityFilter{
		Scope:  scopeFromQuery(r),
		Kind:   strings.TrimSpace(q.Get("kind")),
		Limit:  limit,
		Offset: offset,
	}
	rows, err := h.svc.Entities(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list entities", err.Error())
		return
	}
	if rows == nil {
		rows = []store.EntitySummary{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleListMemoryEntities serves GET /api/v1/memory/{id}/entities.
// Returns the link rows for one memory, ordered by created_at ASC.
func (h *memoryHandler) handleListMemoryEntities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	rows, err := h.svc.MemoryEntities(r.Context(), id)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list memory entities", err.Error())
		return
	}
	if rows == nil {
		rows = []store.MemoryEntityRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// linkEntityRequest is the POST /api/v1/memory/{id}/entities body.
type linkEntityRequest struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

// handleLinkEntity serves POST /api/v1/memory/{id}/entities. Idempotent
// on (memory_id, kind, id, role).
//
// Emits a memory__link_entity audit row mirroring the MCP shape.
func (h *memoryHandler) handleLinkEntity(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	memID := r.PathValue("id")
	if memID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body linkEntityRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Kind) == "" || strings.TrimSpace(body.ID) == "" {
		writeError(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	auditParams := map[string]any{
		"memory_id": memID,
		"kind":      body.Kind,
		"id":        body.ID,
		"role":      body.Role,
	}
	ref := store.EntityRef{Kind: body.Kind, ID: body.ID, Role: body.Role}
	if err := h.svc.LinkEntity(r.Context(), memID, ref, ""); err != nil {
		h.recordAudit(r.Context(), "memory__link_entity", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusBadRequest, "link failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__link_entity", "success", "", auditParams, start)
	w.WriteHeader(http.StatusNoContent)
}

// handleRelatedEntities serves GET /api/v1/memory/entities/{kind}/{id}/related.
// Returns entities that co-link with the path entity in at least one
// memory, ranked by shared_count DESC. Powers the "Related entities"
// section on the dashboard's MemoryAboutPage.
func (h *memoryHandler) handleRelatedEntities(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	id := strings.TrimSpace(r.PathValue("id"))
	if kind == "" || id == "" {
		writeError(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := h.svc.RelatedEntities(r.Context(),
		store.EntityRef{Kind: kind, ID: id}, scopeFromQuery(r), limit)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"related entities failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.EntityCoLink{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleSpreadingActivation serves
// GET /api/v1/memory/entities/{kind}/{id}/spreading. Returns entities
// adjacent to the path entity via vec-neighbour walk through the
// memories about it. Empty when no embedding provider is configured.
func (h *memoryHandler) handleSpreadingActivation(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	id := strings.TrimSpace(r.PathValue("id"))
	if kind == "" || id == "" {
		writeError(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := h.svc.SpreadingActivation(r.Context(),
		store.EntityRef{Kind: kind, ID: id}, scopeFromQuery(r), limit, 20, 8)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"spread failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.EntityCoLink{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleCoRecalled serves GET /api/v1/memory/{id}/co-recalled. Returns
// memories that frequently co-surface with the path memory in the
// recall log (AR4). Empty when MCPLEXER_RECALL_TRACKING is off or no
// signal has accumulated.
func (h *memoryHandler) handleCoRecalled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := h.svc.CoRecalled(r.Context(), id, scopeFromQuery(r), limit)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"co-recalled failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.CoRecalledMemory{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleSuggestions serves GET /api/v1/memory/{id}/suggestions. Returns
// a unified "you might also remember" bundle composing co-recall +
// related-entity + semantic axes (AR5).
func (h *memoryHandler) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 12
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := h.svc.SuggestionsFor(r.Context(), id, scopeFromQuery(r), limit)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"suggestions failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.MemorySuggestion{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleUnlinkEntity serves DELETE /api/v1/memory/{id}/entities. The
// (kind, id, role) tuple to remove rides in the request body since net/http
// path params can't disambiguate the triple. Empty role removes all
// role flavours for the (memory, kind, id) triple.
func (h *memoryHandler) handleUnlinkEntity(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	memID := r.PathValue("id")
	if memID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body linkEntityRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Kind) == "" || strings.TrimSpace(body.ID) == "" {
		writeError(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	auditParams := map[string]any{
		"memory_id": memID,
		"kind":      body.Kind,
		"id":        body.ID,
		"role":      body.Role,
	}
	ref := store.EntityRef{Kind: body.Kind, ID: body.ID, Role: body.Role}
	if err := h.svc.UnlinkEntity(r.Context(), memID, ref); err != nil {
		h.recordAudit(r.Context(), "memory__unlink_entity", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusBadRequest, "unlink failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__unlink_entity", "success", "", auditParams, start)
	w.WriteHeader(http.StatusNoContent)
}
