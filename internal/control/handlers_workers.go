package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// workerToolNames enumerates every Workers admin tool that ships through
// the InternalBackend. Centralised so callBackup-style dispatch can
// match without restating the strings.
var workerToolNames = map[string]bool{
	"list_workers":               true,
	"get_worker":                 true,
	"create_worker":              true,
	"update_worker":              true,
	"delete_worker":              true,
	"pause_worker":               true,
	"resume_worker":              true,
	"run_worker_now":             true,
	"list_worker_runs":           true,
	"get_worker_run":             true,
	"cancel_worker_run":          true,
	"list_worker_approvals":      true,
	"approve_worker_approval":    true,
	"reject_worker_approval":     true,
	"publish_worker_as_template": true,
	"install_worker_template":    true,
	"list_worker_templates":      true,
	"worker_cost_aggregate":      true,
	// M4 — mesh trigger CRUD + per-peer grant convenience.
	"list_worker_mesh_triggers":  true,
	"create_worker_mesh_trigger": true,
	"update_worker_mesh_trigger": true,
	"delete_worker_mesh_trigger": true,
	"grant_trigger_to_peer":      true,
	"revoke_trigger_grant":       true,
	"spawn_subagent":             true,
}

// callWorker routes one of the worker admin tools to the *admin.Service
// the daemon wired into the InternalBackend. Returns a structured error
// result when the service isn't wired or the input doesn't decode.
func (b *InternalBackend) callWorker(
	ctx context.Context, name string, args json.RawMessage,
) json.RawMessage {
	svc := b.workerSvc
	if svc == nil {
		return errorResult(
			"worker admin service not available — daemon built without it",
		)
	}
	switch name {
	case "list_workers":
		return handleListWorkers(ctx, svc, args)
	case "get_worker":
		return handleGetWorker(ctx, svc, args)
	case "create_worker":
		return handleCreateWorker(ctx, svc, args)
	case "update_worker":
		return handleUpdateWorker(ctx, svc, args)
	case "delete_worker":
		return handleDeleteWorker(ctx, svc, args)
	case "pause_worker":
		return handlePauseWorker(ctx, svc, args)
	case "resume_worker":
		return handleResumeWorker(ctx, svc, args)
	case "run_worker_now":
		return handleRunWorkerNow(ctx, svc, args)
	case "list_worker_runs":
		return handleListWorkerRuns(ctx, svc, args)
	case "get_worker_run":
		return handleGetWorkerRun(ctx, svc, args)
	case "cancel_worker_run":
		return handleCancelWorkerRun(ctx, svc, args)
	case "list_worker_approvals":
		return handleListWorkerApprovals(ctx, svc, args)
	case "approve_worker_approval":
		return handleApproveWorkerApproval(ctx, svc, args)
	case "reject_worker_approval":
		return handleRejectWorkerApproval(ctx, svc, args)
	case "publish_worker_as_template":
		return handlePublishWorkerAsTemplate(ctx, svc, args)
	case "install_worker_template":
		return handleInstallWorkerTemplate(ctx, svc, args)
	case "list_worker_templates":
		return b.handleListWorkerTemplates(ctx, args)
	case "worker_cost_aggregate":
		return handleWorkerCostAggregate(ctx, svc, args)
	case "list_worker_mesh_triggers":
		return handleListWorkerMeshTriggers(ctx, svc, args)
	case "create_worker_mesh_trigger":
		return handleCreateWorkerMeshTrigger(ctx, svc, args)
	case "update_worker_mesh_trigger":
		return handleUpdateWorkerMeshTrigger(ctx, svc, args)
	case "delete_worker_mesh_trigger":
		return handleDeleteWorkerMeshTrigger(ctx, svc, args)
	case "grant_trigger_to_peer":
		return handleGrantTriggerToPeer(ctx, svc, args)
	case "revoke_trigger_grant":
		return handleRevokeTriggerGrant(ctx, svc, args)
	case "spawn_subagent":
		return b.handleSpawnSubagent(ctx, args)
	}
	return errorResult(fmt.Sprintf("unknown worker tool: %q", name))
}

func handlePublishWorkerAsTemplate(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.PublishAsTemplateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	out, err := svc.PublishAsTemplate(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(out)
}

func handleInstallWorkerTemplate(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.InstallFromTemplateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	worker, err := svc.InstallFromTemplate(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(worker)
}

func handleListWorkerApprovals(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.ListApprovalsInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errorResult("invalid params: " + err.Error())
		}
	}
	out, err := svc.ListApprovals(ctx, in)
	if err != nil {
		return errorResult(err.Error())
	}
	return mustJSONResult(out)
}

