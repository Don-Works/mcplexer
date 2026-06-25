// Package api — workers_handler.go (M0.6) exposes the same Worker CRUD
// + run-control surface as the mcplexer__*_worker MCP tools, over the
// HTTP API the PWA consumes. The handler is a thin shim over
// workersadmin.Service so the two surfaces stay in lockstep — every
// admin action a human takes in the dashboard runs the same code path
// as an agent calling the equivalent MCP tool.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// workersHandler holds the workers admin service. svc is required; the
// router only registers routes when it's non-nil.
type workersHandler struct {
	svc      *workersadmin.Service
	settings *config.SettingsService // for reading DelegationDisabledProviders etc.
}

func (h *workersHandler) currentDelegationDisabled() map[string]bool {
	if h == nil || h.settings == nil {
		return map[string]bool{}
	}
	s := h.settings.Load(context.Background())
	if s.DelegationDisabledProviders == nil {
		return map[string]bool{}
	}
	// return a copy to avoid mutation surprises
	out := make(map[string]bool, len(s.DelegationDisabledProviders))
	for k, v := range s.DelegationDisabledProviders {
		out[k] = v
	}
	return out
}

// list serves GET /api/v1/workers. Query: workspace_id, enabled_only,
// name_pattern. workspace_id defaults to empty (= all workspaces).
func (h *workersHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := workersadmin.ListInput{
		WorkspaceID: q.Get("workspace_id"),
		NamePattern: q.Get("name_pattern"),
	}
	if v := q.Get("enabled_only"); v == "true" || v == "1" {
		in.EnabledOnly = true
	}
	rows, err := h.svc.List(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to list workers", err.Error())
		return
	}
	if rows == nil {
		rows = []workersadmin.WorkerSummary{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// get serves GET /api/v1/workers/{id}.
func (h *workersHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	out, err := h.svc.Get(r.Context(), workersadmin.GetInput{ID: id})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to get worker", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// create serves POST /api/v1/workers.
func (h *workersHandler) create(w http.ResponseWriter, r *http.Request) {
	var in workersadmin.CreateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	worker, err := h.svc.Create(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to create worker", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, worker)
}

// createDelegation serves POST /api/v1/delegations. It creates one or
// more one-shot workers and dispatches them asynchronously.
func (h *workersHandler) createDelegation(w http.ResponseWriter, r *http.Request) {
	var in workersadmin.DelegationInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Inject current operator disables so capacity/ranked selection and
	// resolve never pick from disabled provider groups.
	in.DisabledProviders = h.currentDelegationDisabled()
	out, err := h.svc.Delegate(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to create delegation", err.Error())
		return
	}
	if bus := h.svc.RunBus(); bus != nil {
		bus.Publish(&runner.RunEvent{Kind: "delegation_updated", DelegationID: out.DelegationID, Note: "created"})
	}
	writeJSON(w, http.StatusAccepted, out)
}

// listDelegations serves GET /api/v1/delegations.
func (h *workersHandler) listDelegations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := workersadmin.DelegationListInput{
		WorkspaceID: strings.TrimSpace(q.Get("workspace_id")),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = n
		}
	}
	rows, err := h.svc.ListDelegations(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to list delegations", err.Error())
		return
	}
	if rows == nil {
		rows = []workersadmin.DelegationContext{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// listDelegationModelCapacity serves GET /api/v1/delegations/model-capacity.
func (h *workersHandler) listDelegationModelCapacity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := workersadmin.DelegationCapacityListInput{
		WorkspaceID:       strings.TrimSpace(q.Get("workspace_id")),
		TaskKind:          strings.TrimSpace(q.Get("task_kind")),
		DisabledProviders: h.currentDelegationDisabled(),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = n
		}
	}
	rows, err := h.svc.ListDelegationModelCapacity(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to list delegation model capacity", err.Error())
		return
	}
	if rows == nil {
		rows = []workersadmin.DelegationModelCapacity{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// reviewDelegation serves POST /api/v1/delegations/{id}/review.
func (h *workersHandler) reviewDelegation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var in workersadmin.DelegationReviewInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.DelegationID = id
	out, err := h.svc.ReviewDelegation(r.Context(), in)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "delegation not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to review delegation", err.Error())
		return
	}
	if bus := h.svc.RunBus(); bus != nil {
		bus.Publish(&runner.RunEvent{Kind: "delegation_updated", DelegationID: out.ID, Note: "reviewed"})
	}
	writeJSON(w, http.StatusOK, out)
}

// extendDelegationBudget serves POST /api/v1/delegations/{id}/budget.
func (h *workersHandler) extendDelegationBudget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "delegation id is required")
		return
	}
	var in workersadmin.DelegationBudgetInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.DelegationID = id
	out, err := h.svc.ExtendDelegationBudget(r.Context(), in)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "delegation not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to extend delegation budget", err.Error())
		return
	}
	if bus := h.svc.RunBus(); bus != nil {
		bus.Publish(&runner.RunEvent{Kind: "delegation_updated", DelegationID: out.DelegationID, Note: "budget_extended"})
	}
	writeJSON(w, http.StatusOK, out)
}

