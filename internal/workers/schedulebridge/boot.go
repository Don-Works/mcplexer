package schedulebridge

import (
	"context"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

// WorkspaceLister mirrors the admin package's collaborator so the boot
// step can walk every workspace without importing admin.
type WorkspaceLister interface {
	ListWorkspaces(ctx context.Context) ([]store.Workspace, error)
}

// WorkerLister abstracts the store call we need for boot — listing
// enabled workers in a workspace. store.WorkerStore satisfies this
// natively.
type WorkerLister interface {
	ListWorkers(ctx context.Context, workspaceID string, enabledOnly bool) ([]*store.Worker, error)
}

// ResyncAllEnabled walks every workspace, lists each workspace's
// enabled workers, and calls EnsureForWorker on each. Used by the
// daemon at startup so a process restart re-establishes the scheduler
// rows for every persisted worker. A single bad worker is logged but
// never aborts the loop — the daemon keeps booting.
func (b *Bridge) ResyncAllEnabled(
	ctx context.Context,
	workspaces WorkspaceLister,
	workers WorkerLister,
) error {
	if b == nil {
		return nil
	}
	if workspaces == nil || workers == nil {
		return nil
	}
	wss, err := workspaces.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		b.resyncWorkspace(ctx, ws.ID, workers)
	}
	return nil
}

// resyncWorkspace lists + ensures every enabled worker inside one
// workspace. Errors are logged per worker and never bubble; the goal
// is best-effort recovery of the schedule after a restart.
func (b *Bridge) resyncWorkspace(
	ctx context.Context, wsID string, workers WorkerLister,
) {
	rows, err := workers.ListWorkers(ctx, wsID, true)
	if err != nil {
		slog.Warn("schedulebridge: list workers failed",
			"workspace_id", wsID, "error", err)
		return
	}
	for _, w := range rows {
		if err := b.EnsureForWorker(ctx, w); err != nil {
			slog.Warn("schedulebridge: ensure failed",
				"worker_id", w.ID, "workspace_id", wsID, "error", err)
		}
	}
}
