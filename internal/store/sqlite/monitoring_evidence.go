package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// GetLogTemplateHistory returns both retained-line evidence and observed days
// that persist beyond pruning. Lifetime count/first_seen remain on the
// template itself; this method never invents missing legacy days.
func (d *DB) GetLogTemplateHistory(ctx context.Context, templateID string) (*store.LogTemplateHistory, error) {
	row := d.q.QueryRowContext(ctx, `
		WITH ordered AS (
			SELECT ts,
				(julianday(ts) - julianday(LAG(ts) OVER (ORDER BY ts))) * 86400.0 AS gap_seconds
			FROM log_lines WHERE template_id = ?
		)
		SELECT COUNT(*), COUNT(DISTINCT substr(ts, 1, 10)),
			COALESCE(MIN(ts), ''), COALESCE(MAX(ts), ''),
			COALESCE(AVG(CASE WHEN gap_seconds > 0 THEN gap_seconds END), 0)
		FROM ordered`, templateID)
	var history store.LogTemplateHistory
	var first, last string
	var averageGapSeconds float64
	if err := row.Scan(&history.RetainedCount, &history.RetainedDistinctDays,
		&first, &last, &averageGapSeconds); err != nil {
		return nil, fmt.Errorf("log template history: %w", err)
	}
	if first != "" {
		history.RetainedFirstSeen = parseTime(first)
	}
	if last != "" {
		history.RetainedLastSeen = parseTime(last)
	}
	history.AverageRetainedLineGap = time.Duration(averageGapSeconds * float64(time.Second))
	if err := d.readObservedDayHistory(ctx, templateID, &history); err != nil {
		return nil, err
	}
	return &history, nil
}

func (d *DB) readObservedDayHistory(
	ctx context.Context, templateID string, history *store.LogTemplateHistory,
) error {
	row := d.q.QueryRowContext(ctx, `
		WITH ordered AS (
			SELECT observed_day,
				(julianday(observed_day) - julianday(LAG(observed_day) OVER (ORDER BY observed_day))) * 86400.0 AS gap_seconds
			FROM log_template_days
			WHERE template_id = ? AND basis = 'observed'
		)
		SELECT COUNT(*), COALESCE(MIN(observed_day), ''),
			COALESCE(MAX(observed_day), ''),
			COALESCE(AVG(CASE WHEN gap_seconds > 0 THEN gap_seconds END), 0)
		FROM ordered`, templateID)
	var first, last string
	var averageGapSeconds float64
	if err := row.Scan(&history.ObservedDistinctDays, &first, &last,
		&averageGapSeconds); err != nil {
		return fmt.Errorf("log template day history: %w", err)
	}
	if first != "" {
		history.ObservedFirstDay, _ = time.Parse("2006-01-02", first)
	}
	if last != "" {
		history.ObservedLastDay, _ = time.Parse("2006-01-02", last)
	}
	history.AverageObservedDayGap = time.Duration(averageGapSeconds * float64(time.Second))
	return nil
}

// ListLogLinesForTemplateEvidence is the bounded retained slice used for
// cardinality and replicated task samples. monitoring.raw keeps a lower cap.
func (d *DB) ListLogLinesForTemplateEvidence(
	ctx context.Context, templateID string, limit int,
) ([]*store.LogLine, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT source_id, template_id, ts, line FROM log_lines
		WHERE template_id = ? ORDER BY ts DESC LIMIT ?`, templateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list log lines for template evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectLogLines(rows)
}
