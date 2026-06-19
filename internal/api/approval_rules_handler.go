package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/store"
)

// approvalRulesHandler serves CRUD on the approval_rules table. The
// rules drive the Shell Guard's "trusted allowlist" policy: matching
// rules auto-approve (or auto-deny) requests after a 5s grace period
// instead of forcing a human prompt. Every mutating call here also
// re-loads the active resolver's snapshot via mgr.ReloadPolicyRules so
// edits take effect without a daemon restart.
type approvalRulesHandler struct {
	store store.ApprovalRuleStore
	mgr   *approval.Manager // optional; nil disables hot-reload
}

func (h *approvalRulesHandler) reload(ctx context.Context) {
	if h.mgr == nil || h.store == nil {
		return
	}
	_ = h.mgr.ReloadPolicyRules(ctx, h.store)
}

// list serves GET /api/v1/approval-rules?surface=shell. surface is
// optional; empty returns every rule, ordered by priority ASC.
func (h *approvalRulesHandler) list(w http.ResponseWriter, r *http.Request) {
	surface := r.URL.Query().Get("surface")
	rules, err := h.store.ListApprovalRules(r.Context(), surface)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []store.ApprovalRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

// approvalRuleInput is the wire shape used by create + update. ID and
// audit columns are server-set so the dashboard form stays minimal.
type approvalRuleInput struct {
	Surface        string  `json:"surface"`
	Pattern        string  `json:"pattern"`
	Directory      string  `json:"directory"`
	AISessionID    string  `json:"ai_session_id"`
	Decision       string  `json:"decision"`
	Priority       int     `json:"priority"`
	ExpiresAt      *string `json:"expires_at,omitempty"` // RFC3339 or null
	CreatedBy      string  `json:"created_by"`
	AllowMetachars bool    `json:"allow_metachars,omitempty"`
}

func (in *approvalRuleInput) toRule(id string, now time.Time) (*store.ApprovalRule, error) {
	if in.Surface == "" || in.Decision == "" {
		return nil, errors.New("surface and decision are required")
	}
	if in.Decision != "allow" && in.Decision != "deny" && in.Decision != "prompt" {
		return nil, errors.New("decision must be allow | deny | prompt")
	}
	rule := &store.ApprovalRule{
		ID:             id,
		Surface:        in.Surface,
		Pattern:        in.Pattern,
		Directory:      in.Directory,
		AISessionID:    in.AISessionID,
		Decision:       in.Decision,
		Priority:       in.Priority,
		CreatedBy:      in.CreatedBy,
		CreatedAt:      now,
		UpdatedAt:      now,
		AllowMetachars: in.AllowMetachars,
	}
	if in.ExpiresAt != nil && *in.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *in.ExpiresAt)
		if err != nil {
			return nil, errors.New("expires_at must be RFC3339")
		}
		rule.ExpiresAt = &t
	}
	return rule, nil
}

// create serves POST /api/v1/approval-rules. Generates an ID, validates,
// inserts, reloads the resolver snapshot.
func (h *approvalRulesHandler) create(w http.ResponseWriter, r *http.Request) {
	var in approvalRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rule, err := in.toRule(uuid.NewString(), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateApprovalRule(r.Context(), rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.reload(r.Context())
	writeJSON(w, http.StatusCreated, rule)
}

// update serves PUT /api/v1/approval-rules/{id}. The path id wins over
// any id in the body so a stale form can't repoint an unrelated rule.
func (h *approvalRulesHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	existing, err := h.store.GetApprovalRule(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var in approvalRuleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rule, err := in.toRule(existing.ID, existing.CreatedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule.HitCount = existing.HitCount
	rule.LastHitAt = existing.LastHitAt
	if err := h.store.UpdateApprovalRule(r.Context(), rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.reload(r.Context())
	writeJSON(w, http.StatusOK, rule)
}

// delete serves DELETE /api/v1/approval-rules/{id}.
func (h *approvalRulesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	if err := h.store.DeleteApprovalRule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}
