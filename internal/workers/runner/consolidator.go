// consolidator.go — memory-consolidator-specific finalize hooks. A
// consolidator run is just a regular worker run whose worker.Name ==
// "memory-consolidator" (the well-known per-workspace name maintained
// by internal/api/memory_consolidate_handler.go). When that run lands
// status=success we fire two extra signals beyond the generic
// worker_run.finished envelope:
//
//  1. A memory__consolidator_run audit row carrying
//     {workspace_id, consolidations_performed, run_id, started_at,
//     finished_at}. Downstream agents read this to detect "machine A
//     just ran the consolidator" without diffing memory snapshots.
//
//  2. A kind=finding/priority=low mesh broadcast describing the run.
//     The broadcast is gated on the presence of at least one Tier-1
//     (same-user) paired peer — same-machine setups and tests
//     skip the mesh emission (when PeerTiers is wired). When
//     PeerTiers is nil, the broadcast fires unconditionally so
//     single-machine flows still see the provenance event locally.
//
// Both signals fire AFTER the generic finalize work (run row persisted,
// worker_run.finished audit + mesh-finished signal sent) so an audit
// reader sees the worker lifecycle row before the consolidator-domain
// row — preserving the "run completed → domain event" ordering.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// consolidatorWorkerName is the well-known per-workspace Worker.Name
// the memory-consolidator REST surface materialises from the
// memory-consolidator template. Stable: the API handler, the runner's
// consolidator finalize path, and the bulletproof harness all key off
// this exact string.
const consolidatorWorkerName = "memory-consolidator"

// runConsolidatorFinalize is the consolidator's domain-level finalize
// hook. Called from r.finalize unconditionally; short-circuits when:
//
//   - The worker isn't the memory-consolidator
//   - The outcome wasn't success (we don't emit the "consolidated"
//     audit row on cap_exceeded / failure — there's no consolidation
//     to report, and the generic worker_run.finished row already
//     carries the failure reason)
//
// startedAt is the run's StartedAt; finishedAt is the wall-clock
// finalize timestamp (matches the generic finalize row).
func (r *Runner) runConsolidatorFinalize(
	ctx context.Context,
	worker *store.Worker,
	runID string,
	consolidationsPerformed int,
	startedAt, finishedAt time.Time,
	status string,
) {
	if worker == nil || worker.Name != consolidatorWorkerName {
		return
	}
	if status != StatusSuccess {
		return
	}
	r.emitAuditMemoryConsolidatorRun(
		ctx, worker.ID, runID, worker.WorkspaceID,
		consolidationsPerformed, startedAt, finishedAt,
	)
	r.emitConsolidatorMeshBroadcast(
		ctx, worker.ID, runID, consolidationsPerformed, finishedAt,
	)
}

// emitConsolidatorMeshBroadcast fires a kind=finding mesh message
// describing the just-completed consolidator pass. Gated on the
// SameUserPeerLister — when wired, the broadcast only crosses peers if at
// least one Tier-1 same-user peer would receive it. nil lister still emits
// the row locally for tests + single-machine flows, but does not opt into
// cross-peer delivery.
//
// Best-effort: a degraded mesh sender never fails the consolidator
// run; emitSignal already logs the error and returns "".
func (r *Runner) emitConsolidatorMeshBroadcast(
	ctx context.Context, workerID, runID string,
	consolidationsPerformed int, finishedAt time.Time,
) {
	if r.mesh == nil {
		return
	}
	broadcastPeers := false
	if r.peerTiers != nil {
		if !r.peerTiers.HasSameUserPeer(ctx) {
			// No Tier-1 peer to receive the provenance row — skip the
			// emit so a single-machine deployment doesn't accumulate
			// mesh chatter no other agent will read.
			return
		}
		broadcastPeers = true
	}
	who := r.selfDisplay
	if who == "" {
		who = "self"
	}
	content := fmt.Sprintf(
		"%s ran consolidator at %s — %d consolidations",
		who,
		finishedAt.UTC().Format("15:04"),
		consolidationsPerformed,
	)
	r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "finding",
		Priority: "low",
		Tags:     "memory,consolidator,memory_consolidated",
		Content:  content,
		// Deliberate cross-machine delivery: only set after the
		// SameUserPeerLister has confirmed a Tier-1 peer exists.
		BroadcastPeers: broadcastPeers,
	})
}