// update serves PATCH /api/v1/workers/{id}.
func (h *workersHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var in workersadmin.UpdateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.ID = id // url path wins over any id in the body
	worker, err := h.svc.Update(r.Context(), in)
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update worker", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// remove serves DELETE /api/v1/workers/{id}.
func (h *workersHandler) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to delete worker", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pause / resume route through the dedicated Service methods so each
// emits its own worker_admin.pause / worker_admin.resume audit verb
// (instead of the generic worker_admin.set_enabled that SetEnabled
// produces).
func (h *workersHandler) pause(w http.ResponseWriter, r *http.Request) {
	h.toggleEnabled(w, r, h.svc.Pause)
}
func (h *workersHandler) resume(w http.ResponseWriter, r *http.Request) {
	h.toggleEnabled(w, r, h.svc.Resume)
}

// toggleEnabled centralises the id-extraction + error-mapping shared by
// the pause and resume HTTP handlers.
func (h *workersHandler) toggleEnabled(
	w http.ResponseWriter, r *http.Request,
	fn func(context.Context, string) (*store.Worker, error),
) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	worker, err := fn(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to toggle worker", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// runNow serves POST /api/v1/workers/{id}/run-now.
//
// Detached dispatch: a worker run can take minutes (LLM + tool calls +
// sandboxed subprocess work), and HTTP clients with their own default
// timeouts (Hammerspoon's hs.http.asyncPost defaults to ~60s) would
// close the connection mid-run. When that happens the Go http server
// cancels the request context, which propagates into the synchronous
// RunWithOpts call and SIGKILLs the model adapter's subprocess — runs
// die at ~60s wall with `signal: killed` and no stderr. So we detach
// the run onto a goroutine with a fresh background context, sized to
// the worker's own MaxWallClockSeconds + 60s headroom (matching the
// spawn_subagent dispatch pattern), and return 202 immediately. The
// run id will appear in the next /runs poll or SSE event.
func (h *workersHandler) runNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	got, err := h.svc.Get(r.Context(), workersadmin.GetInput{ID: id})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to look up worker", err.Error())
		return
	}
	timeout := time.Duration(got.Worker.MaxWallClockSeconds+60) * time.Second
	if got.Worker.MaxWallClockSeconds <= 0 {
		timeout = 10 * time.Minute
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if _, runErr := h.svc.RunNow(runCtx, id); runErr != nil {
			slog.Warn("run-now: detached run failed",
				"worker_id", id, "error", runErr)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"worker_id": id,
		"status":    "dispatched",
	})
}

// listRuns serves GET /api/v1/workers/{id}/runs?limit=N&status=X.
func (h *workersHandler) listRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	in := workersadmin.ListRunsInput{WorkerID: id, Status: strings.TrimSpace(r.URL.Query().Get("status"))}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = n
		}
	}
	runs, err := h.svc.ListRuns(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to list runs", err.Error())
		return
	}
	if runs == nil {
		runs = []*store.WorkerRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// listApprovals serves GET /api/v1/worker-approvals?status=pending.
// Empty status returns every approval (capped by the admin svc).
func (h *workersHandler) listApprovals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := workersadmin.ListApprovalsInput{
		Status: strings.TrimSpace(q.Get("status")),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = n
		}
	}
	rows, err := h.svc.ListApprovals(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "failed to list approvals", err.Error())
		return
	}
	if rows == nil {
		rows = []*store.WorkerApproval{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// approveApproval serves POST /api/v1/worker-approvals/{id}/approve.
// Body may carry { "decided_by": "..." } — when omitted the actor is
// recorded as "operator".
func (h *workersHandler) approveApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	decidedBy := decodeDecidedBy(r)
	out, err := h.svc.ApproveAndResume(r.Context(), id, decidedBy)
	if err != nil {
		if errors.Is(err, store.ErrWorkerApprovalNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to approve", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// rejectApproval serves POST /api/v1/worker-approvals/{id}/reject.
func (h *workersHandler) rejectApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	decidedBy := decodeDecidedBy(r)
	out, err := h.svc.Reject(r.Context(), id, decidedBy)
	if err != nil {
		if errors.Is(err, store.ErrWorkerApprovalNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to reject", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeDecidedBy extracts the optional decided_by field from the
// request body, defaulting to "operator". Body parse failures fall
// back to the default — the actor field is a UI nicety, not security.
func decodeDecidedBy(r *http.Request) string {
	var body struct {
		DecidedBy string `json:"decided_by,omitempty"`
	}
	_ = decodeJSON(r, &body) // best-effort
	if strings.TrimSpace(body.DecidedBy) == "" {
		return "operator"
	}
	return body.DecidedBy
}

// costAggregate serves GET /api/v1/workers/cost-aggregate?days=30&workspace_id=...
// — the workspace-wide cost dashboard payload. Days defaults to 30
// (the admin service enforces the upper bound). The optional
// workspace_id narrows the query; empty selects every workspace
// visible to the caller (CWD gate enforced at the MCP layer; the HTTP
// surface is already token-gated).
func (h *workersHandler) costAggregate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := workersadmin.CostAggregateInput{
		WorkspaceID: q.Get("workspace_id"),
	}
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Days = n
		}
	}
	out, err := h.svc.CostAggregate(r.Context(), in)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to aggregate worker costs", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getRun serves GET /api/v1/workers/{id}/runs/{run_id}.
func (h *workersHandler) getRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}
	run, err := h.svc.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrWorkerRunNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to get run", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// cancelRun serves POST /api/v1/worker-runs/{run_id}/cancel and the nested
// /api/v1/workers/{id}/runs/{run_id}/cancel alias. Body is optional:
// {"reason":"..."}.
func (h *workersHandler) cancelRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}
	in := workersadmin.CancelRunInput{RunID: runID}
	if r.Body != nil && r.ContentLength != 0 {
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		in.Reason = payload.Reason
	}
	out, err := h.svc.CancelRun(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrWorkerRunNotFound):
			writeError(w, http.StatusNotFound, "run not found")
		case errors.Is(err, store.ErrRunNotCancellable):
			writeError(w, http.StatusConflict, "run already finished; not cancellable")
		default:
			writeErrorDetail(w, http.StatusInternalServerError, "failed to cancel run", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}
