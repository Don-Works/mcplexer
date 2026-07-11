// Package sqlite — usage.go implements the bounded usage-ledger projection
// consumed by the AI subscription dashboard.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

var _ store.UsageSnapshotCache = (*DB)(nil)

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

// GetUsageSnapshot returns a durable last-known dashboard snapshot.
func (d *DB) GetUsageSnapshot(
	ctx context.Context, key string,
) (store.UsageSnapshot, bool, error) {
	var payload []byte
	err := d.q.QueryRowContext(ctx, `
		SELECT snapshot_json
		FROM usage_snapshot_cache
		WHERE cache_key = ?`, key).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return store.UsageSnapshot{}, false, nil
		}
		return store.UsageSnapshot{}, false, fmt.Errorf("get usage snapshot: %w", err)
	}
	var snapshot store.UsageSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return store.UsageSnapshot{}, false, fmt.Errorf("decode usage snapshot: %w", err)
	}
	return snapshot, true, nil
}

// PutUsageSnapshot upserts the durable last-known dashboard snapshot.
func (d *DB) PutUsageSnapshot(
	ctx context.Context, key string, snapshot store.UsageSnapshot,
) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode usage snapshot: %w", err)
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO usage_snapshot_cache (
			cache_key, window_days, snapshot_json, generated_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			window_days = excluded.window_days,
			snapshot_json = excluded.snapshot_json,
			generated_at = excluded.generated_at,
			updated_at = excluded.updated_at`,
		key, snapshot.WindowDays, payload,
		formatTime(snapshot.GeneratedAt.UTC()), formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("put usage snapshot: %w", err)
	}
	return nil
}
