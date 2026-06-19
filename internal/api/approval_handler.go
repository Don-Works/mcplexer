package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/store"
)

type approvalHandler struct {
	manager *approval.Manager
	store   store.ToolApprovalStore
}

func (h *approvalHandler) list(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	wsFilter := r.URL.Query().Get("workspace_id")

	if status == "pending" || status == "" {
		// Return in-memory pending approvals for realtime accuracy.
		pending := h.manager.ListPending("")
		if pending == nil {
			pending = []*store.ToolApproval{}
		}
		if wsFilter != "" {
			filtered := make([]*store.ToolApproval, 0, len(pending))
			for _, a := range pending {
				if a.WorkspaceID == wsFilter {
					filtered = append(filtered, a)
				}
			}
			pending = filtered
		}
		writeJSON(w, http.StatusOK, pending)
		return
	}

	// Any non-pending status ("resolved", "all", or a specific terminal
	// status) returns the resolved history, newest-first. The store caps
	// the row count; an exact terminal status further narrows it here.
	approvals, err := h.store.ListResolvedApprovals(r.Context(), 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list approvals")
		return
	}
	exact := status != "resolved" && status != "all"
	filtered := make([]store.ToolApproval, 0, len(approvals))
	for _, a := range approvals {
		if wsFilter != "" && a.WorkspaceID != wsFilter {
			continue
		}
		if exact && a.Status != status {
			continue
		}
		filtered = append(filtered, a)
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (h *approvalHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := h.store.GetToolApproval(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get approval")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *approvalHandler) resolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	approverSessionID := dashboardSessionID(r)
	if approverSessionID == "" {
		writeError(w, http.StatusUnauthorized, "approver identity required")
		return
	}

	err := h.manager.Resolve(id, approverSessionID, "dashboard", body.Reason, body.Approved)
	if err != nil {
		if errors.Is(err, approval.ErrAlreadyResolved) {
			writeError(w, http.StatusConflict, "approval already resolved")
			return
		}
		if errors.Is(err, approval.ErrSelfApproval) {
			writeError(w, http.StatusForbidden, "cannot self-approve from the same MCP session")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "denied"
	if body.Approved {
		action = "approved"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": action})
}

// dashboardSessionID derives a stable approver-session identifier from the
// caller's auth token. The dashboard prefix guarantees no collision with MCP
// session IDs, while the hash gives stability across requests for the same
// caller. Returns "" when no token can be extracted (auth middleware should
// have rejected the request first; this is defense-in-depth).
func dashboardSessionID(r *http.Request) string {
	tok, ok := extractToken(r)
	if !ok || tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	return "dashboard:" + hex.EncodeToString(sum[:8])
}
