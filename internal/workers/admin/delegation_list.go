// delegation_list.go — ListDelegations and its helpers. Replaces the
// historical List-then-Get-per-row N+1: workers are listed once per
// workspace, grouped by delegation id from their embedded metadata, the
// limit is applied BEFORE run hydration, and the surviving workers' runs
// are fetched in one batched store query.
package admin

import (
	"context"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// batchRunLister is the optional store capability ListDelegations uses
// to hydrate runs in one query. store.Store / *sqlite.DB satisfy it;
// narrow fakes that only implement store.WorkerStore fall back to the
// per-worker ListWorkerRuns path.
type batchRunLister interface {
	ListRecentWorkerRunsByWorkerIDs(ctx context.Context, workerIDs []string, perWorker int) (map[string][]*store.WorkerRun, error)
}

// delegationWorkerRow pairs a delegation worker with its parsed metadata.
type delegationWorkerRow struct {
	worker *store.Worker
	meta   delegationMetadata
}

func (s *Service) ListDelegations(ctx context.Context, in DelegationListInput) ([]DelegationContext, error) {
	// This is the query the DelegationsPage (and any MCP/list caller) uses.
	// While workers for a delegation are running, the frontend subscribes to
	// the multiplexed 'workers' SSE channel (fed by RunBus) and refetches this
	// on status/usage/delegation_updated events, making the page live without
	// a blind poll or page reload.
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.listDelegationWorkers(ctx, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*DelegationContext{}
	for _, row := range rows {
		d := byID[row.meta.ID]
		if d == nil {
			d = newDelegationContext(row.worker, row.meta)
			byID[row.meta.ID] = d
		}
		if row.worker.UpdatedAt.After(d.UpdatedAt) {
			d.UpdatedAt = row.worker.UpdatedAt
		}
		d.Workers = append(d.Workers, DelegationWorkerContext{
			Worker:         row.worker,
			ParallelIndex:  row.meta.ParallelIndex,
			ParallelTotal:  row.meta.ParallelTotal,
			DispatchFailed: row.meta.DispatchFailed,
			DispatchError:  row.meta.DispatchError,
		})
	}
	out := make([]*DelegationContext, 0, len(byID))
	for _, d := range byID {
		out = append(out, d)
	}
	// Apply the limit BEFORE hydrating runs. Ordering here uses
	// worker-row UpdatedAt (bumped by create/review/metadata writes);
	// after hydration UpdatedAt is refreshed with run finish times and
	// the survivors re-sorted. A delegation whose ONLY recent activity
	// is a run on an otherwise-untouched worker may sort slightly low at
	// the cut — accepted: delegations are one-shot and the cap is 200.
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	if err := s.hydrateDelegationRuns(ctx, out); err != nil {
		return nil, err
	}
	final := make([]DelegationContext, 0, len(out))
	for _, d := range out {
		sort.Slice(d.Workers, func(i, j int) bool {
			return d.Workers[i].ParallelIndex < d.Workers[j].ParallelIndex
		})
		d.Aggregate, d.Status = aggregateDelegation(*d)
		d.ModelStats = modelStatsForDelegation(*d)
		final = append(final, *d)
	}
	sort.Slice(final, func(i, j int) bool { return final[i].UpdatedAt.After(final[j].UpdatedAt) })
	return final, nil
}

// CountUnreviewedRequiredDelegations returns the number of distinct
// delegation contexts (across all workspaces) that have review_required
// set and have not yet received a review. It reuses the lightweight
// listDelegationWorkers scan (no run hydration) so it is cheap enough
// for the dashboard path.
func (s *Service) CountUnreviewedRequiredDelegations(ctx context.Context) (int, error) {
	rows, err := s.listDelegationWorkers(ctx, "")
	if err != nil {
		return 0, err
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if delegationMetadataReviewRequired(row.meta) && !row.meta.Review.Reviewed {
			seen[row.meta.ID] = struct{}{}
		}
	}
	return len(seen), nil
}

// listDelegationWorkers returns every non-archived delegation worker in
// the workspace(s), pre-parsed. One ListWorkers call per workspace — no
// per-row Get.
func (s *Service) listDelegationWorkers(ctx context.Context, workspaceID string) ([]delegationWorkerRow, error) {
	workspaceIDs, err := s.collectWorkspaceIDs(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []delegationWorkerRow
	for _, wsID := range workspaceIDs {
		workers, err := s.store.ListWorkers(ctx, wsID, false)
		if err != nil {
			return nil, err
		}
		for _, w := range workers {
			if !strings.HasPrefix(strings.ToLower(w.Name), delegationWorkerPrefix) {
				continue
			}
			meta, ok := parseDelegationMetadata(w.ParametersJSON)
			if !ok {
				continue
			}
			if meta.ArchivedAt != nil {
				// Retention sweep archived this worker — drop it from the
				// delegations surface (and from capacity ranking, which
				// reads this list).
				continue
			}
			out = append(out, delegationWorkerRow{worker: w, meta: meta})
		}
	}
	return out, nil
}

// hydrateDelegationRuns attaches RecentRuns/LatestRun to every worker of
// the (already limited) delegation set via one batched query, falling
// back to per-worker ListWorkerRuns when the store can't batch.
func (s *Service) hydrateDelegationRuns(ctx context.Context, ds []*DelegationContext) error {
	ids := make([]string, 0)
	for _, d := range ds {
		for i := range d.Workers {
			if w := d.Workers[i].Worker; w != nil {
				ids = append(ids, w.ID)
			}
		}
	}
	runsByWorker, err := s.recentRunsByWorker(ctx, ids)
	if err != nil {
		return err
	}
	for _, d := range ds {
		for i := range d.Workers {
			w := d.Workers[i].Worker
			if w == nil {
				continue
			}
			runs := runsByWorker[w.ID]
			s.annotateRunsAnnotations(ctx, runs, w)
			if runs == nil {
				runs = []*store.WorkerRun{}
			}
			d.Workers[i].RecentRuns = runs
			if len(runs) > 0 {
				latest := runs[0]
				d.Workers[i].LatestRun = latest
				if latest.FinishedAt != nil && latest.FinishedAt.After(d.UpdatedAt) {
					d.UpdatedAt = *latest.FinishedAt
				}
			}
		}
	}
	return nil
}

// recentRunsByWorker fetches up to 5 recent runs per worker — batched
// when the store supports it.
func (s *Service) recentRunsByWorker(
	ctx context.Context, workerIDs []string,
) (map[string][]*store.WorkerRun, error) {
	const perWorker = 5
	if bl, ok := s.store.(batchRunLister); ok {
		return bl.ListRecentWorkerRunsByWorkerIDs(ctx, workerIDs, perWorker)
	}
	out := make(map[string][]*store.WorkerRun, len(workerIDs))
	for _, id := range workerIDs {
		runs, err := s.store.ListWorkerRuns(ctx, id, perWorker)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			out[id] = runs
		}
	}
	return out, nil
}

// newDelegationContext builds the skeleton context from the first
// worker row seen for a delegation id.
func newDelegationContext(w *store.Worker, meta delegationMetadata) *DelegationContext {
	d := &DelegationContext{
		ID:                 meta.ID,
		WorkspaceID:        w.WorkspaceID,
		Objective:          meta.Objective,
		Handoff:            meta.Handoff,
		TaskID:             meta.TaskID,
		TaskKind:           meta.TaskKind,
		TaskShape:          meta.TaskShape,
		WorkerMode:         meta.WorkerMode,
		Warnings:           meta.Warnings,
		ModelSelectionMode: meta.ModelSelectionMode,
		ReviewRequired:     delegationMetadataReviewRequired(meta),
		ParallelTotal:      meta.ParallelTotal,
		CreatedAt:          meta.CreatedAt,
		UpdatedAt:          w.UpdatedAt,
		Baseline: DelegationBaseline{
			TokensEstimate: meta.BaselineTokensEstimate,
			CostUSD:        meta.BaselineCostUSD,
		},
		Review: meta.Review,
		Parent: DelegationParent{
			ContextID:    meta.ParentContextID,
			SessionID:    meta.ParentSessionID,
			Model:        meta.ParentModel,
			InputTokens:  meta.ParentInputTokens,
			OutputTokens: meta.ParentOutputTokens,
			CostUSD:      meta.ParentCostUSD,
		},
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = w.CreatedAt
	}
	return d
}
