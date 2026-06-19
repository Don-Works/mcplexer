package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// skillRefinementHandler exposes the W3 refinement proposals over the
// dashboard's REST surface. The mcpx tool `skill__propose_refinement`
// is the agent's write path; this is the human's read + resolve path.
// They share the same Store so writes show up immediately in both.
type skillRefinementHandler struct {
	store store.Store
}

func (h *skillRefinementHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	filter := store.RefinementFilter{
		SkillName:   strings.TrimSpace(q.Get("skill")),
		Status:      strings.TrimSpace(q.Get("status")),
		WorkspaceID: strings.TrimSpace(q.Get("workspace_id")),
		Limit:       limit,
	}
	rows, err := h.store.ListRefinementProposals(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list refinement proposals")
		return
	}
	if rows == nil {
		rows = []store.SkillRefinementProposal{}
	}
	writeJSON(w, http.StatusOK, listRefinementResponse{
		Proposals: rows,
		Count:     len(rows),
	})
}

type listRefinementResponse struct {
	Proposals []store.SkillRefinementProposal `json:"proposals"`
	Count     int                             `json:"count"`
}

func (h *skillRefinementHandler) get(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	p, err := h.store.GetRefinementProposal(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "refinement proposal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load refinement proposal")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type resolveRefinementRequest struct {
	Action string `json:"action"` // "promote" | "reject"
	Note   string `json:"note"`
}

type resolveRefinementResponse struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	ResolvedAt time.Time `json:"resolved_at"`
}

func (h *skillRefinementHandler) resolve(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	var req resolveRefinementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	status, ok := refinementActionStatus(action)
	if !ok {
		writeError(w, http.StatusBadRequest, "action must be \"promote\" or \"reject\"")
		return
	}

	// Make sure the proposal exists before applying a patch, so the
	// 404 distinguishes "missing" from "stale review action".
	existing, err := h.store.GetRefinementProposal(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "refinement proposal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load refinement proposal")
		return
	}
	if existing.Status == store.RefinementStatusPromoted ||
		existing.Status == store.RefinementStatusRejected {
		// Idempotent re-resolve is harmless if the action matches, but
		// flipping a promoted proposal to rejected (or vice versa)
		// would muddy the audit trail. Refuse loudly — the reviewer
		// can record their reversal as a new note on the original.
		writeError(w, http.StatusConflict,
			"proposal already resolved; cannot re-resolve")
		return
	}

	note := strings.TrimSpace(req.Note)
	patch := store.RefinementProposalPatch{
		Status:         &status,
		ResolutionNote: &note,
	}
	if err := h.store.UpdateRefinementProposal(r.Context(), id, patch); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update refinement proposal")
		return
	}

	// TODO(W3-followup): when action == "promote", actually create a
	// new skill_registry entry that supersedes the current head + records
	// the suggested_change as the new SKILL.md body delta. Out of scope
	// for this milestone — the promotion records the DECISION only,
	// the version bump is a separate step that runs against W2 telemetry
	// (A/B winner selection) to confirm the candidate beats the
	// incumbent before flipping the @stable tag.

	resolved, err := h.store.GetRefinementProposal(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to re-load refinement proposal")
		return
	}
	var resolvedAt time.Time
	if resolved.ResolvedAt != nil {
		resolvedAt = *resolved.ResolvedAt
	}
	writeJSON(w, http.StatusOK, resolveRefinementResponse{
		ID:         resolved.ID,
		Status:     resolved.Status,
		ResolvedAt: resolvedAt,
	})
}

// refinementActionStatus translates the REST action string into the
// store's status enum. Returns (status, ok). Unknown actions fail
// with ok=false so the handler can write a 400.
func refinementActionStatus(action string) (string, bool) {
	switch action {
	case "promote":
		return store.RefinementStatusPromoted, true
	case "reject":
		return store.RefinementStatusRejected, true
	default:
		return "", false
	}
}
