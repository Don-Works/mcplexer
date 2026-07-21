// delegation_gc.go — retention sweep for delegation-created workers.
// Delegation workers are one-shot contexts; once their work is done and
// reviewed they only add noise (and query load — capacity ranking walks
// the whole delegation list). The sweep auto-disables + archives any
// delegation worker whose last terminal run finished more than the
// retention window ago. Wired into the nightly retention tick (see
// cmd/mcplexer/retention.go).
package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultDelegationRetention is how long a delegation worker survives
// after its last terminal run before the sweep archives it.
const DefaultDelegationRetention = 14 * 24 * time.Hour

// SweepDelegationRetention disables + archives delegation-created
// workers idle past `retention` (<= 0 → DefaultDelegationRetention).
// A worker qualifies when:
//   - it carries delegation metadata and is not already archived,
//   - it has no run still in a live status (running/awaiting_approval),
//   - its last activity (latest terminal run's FinishedAt, falling back
//     to the worker's UpdatedAt when it never ran) is older than the
//     cutoff.
//
// Archiving = Enabled=false + metadata archived_at stamp; archived
// workers drop out of ListDelegations (and capacity ranking). Rows and
// runs are intentionally kept — the run ledger ages out separately via
// PruneWorkerRuns. Returns the number of workers archived; per-worker
// failures are logged and skipped so one bad row never aborts the sweep.
func (s *Service) SweepDelegationRetention(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		retention = DefaultDelegationRetention
	}
	cutoff := s.clock.Now().UTC().Add(-retention)
	rows, err := s.listDelegationWorkers(ctx, "")
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.worker.ID)
	}
	runsByWorker, err := s.recentRunsByWorker(ctx, ids)
	if err != nil {
		return 0, err
	}
	archived := 0
	for _, row := range rows {
		if !delegationWorkerExpired(row, runsByWorker[row.worker.ID], cutoff) {
			continue
		}
		if err := s.archiveDelegationWorker(ctx, row); err != nil {
			slog.Warn("delegation retention: archive failed",
				"worker_id", row.worker.ID, "error", err)
			continue
		}
		archived++
	}
	if archived > 0 {
		slog.Info("delegation retention: archived idle delegation workers",
			"archived", archived, "retention", retention.String())
	}
	return archived, nil
}

// delegationWorkerExpired decides whether one delegation worker is past
// retention. Any live run vetoes; otherwise the most recent terminal
// run's FinishedAt (or the worker's UpdatedAt when it never ran — e.g.
// a dispatch failure) must be older than cutoff.
func delegationWorkerExpired(row delegationWorkerRow, runs []*store.WorkerRun, cutoff time.Time) bool {
	lastActivity := row.worker.UpdatedAt
	for _, run := range runs {
		switch run.Status {
		case "running", "awaiting_approval":
			return false
		}
		if run.FinishedAt != nil && run.FinishedAt.After(lastActivity) {
			lastActivity = *run.FinishedAt
		}
	}
	if lastActivity.IsZero() {
		return false
	}
	return lastActivity.Before(cutoff)
}

// archiveDelegationWorker stamps archived_at into the metadata and
// disables the worker in one Update.
func (s *Service) archiveDelegationWorker(ctx context.Context, row delegationWorkerRow) error {
	now := s.clock.Now().UTC()
	meta := row.meta
	// Most delegations are never reviewed (review_required defaults off),
	// so retention archive is the reliable release point for the
	// delegation's advisory file claim; releasing is idempotent, so the
	// per-worker fan-in of one delegation is harmless.
	s.releaseDelegationFileClaims(ctx, meta.ID)
	meta.ArchivedAt = &now
	params, err := updateDelegationMetadataJSON(row.worker.ParametersJSON, meta)
	if err != nil {
		return err
	}
	enabled := false
	_, err = s.Update(ctx, UpdateInput{
		ID:             row.worker.ID,
		ParametersJSON: &params,
		Enabled:        &enabled,
	})
	return err
}
