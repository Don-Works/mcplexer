// monitoring_ratespike.go — sqlite backing for the distiller's
// rate-spike detector (migration 135): the two-window aggregate over
// log_lines + log_templates, and the per-source hysteresis latch that
// stops a chronic spike from re-notifying every ingest.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// CountErrorLinesInWindows aggregates error/critical-class log_lines
// for one source into a current window (ts >= currentSince) and its
// trailing baseline (ts in [baselineSince, currentSince)) in a single
// scan over the (source_id, ts) index.
func (d *DB) CountErrorLinesInWindows(ctx context.Context, sourceID string, baselineSince, currentSince time.Time) (current int64, baseline int64, err error) {
	cur := formatTime(currentSince.UTC())
	row := d.q.QueryRowContext(ctx, `
		SELECT
			SUM(CASE WHEN l.ts >= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN l.ts < ? THEN 1 ELSE 0 END)
		FROM log_lines l
		JOIN log_templates t ON t.id = l.template_id
		WHERE l.source_id = ? AND l.ts >= ? AND t.severity IN ('error', 'critical')`,
		cur, cur, sourceID, formatTime(baselineSince.UTC()))
	var curCount, baseCount sql.NullInt64
	if err := row.Scan(&curCount, &baseCount); err != nil {
		return 0, 0, fmt.Errorf("count error lines in windows: %w", err)
	}
	return curCount.Int64, baseCount.Int64, nil
}

// GetLogSourceErrorSpikeActive reads the rate-spike hysteresis latch.
func (d *DB) GetLogSourceErrorSpikeActive(ctx context.Context, sourceID string) (bool, error) {
	var active int
	err := d.q.QueryRowContext(ctx,
		`SELECT error_spike_active FROM log_sources WHERE id = ?`, sourceID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, store.ErrLogSourceNotFound
	}
	if err != nil {
		return false, fmt.Errorf("get log source error spike active: %w", err)
	}
	return active != 0, nil
}

// SetLogSourceErrorSpikeActive persists the rate-spike hysteresis latch.
func (d *DB) SetLogSourceErrorSpikeActive(ctx context.Context, sourceID string, active bool) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE log_sources SET error_spike_active = ?, updated_at = ?
		WHERE id = ?`, boolToInt(active), formatTime(time.Now().UTC()), sourceID)
	if err != nil {
		return fmt.Errorf("set log source error spike active: %w", err)
	}
	return requireRowAffected(res, store.ErrLogSourceNotFound)
}
