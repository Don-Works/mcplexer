// Package sqlite — compression.go implements the token-compression savings
// ledger (migration 126) that backs GET /api/v1/compression/stats. Kept in its
// own file so the upsert + aggregation logic is easy to review in isolation.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// compressionAggregateMaxDays caps the requested day window so a wild
// days=100000 query can't materialise an enormous per-day grid.
const compressionAggregateMaxDays = 365

// RecordCompression upserts each observation into its (workspace_id, transform,
// day) daily-rollup bucket. See the store.Store interface contract.
func (d *DB) RecordCompression(
	ctx context.Context, workspaceID string, now time.Time, obs []store.CompressionObservation,
) error {
	if len(obs) == 0 {
		return nil
	}
	day := now.UTC().Format("2006-01-02")
	ts := formatTime(now.UTC())
	for _, o := range obs {
		if o.Transform == "" {
			continue
		}
		if _, err := d.q.ExecContext(ctx, `
			INSERT INTO compression_stats
			  (workspace_id, transform, day, lossless, samples, changed, orig_bytes,
			   would_save_bytes, would_save_tokens, applied, applied_save_bytes,
			   applied_save_tokens, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id, transform, day) DO UPDATE SET
			  lossless            = excluded.lossless,
			  samples             = compression_stats.samples + 1,
			  changed             = compression_stats.changed + excluded.changed,
			  orig_bytes          = compression_stats.orig_bytes + excluded.orig_bytes,
			  would_save_bytes    = compression_stats.would_save_bytes + excluded.would_save_bytes,
			  would_save_tokens   = compression_stats.would_save_tokens + excluded.would_save_tokens,
			  applied             = compression_stats.applied + excluded.applied,
			  applied_save_bytes  = compression_stats.applied_save_bytes + excluded.applied_save_bytes,
			  applied_save_tokens = compression_stats.applied_save_tokens + excluded.applied_save_tokens,
			  updated_at          = excluded.updated_at`,
			workspaceID, o.Transform, day, boolToInt(o.Lossless),
			boolToInt(o.Changed), o.OrigBytes, o.WouldSaveBytes, o.WouldSaveTokens,
			boolToInt(o.Applied), o.AppliedSaveBytes, o.AppliedSaveTokens, ts,
		); err != nil {
			return fmt.Errorf("record compression: %w", err)
		}
	}
	return nil
}

// CompressionAggregate rolls up the ledger over the last `days` UTC days.
func (d *DB) CompressionAggregate(
	ctx context.Context, workspaceID string, days int, now time.Time,
) (store.CompressionAggregate, error) {
	if days <= 0 {
		days = 30
	}
	if days > compressionAggregateMaxDays {
		days = compressionAggregateMaxDays
	}
	nowUTC := now.UTC()
	windowStart := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(days - 1))
	startKey := windowStart.Format("2006-01-02")

	agg := store.CompressionAggregate{Days: days, ByTransform: []store.CompressionTransformAggregate{}}

	where := `day >= ?`
	args := []any{startKey}
	if workspaceID != "" {
		where += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}

	if err := d.compressionByTransform(ctx, where, args, &agg); err != nil {
		return agg, err
	}
	daily, err := d.compressionDaily(ctx, where, args)
	if err != nil {
		return agg, err
	}
	agg.Daily = make([]store.CompressionDailyPoint, 0, days)
	for i := range days {
		key := windowStart.AddDate(0, 0, i).Format("2006-01-02")
		w, a := daily[key][0], daily[key][1]
		agg.Daily = append(agg.Daily, store.CompressionDailyPoint{
			Date: key, WouldSaveTokens: w, AppliedSaveTokens: a,
		})
	}
	return agg, nil
}

func (d *DB) compressionByTransform(
	ctx context.Context, where string, args []any, agg *store.CompressionAggregate,
) error {
	rows, err := d.q.QueryContext(ctx, `
		SELECT transform, MAX(lossless), SUM(samples), SUM(changed), SUM(orig_bytes),
		       SUM(would_save_bytes), SUM(would_save_tokens), SUM(applied),
		       SUM(applied_save_bytes), SUM(applied_save_tokens)
		FROM compression_stats WHERE `+where+`
		GROUP BY transform ORDER BY transform ASC`, args...)
	if err != nil {
		return fmt.Errorf("compression aggregate: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			t        store.CompressionTransformAggregate
			lossless int
		)
		if err := rows.Scan(&t.Transform, &lossless, &t.Samples, &t.Changed, &t.OrigBytes,
			&t.WouldSaveBytes, &t.WouldSaveTokens, &t.Applied,
			&t.AppliedSaveBytes, &t.AppliedSaveTokens); err != nil {
			return fmt.Errorf("scan compression transform: %w", err)
		}
		t.Lossless = lossless != 0
		agg.ByTransform = append(agg.ByTransform, t)
		if t.Samples > agg.Samples {
			agg.Samples = t.Samples
		}
		agg.OrigBytes += t.OrigBytes
		agg.WouldSaveBytes += t.WouldSaveBytes
		agg.WouldSaveTokens += t.WouldSaveTokens
		agg.AppliedSaveBytes += t.AppliedSaveBytes
		agg.AppliedSaveTokens += t.AppliedSaveTokens
	}
	return rows.Err()
}

// compressionDaily returns {day → [wouldSaveTokens, appliedSaveTokens]}.
func (d *DB) compressionDaily(
	ctx context.Context, where string, args []any,
) (map[string][2]int64, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT day, SUM(would_save_tokens), SUM(applied_save_tokens)
		FROM compression_stats WHERE `+where+`
		GROUP BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("compression daily: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string][2]int64)
	for rows.Next() {
		var (
			day     string
			would   int64
			applied int64
		)
		if err := rows.Scan(&day, &would, &applied); err != nil {
			return nil, fmt.Errorf("scan compression daily: %w", err)
		}
		out[day] = [2]int64{would, applied}
	}
	return out, rows.Err()
}
