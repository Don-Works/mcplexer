// Package runner — approvals.go owns the propose-first persistence
// path (M1). When a propose-mode Worker hits a write-class tool, the
// loop short-circuits to status=awaiting_approval and stores the
// pending tool call on loopState.pendingApproval. After the run row is
// finalized, finalize() calls persistApproval to write a
// WorkerApproval ledger row + emit a high-priority mesh alert.
//
// The approval is decided OUT-OF-BAND via the admin service
// (HTTP + MCP). Approve fires a NEW run with RunOpts.PreApprovedTools
// so propose-gating skips the named tool exactly once; reject stamps
// the original run as rejected. Mid-run resume of the original loop
// isn't supported in M1 (would need loop-state snapshotting) — see
// the comments on admin.Service.ApproveAndResume.
package runner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

// persistApproval writes the WorkerApproval row capturing the tool
// dispatch that needs operator review, then fires a high-priority
// mesh alert. Both surfaces drive UI: the dashboard's approvals page
// reads worker_approvals, and the mesh alert lights up the Signal
// tray + OS notifications via the existing notify bridge.
func (r *Runner) persistApproval(
	ctx context.Context,
	worker *store.Worker,
	runID string,
	pa *pendingApprovalInfo,
) {
	app := &store.WorkerApproval{
		WorkerID:  worker.ID,
		RunID:     runID,
		ToolName:  pa.toolName,
		ToolInput: pa.toolInput,
		Reason:    "write-class tool, propose-mode",
	}
	if err := r.store.CreateWorkerApproval(ctx, app); err != nil {
		slog.Error("persist worker approval failed",
			"worker_id", worker.ID, "run_id", runID,
			"tool", pa.toolName, "error", err)
		return
	}
	if r.mesh == nil {
		return
	}
	content := fmt.Sprintf(
		"Worker %q needs approval for tool %s (run %s)",
		worker.Name, pa.toolName, runID,
	)
	_, err := r.mesh.Send(ctx, MeshOutbound{
		Kind:     "alert",
		Priority: "high",
		Content:  content,
		Tags:     "worker,approval_needed",
		WorkerID: worker.ID,
		RunID:    runID,
	})
	if err != nil {
		slog.Warn("approval mesh alert failed",
			"worker_id", worker.ID, "run_id", runID, "error", err)
	}
}
