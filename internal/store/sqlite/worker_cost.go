// Package sqlite — worker_cost.go (M2) implements the workspace-wide
// cost aggregation query that backs /api/v1/workers/cost-aggregate.
// Pulled into its own file so worker_run.go stays focused on per-run
// CRUD and the aggregation logic is easy to review in isolation.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// workerCostAggregateMaxDays caps the requested day window so a wild
// `days=100000` query can't materialise an enormous per-day grid.
const workerCostAggregateMaxDays = 365

// WorkerCostAggregate returns per-worker cost rollups for the cost
// dashboard. See the store.Store interface contract for semantics.
func (d *DB) WorkerCostAggregate(
	ctx context.Context, workspaceID string, days int, now time.Time,
) ([]store.WorkerCostAggregate, error) {
	if days <= 0 {
		days = 30
	}
	if days > workerCostAggregateMaxDays {
		days = workerCostAggregateMaxDays
	}
	nowUTC := now.UTC()
	windowStart := time.Date(
		nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC,
	).AddDate(0, 0, -(days - 1))
	monthStart := time.Date(
		nowUTC.Year(), nowUTC.Month(), 1, 0, 0, 0, 0, time.UTC,
	)

	workers, err := d.listWorkerIdentities(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if len(workers) == 0 {
		return []store.WorkerCostAggregate{}, nil
	}
	dailyByWorker, runCountByWorker, err := d.queryWorkerDailyCosts(
		ctx, workers, windowStart,
	)
	if err != nil {
		return nil, err
	}
	mtdByWorker, err := d.queryWorkerMTDCosts(ctx, workers, monthStart)
	if err != nil {
		return nil, err
	}
	out := make([]store.WorkerCostAggregate, 0, len(workers))
	for _, w := range workers {
		row := store.WorkerCostAggregate{
			WorkerID:       w.id,
			WorkerName:     w.name,
			WorkspaceID:    w.workspaceID,
			DailyCosts:     buildDailySeries(windowStart, days, dailyByWorker[w.id]),
			MonthToDateUSD: mtdByWorker[w.id],
			RunCount30D:    runCountByWorker[w.id],
		}
		out = append(out, row)
	}
	return out, nil
}

// workerIdentity is the slim row the aggregator carries through the
// per-worker map lookups. Avoids pulling the full Worker row when we
// only need (id, name, workspace_id).
type workerIdentity struct {
	id          string
	name        string
	workspaceID string
}

// listWorkerIdentities returns id+name+workspace_id for every worker in
// scope. Empty workspaceID selects every workspace — the dashboard
// admin tool is scoped at a layer above, but the store query stays
// dumb on purpose.
func (d *DB) listWorkerIdentities(
	ctx context.Context, workspaceID string,
) ([]workerIdentity, error) {
	q := `SELECT id, name, workspace_id FROM workers`
	args := []any{}
	if workspaceID != "" {
		q += ` WHERE workspace_id = ?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY name ASC`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list workers for cost agg: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []workerIdentity
	for rows.Next() {
		var w workerIdentity
		if err := rows.Scan(&w.id, &w.name, &w.workspaceID); err != nil {
			return nil, fmt.Errorf("scan worker id: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// queryWorkerDailyCosts returns {worker_id → {YYYY-MM-DD → cost}} and
// {worker_id → run_count} aggregating worker_runs rows whose
// started_at falls on or after windowStart. The DATE() expression
// truncates the ISO timestamp to a UTC day key matching what
// buildDailySeries emits.
func (d *DB) queryWorkerDailyCosts(
	ctx context.Context, workers []workerIdentity, windowStart time.Time,
) (map[string]map[string]float64, map[string]int, error) {
	ids := workerIDList(workers)
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimSuffix(placeholders, ",")
	q := fmt.Sprintf(`
		SELECT worker_id,
		       SUBSTR(started_at, 1, 10) AS day,
		       SUM(cost_usd) AS cost,
		       COUNT(*) AS runs
		FROM worker_runs
		WHERE worker_id IN (%s) AND started_at >= ?
		GROUP BY worker_id, day`, placeholders)
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, formatTime(windowStart))
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("worker daily costs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	daily := make(map[string]map[string]float64, len(workers))
	runCount := make(map[string]int, len(workers))
	for rows.Next() {
		var (
			workerID string
			day      string
			cost     sql.NullFloat64
			runs     int
		)
		if err := rows.Scan(&workerID, &day, &cost, &runs); err != nil {
			return nil, nil, fmt.Errorf("scan worker daily: %w", err)
		}
		if _, ok := daily[workerID]; !ok {
			daily[workerID] = make(map[string]float64)
		}
		if cost.Valid {
			daily[workerID][day] = cost.Float64
		}
		runCount[workerID] += runs
	}
	return daily, runCount, rows.Err()
}

// queryWorkerMTDCosts returns {worker_id → MTD cost} for the workers
// in the given slice. MTD = sum since the first of the current
// calendar month in UTC.
func (d *DB) queryWorkerMTDCosts(
	ctx context.Context, workers []workerIdentity, monthStart time.Time,
) (map[string]float64, error) {
	ids := workerIDList(workers)
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimSuffix(placeholders, ",")
	q := fmt.Sprintf(`
		SELECT worker_id, COALESCE(SUM(cost_usd), 0)
		FROM worker_runs
		WHERE worker_id IN (%s) AND started_at >= ?
		GROUP BY worker_id`, placeholders)
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, formatTime(monthStart))
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("worker mtd costs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]float64, len(workers))
	for rows.Next() {
		var (
			id  string
			sum sql.NullFloat64
		)
		if err := rows.Scan(&id, &sum); err != nil {
			return nil, fmt.Errorf("scan worker mtd: %w", err)
		}
		if sum.Valid {
			out[id] = sum.Float64
		}
	}
	return out, rows.Err()
}

// workerIDList projects the slim worker rows to a string-id slice. The
// repeated SQL IN-list is fine for the size of this list (~50 workers
// per workspace is the upper bound in practice).
func workerIDList(workers []workerIdentity) []string {
	out := make([]string, 0, len(workers))
	for _, w := range workers {
		out = append(out, w.id)
	}
	return out
}

// buildDailySeries returns a contiguous days-length slice from
// windowStart (inclusive). Missing days collapse to $0 so the
// sparkline renders evenly.
func buildDailySeries(
	windowStart time.Time, days int, byDay map[string]float64,
) []store.WorkerCostDailyPoint {
	out := make([]store.WorkerCostDailyPoint, 0, days)
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.UTC().Format("2006-01-02")
		out = append(out, store.WorkerCostDailyPoint{
			Date:    key,
			CostUSD: byDay[key],
		})
	}
	return out
}
