// Package sqlite — usage.go implements the bounded usage-ledger projection
// consumed by the AI subscription dashboard.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ListUsageLedgerRuns returns the bounded time-window projection used by the
// subscription dashboard. The query deliberately includes failed runs: they
// still represent provider requests, while accounting-missing is only marked
// for successful rows by the aggregation layer.
func (d *DB) ListUsageLedgerRuns(
	ctx context.Context, since time.Time,
) ([]store.UsageLedgerRun, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT started_at, model_provider, model_id, billing_model,
		       subscription_bucket, real_cost_usd, cost_usd,
		       input_tokens, output_tokens, status
		FROM worker_runs
		WHERE started_at >= ?
		ORDER BY started_at DESC`, formatTime(since.UTC()))
	if err != nil {
		return nil, fmt.Errorf("list usage ledger runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]store.UsageLedgerRun, 0)
	for rows.Next() {
		var row store.UsageLedgerRun
		var startedAt string
		if err := rows.Scan(
			&startedAt, &row.ModelProvider, &row.ModelID, &row.BillingModel,
			&row.SubscriptionBucket, &row.RealCostUSD, &row.CostUSD,
			&row.InputTokens, &row.OutputTokens, &row.Status,
		); err != nil {
			return nil, fmt.Errorf("scan usage ledger run: %w", err)
		}
		row.StartedAt = parseTime(startedAt)
		out = append(out, row)
	}
	return out, rows.Err()
}
