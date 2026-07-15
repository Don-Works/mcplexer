package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PruneLogLines enforces the per-source retention caps: age first, then
// repeatedly halves the oldest rows until the byte budget is satisfied.
func (d *DB) PruneLogLines(
	ctx context.Context, sourceID string, maxAge time.Time, maxBytes int64,
) (int64, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM log_lines WHERE source_id = ? AND ts < ?`,
		sourceID, formatTime(maxAge.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune log lines by age: %w", err)
	}
	removed, _ := res.RowsAffected()
	for maxBytes > 0 {
		total, count, err := d.logLineStorage(ctx, sourceID)
		if err != nil {
			return removed, err
		}
		if total <= maxBytes || count == 0 {
			break
		}
		pruned, err := d.pruneOldestLogLines(ctx, sourceID, count)
		if err != nil {
			return removed, err
		}
		if pruned == 0 {
			return removed, fmt.Errorf("prune log lines by size made no progress")
		}
		removed += pruned
	}
	return removed, nil
}

func (d *DB) logLineStorage(ctx context.Context, sourceID string) (int64, int64, error) {
	var total sql.NullInt64
	var count int64
	err := d.q.QueryRowContext(ctx,
		`SELECT SUM(LENGTH(line)), COUNT(*) FROM log_lines WHERE source_id = ?`, sourceID,
	).Scan(&total, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("prune log lines size: %w", err)
	}
	if !total.Valid {
		return 0, count, nil
	}
	return total.Int64, count, nil
}

func (d *DB) pruneOldestLogLines(ctx context.Context, sourceID string, count int64) (int64, error) {
	limit := count / 2
	if limit < 1 {
		limit = 1
	}
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM log_lines WHERE rowid IN (
			SELECT rowid FROM log_lines WHERE source_id = ? ORDER BY ts ASC LIMIT ?
		)`, sourceID, limit)
	if err != nil {
		return 0, fmt.Errorf("prune log lines by size: %w", err)
	}
	return res.RowsAffected()
}
