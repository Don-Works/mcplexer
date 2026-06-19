// dream.go — dream-consolidator finalize hooks (harvest recipes + memory).
// A dream run is a regular worker run whose worker.Name == "dream-consolidator".
// The template is seeded by migration (101_dream_consolidator.sql) and
// scheduled off-peak (recommended nightly ~04:00 UTC, after memory-consolidator).
//
// On success we emit:
//  1. A dream__run audit row {workspace_id, actions_performed, run_id, started_at, finished_at}.
//     Downstream agents (and future recipe surfaces) detect "dream pass completed".
//  2. A low-priority kind=finding mesh broadcast (gated like consolidator).
//
// Both after the generic worker_run.finished so ordering is clear.
// The actions_performed counter is the generic consolidationsPerformed tally
// (memory__save + mcpx__skill_publish during the run); the dream prompt
// performs memory consolidation + recipe harvesting (distilling patterns
// into tagged global memory notes or published skills).
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// dreamWorkerName is the stable well-known name for the scheduled
// dream-mode consolidation worker (recipes + memory). The runner
// and any future auto-install / scheduler surface key off this exact string.
const dreamWorkerName = "dream-consolidator"

// runDreamFinalize is the dream-consolidator domain finalize hook.
// Called from finalize after generic work; short-circuits for non-dream
// workers or non-success outcomes (no "dream completed" claim on failure).
func (r *Runner) runDreamFinalize(
	ctx context.Context,
	worker *store.Worker,
	runID string,
	actionsPerformed int,
	startedAt, finishedAt time.Time,
	status string,
) {
	if worker == nil || worker.Name != dreamWorkerName {
		return
	}
	if status != StatusSuccess {
		return
	}
	r.emitAuditDreamRun(
		ctx, worker.ID, runID, worker.WorkspaceID,
		actionsPerformed, startedAt, finishedAt,
	)
	r.emitDreamMeshBroadcast(
		ctx, worker.ID, runID, actionsPerformed, finishedAt,
	)
}

// emitDreamMeshBroadcast fires a low-priority finding describing the
// dream pass (memory compaction + recipe harvest). Same gating as
// consolidator: cross-peer only after the same-user peer gate confirms a
// Tier-1 peer; nil lister emits a local-only provenance row.
func (r *Runner) emitDreamMeshBroadcast(
	ctx context.Context, workerID, runID string,
	actionsPerformed int, finishedAt time.Time,
) {
	if r.mesh == nil {
		return
	}
	broadcastPeers := false
	if r.peerTiers != nil {
		if !r.peerTiers.HasSameUserPeer(ctx) {
			return
		}
		broadcastPeers = true
	}
	who := r.selfDisplay
	if who == "" {
		who = "self"
	}
	content := fmt.Sprintf(
		"%s ran dream consolidation at %s — %d actions (memory+recipes)",
		who,
		finishedAt.UTC().Format("15:04"),
		actionsPerformed,
	)
	r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "finding",
		Priority: "low",
		Tags:     "dream,consolidator,recipes,memory,harvested",
		Content:  content,
		// Deliberate cross-machine delivery: only set after the
		// SameUserPeerLister has confirmed a Tier-1 peer exists.
		BroadcastPeers: broadcastPeers,
	})
}
