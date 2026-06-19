package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/don-works/mcplexer/internal/store"
)

type refinementHandler struct {
	store store.ToolDescriptionStore
}

func (h *refinementHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.ToolDescriptionFilter{Limit: 50}

	if v := q.Get("tool_name"); v != "" {
		filter.ToolName = &v
	}
	if v := q.Get("status"); v != "" {
		filter.Status = &v
	}
	if v := q.Get("source"); v != "" {
		filter.Source = &v
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	versions, total, err := h.store.ListToolDescriptionVersions(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list description versions")
		return
	}
	if versions == nil {
		versions = []store.ToolDescriptionVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":   versions,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func (h *refinementHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := h.store.GetToolDescriptionVersion(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "description version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get description version")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *refinementHandler) accept(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		ReviewNote string `json:"review_note"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	err := h.store.ActivateVersion(r.Context(), id, "dashboard", body.ReviewNote)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "description version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (h *refinementHandler) reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		ReviewNote string `json:"review_note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ReviewNote == "" {
		writeError(w, http.StatusBadRequest, "review_note is required for rejection")
		return
	}

	err := h.store.RejectVersion(r.Context(), id, "dashboard", body.ReviewNote)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "description version not found or already resolved")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (h *refinementHandler) submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ToolName    string `json:"tool_name"`
		Description string `json:"description"`
		Rationale   string `json:"rationale"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ToolName == "" || body.Description == "" {
		writeError(w, http.StatusBadRequest, "tool_name and description are required")
		return
	}

	v := &store.ToolDescriptionVersion{
		ToolName:    body.ToolName,
		Description: body.Description,
		Source:      "manual",
		Status:      "pending",
		Rationale:   body.Rationale,
	}
	if err := h.store.CreateToolDescriptionVersion(r.Context(), v); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create description version")
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
