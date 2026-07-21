// memory_entries.go — extracted from memory_handler.go (see original for
// full route list). Split to keep memory_handler.go <= ~300 lines per project
// guideline. Same package "api"; handlers attach to memoryHandler at build.
//
// Contains: entityRefJSON + toStore..., create/search/invalidate/pin/unpin/
// delete/forget-by-source handlers + their request DTOs.
//
// parse* filters live in memory_filter.go (already split out).

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// entityRefJSON is the wire shape for an entity link on REST bodies
// (mirrors store.EntityRef + the MCP entity arg shape).
type entityRefJSON struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

func toStoreEntityRefs(in []entityRefJSON) []store.EntityRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.EntityRef, 0, len(in))
	for _, e := range in {
		if strings.TrimSpace(e.Kind) == "" || strings.TrimSpace(e.ID) == "" {
			continue
		}
		out = append(out, store.EntityRef{Kind: e.Kind, ID: e.ID, Role: e.Role})
	}
	return out
}

// createMemoryRequest is the POST /api/v1/memory body. Entities (when
// present) are linked in the same transaction as the row insert.
type createMemoryRequest struct {
	Name        string          `json:"name"`
	Content     string          `json:"content"`
	Kind        string          `json:"kind,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	WorkspaceID *string         `json:"workspace_id,omitempty"`
	Pinned      bool            `json:"pinned,omitempty"`
	Entities    []entityRefJSON `json:"entities,omitempty"`
}

// handleCreate serves POST /api/v1/memory.
//
// Emits a memory__save audit row mirroring the MCP shape so REST + MCP
// land identical entries (bug F053JE). The arguments payload includes
// `content` so internal/audit.Redact scrubs any secret-shaped substring
// before the row is persisted.
func (h *memoryHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var body createMemoryRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	auditParams := map[string]any{
		"name":         body.Name,
		"content":      body.Content,
		"kind":         body.Kind,
		"tags":         body.Tags,
		"metadata":     body.Metadata,
		"workspace_id": body.WorkspaceID,
		"pinned":       body.Pinned,
		"entities":     body.Entities,
	}
	id, err := h.svc.Write(r.Context(), memory.WriteOptions{
		Name:        body.Name,
		Kind:        body.Kind,
		Content:     body.Content,
		Tags:        body.Tags,
		Metadata:    body.Metadata,
		WorkspaceID: body.WorkspaceID,
		SourceKind:  store.MemorySourceHuman,
		Pinned:      body.Pinned,
		Entities:    toStoreEntityRefs(body.Entities),
	})
	if err != nil {
		h.recordAudit(r.Context(), "memory__save", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusBadRequest, "create failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__save", "success", "", auditParams, start)
	entry, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// searchMemoryRequest is the POST /api/v1/memory/search body. Entities
// + EntitiesAny add the "aboutness" axis (migration 076): Entities is
// AND across links; EntitiesAny is OR. ValidAt is the bi-temporal as-of
// axis (RFC3339): "what did we believe at this instant".
type searchMemoryRequest struct {
	Query          string          `json:"query"`
	Kind           string          `json:"kind,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Limit          int             `json:"limit,omitempty"`
	IncludeInvalid bool            `json:"include_invalid,omitempty"`
	ValidAt        string          `json:"valid_at,omitempty"`
	WorkspaceID    *string         `json:"workspace_id,omitempty"`
	Entities       []entityRefJSON `json:"entities,omitempty"`
	EntitiesAny    []entityRefJSON `json:"entities_any,omitempty"`
}

// parseValidAt converts an optional RFC3339 timestamp into the bi-temporal
// as-of pointer threaded onto MemoryFilter.ValidAt. Empty = nil (no error,
// current beliefs only); a non-empty value that fails to parse returns an
// error so the REST surface can answer 400 instead of silently dropping the
// filter. The querystring variant (?valid_at=) feeds through the same path.
func parseValidAt(raw string) (*time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf(
			"invalid valid_at %q: must be an RFC3339 timestamp "+
				"(e.g. 2026-01-15T09:00:00Z): %w", raw, err)
	}
	return &t, nil
}