func handleApproveWorkerApproval(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID        string `json:"id"`
		DecidedBy string `json:"decided_by,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if in.DecidedBy == "" {
		in.DecidedBy = "agent"
	}
	out, err := svc.ApproveAndResume(ctx, in.ID, in.DecidedBy)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(out)
}

func handleRejectWorkerApproval(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID        string `json:"id"`
		DecidedBy string `json:"decided_by,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if in.DecidedBy == "" {
		in.DecidedBy = "agent"
	}
	out, err := svc.Reject(ctx, in.ID, in.DecidedBy)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(out)
}

func handleListWorkers(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.ListInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errorResult("invalid params: " + err.Error())
		}
	}
	out, err := svc.List(ctx, in)
	if err != nil {
		return errorResult(err.Error())
	}
	return mustJSONResult(out)
}

func handleGetWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.GetInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	out, err := svc.Get(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(out)
}

func handleCreateWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.CreateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	w, err := svc.Create(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(w)
}

func handleUpdateWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.UpdateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	w, err := svc.Update(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(w)
}

func handleDeleteWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if err := svc.Delete(ctx, in.ID); err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(map[string]bool{"deleted": true})
}

func handlePauseWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	return dispatchEnableToggle(ctx, args, svc.Pause)
}

func handleResumeWorker(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	return dispatchEnableToggle(ctx, args, svc.Resume)
}

// dispatchEnableToggle decodes the {id} payload and calls the named
// service method. Used by both pause_worker and resume_worker so each
// emits its own worker_admin.pause / worker_admin.resume audit verb
// (calling SetEnabled here would emit the generic .set_enabled verb).
func dispatchEnableToggle(
	ctx context.Context, args json.RawMessage,
	fn func(context.Context, string) (*store.Worker, error),
) json.RawMessage {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	w, err := fn(ctx, in.ID)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(w)
}

func handleRunWorkerNow(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	w, err := svc.Get(ctx, admin.GetInput{ID: in.ID})
	if err != nil {
		return mapWorkerErr(err)
	}
	if w == nil || w.Worker == nil {
		return errorResult("worker not found")
	}
	timeout := 10 * time.Minute
	if w.Worker.MaxWallClockSeconds > 0 {
		timeout = time.Duration(w.Worker.MaxWallClockSeconds+60) * time.Second
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if _, runErr := svc.RunNow(runCtx, in.ID); runErr != nil {
			slog.Warn("run_worker_now: detached run failed",
				"worker_id", in.ID, "error", runErr)
		}
	}()
	return mustJSONResult(map[string]string{
		"worker_id": in.ID,
		"status":    "dispatched",
	})
}

func handleListWorkerRuns(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.ListRunsInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	runs, err := svc.ListRuns(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(runs)
}

func handleGetWorkerRun(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	run, err := svc.GetRun(ctx, in.RunID)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(run)
}

func handleCancelWorkerRun(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.CancelRunInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	out, err := svc.CancelRun(ctx, in)
	if err != nil {
		return mapWorkerErr(err)
	}
	return mustJSONResult(out)
}

// handleWorkerCostAggregate dispatches mcplexer__worker_cost_aggregate
// to the same admin.Service.CostAggregate the HTTP endpoint calls. The
// payload shape matches admin.CostAggregateOutput so an agent sees the
// same fields the PWA dashboard renders.
func handleWorkerCostAggregate(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.CostAggregateInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errorResult("invalid params: " + err.Error())
		}
	}
	out, err := svc.CostAggregate(ctx, in)
	if err != nil {
		return errorResult(err.Error())
	}
	return mustJSONResult(out)
}

// mapWorkerErr converts the store's sentinel errors into a readable
// errorResult. Anything we don't recognise drops to the raw error
// message — the admin agent still sees something useful.
func mapWorkerErr(err error) json.RawMessage {
	switch {
	case errors.Is(err, store.ErrWorkerNotFound):
		return errorResult("worker not found")
	case errors.Is(err, store.ErrWorkerRunNotFound):
		return errorResult("worker run not found")
	case errors.Is(err, store.ErrRunNotCancellable):
		return errorResult("worker run already finished; not cancellable")
	case errors.Is(err, store.ErrWorkerApprovalNotFound):
		return errorResult("worker approval not found")
	case errors.Is(err, store.ErrAlreadyExists):
		return errorResult("worker with that name already exists in this workspace")
	}
	return errorResult(err.Error())
}

// mustJSONResult is jsonResult with the error swallowed into an
// errorResult — none of these structs ever fail to marshal in practice,
// and threading the error up through ten handlers is noise.
func mustJSONResult(v any) json.RawMessage {
	r, err := jsonResult(v)
	if err != nil {
		return errorResult(err.Error())
	}
	return r
}
