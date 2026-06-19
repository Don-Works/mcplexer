package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ListRecentWorkerRunsByWorkerIDs returns up to perWorker most-recent
// runs for EACH worker in workerIDs, in one query. Uses ROW_NUMBER()
// partitioned by worker_id (same window-function pattern as
// PruneWorkerRuns) so the per-worker limit applies inside SQLite rather
// than over-fetching N full histories. Workers without runs are absent
// from the map.
func (d *DB) ListRecentWorkerRunsByWorkerIDs(
	ctx context.Context, workerIDs []string, perWorker int,
) (map[string][]*store.WorkerRun, error) {
	out := make(map[string][]*store.WorkerRun, len(workerIDs))
	if len(workerIDs) == 0 {
		return out, nil
	}
	if perWorker <= 0 || perWorker > listWorkerRunCap {
		perWorker = listWorkerRunCap
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(workerIDs)), ",")
	args := make([]any, 0, len(workerIDs)+1)
	for _, id := range workerIDs {
		args = append(args, id)
	}
	args = append(args, perWorker)
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerRunCols+` FROM (
		    SELECT `+workerRunCols+`,
		        ROW_NUMBER() OVER (
		            PARTITION BY worker_id
		            ORDER BY started_at DESC
		        ) AS rn
		    FROM worker_runs
		    WHERE worker_id IN (`+placeholders+`)
		) ranked
		WHERE ranked.rn <= ?
		ORDER BY worker_id, started_at DESC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker_runs by worker ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r, err := scanWorkerRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan worker_run: %w", err)
		}
		out[r.WorkerID] = append(out[r.WorkerID], r)
	}
	return out, rows.Err()
}