// handleSearch serves POST /api/v1/memory/search.
//
// The bi-temporal as-of filter accepts valid_at in the JSON body OR as a
// ?valid_at= querystring param; the body wins when both are present. An
// unparseable timestamp is a 400 (clear error) rather than a silently
// dropped filter.
func (h *memoryHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body searchMemoryRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	validAtRaw := body.ValidAt
	if strings.TrimSpace(validAtRaw) == "" {
		validAtRaw = r.URL.Query().Get("valid_at")
	}
	validAt, err := parseValidAt(validAtRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := store.MemoryFilter{
		Scope:          scopeFromWorkspace(body.WorkspaceID),
		Kind:           body.Kind,
		Tags:           body.Tags,
		IncludeInvalid: body.IncludeInvalid,
		ValidAt:        validAt,
		Limit:          body.Limit,
		Entities:       toStoreEntityRefs(body.Entities),
		EntitiesAny:    toStoreEntityRefs(body.EntitiesAny),
	}
	hits, err := h.svc.Recall(r.Context(), f, body.Query, body.Limit)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"search failed", err.Error())
		return
	}
	if hits == nil {
		hits = []store.MemoryHit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

// invalidateMemoryRequest is the POST /api/v1/memory/{id}/invalidate body.
type invalidateMemoryRequest struct {
	SupersededByID string `json:"superseded_by_id,omitempty"`
}

// handleInvalidate serves POST /api/v1/memory/{id}/invalidate.
//
// Emits a memory__invalidate audit row on success (mirrors MCP shape).
func (h *memoryHandler) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body invalidateMemoryRequest
	_ = decodeJSON(r, &body) // best-effort: superseded_by_id is optional
	auditParams := map[string]any{
		"id":               id,
		"superseded_by_id": body.SupersededByID,
	}
	if err := h.svc.Invalidate(r.Context(), id, body.SupersededByID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.recordAudit(r.Context(), "memory__invalidate", "error", "not found", auditParams, start)
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		h.recordAudit(r.Context(), "memory__invalidate", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusInternalServerError,
			"invalidate failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__invalidate", "success", "", auditParams, start)
	w.WriteHeader(http.StatusNoContent)
}

// handlePin serves POST /api/v1/memory/{id}/pin — set pinned=true.
func (h *memoryHandler) handlePin(w http.ResponseWriter, r *http.Request) {
	h.setPinned(w, r, true)
}

// handleUnpin serves POST /api/v1/memory/{id}/unpin — set pinned=false.
func (h *memoryHandler) handleUnpin(w http.ResponseWriter, r *http.Request) {
	h.setPinned(w, r, false)
}

func (h *memoryHandler) setPinned(w http.ResponseWriter, r *http.Request, pinned bool) {
	start := time.Now()
	id := r.PathValue("id")
	// tool_name mirrors the MCP vocab: memory__pin / memory__unpin.
	toolName := "memory__pin"
	if !pinned {
		toolName = "memory__unpin"
	}
	auditParams := map[string]any{"id": id}
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.svc.SetPinned(r.Context(), id, pinned); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.recordAudit(r.Context(), toolName, "error", "not found", auditParams, start)
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		h.recordAudit(r.Context(), toolName, "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusInternalServerError,
			"set pinned failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), toolName, "success", "", auditParams, start)
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete serves DELETE /api/v1/memory/{id}.
//
// Emits a memory__forget audit row on success (mirrors MCP shape).
func (h *memoryHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	auditParams := map[string]any{"id": id}
	if err := h.svc.Forget(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.recordAudit(r.Context(), "memory__forget", "error", "not found", auditParams, start)
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		h.recordAudit(r.Context(), "memory__forget", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusInternalServerError,
			"delete failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__forget", "success", "", auditParams, start)
	w.WriteHeader(http.StatusNoContent)
}

// forgetBySourceRequest is the POST /api/v1/memory/forget-by-source body.
type forgetBySourceRequest struct {
	SourceSessionID string `json:"source_session_id"`
}

// handleForgetBySource serves POST /api/v1/memory/forget-by-source.
// Returns {count: N} of rows purged. N=0 is success, not 404 — idempotent.
//
// Emits a memory__forget_by_source audit row mirroring the MCP shape.
func (h *memoryHandler) handleForgetBySource(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var body forgetBySourceRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.SourceSessionID) == "" {
		writeError(w, http.StatusBadRequest, "source_session_id is required")
		return
	}
	auditParams := map[string]any{"source_session_id": body.SourceSessionID}
	n, err := h.svc.ForgetBySource(r.Context(), body.SourceSessionID, store.SkillScope{IncludeAll: true})
	if err != nil {
		h.recordAudit(r.Context(), "memory__forget_by_source", "error", err.Error(), auditParams, start)
		writeErrorDetail(w, http.StatusInternalServerError,
			"forget-by-source failed", err.Error())
		return
	}
	h.recordAudit(r.Context(), "memory__forget_by_source", "success", "", auditParams, start)
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// parseMemoryFilter, scopeFromQuery, scopeFromWorkspace and parseBoolQ
// live in memory_filter.go.
