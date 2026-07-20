// monitoring_expected_signal.go — sqlite CRUD for expected-signal (absence)
// rules, migration 145. Evaluation itself lives in
// monitoring_expected_signal_record.go; judgement lives in the pure
// store.EvaluateExpectedSignal. This file only moves rows.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

const expectedSignalCols = `id, workspace_id, source_id, name,
    match_substring, min_severity, min_count, window_seconds, severity,
    timezone, active_days_mask, active_start_minute, active_end_minute,
    require_source_liveness, max_consecutive_failures, enabled,
    last_evaluated_at, last_signal_at, last_outcome, last_raised_at,
    last_recovered_at, active_incident_id, created_at, updated_at`

// CreateMonitoringExpectedSignal inserts a rule after defaults + validation.
func (d *DB) CreateMonitoringExpectedSignal(ctx context.Context, r *store.MonitoringExpectedSignal) error {
	if r == nil {
		return errors.New("CreateMonitoringExpectedSignal: rule required")
	}
	if r.WorkspaceID == "" {
		return errors.New("CreateMonitoringExpectedSignal: workspace_id required")
	}
	store.ApplyExpectedSignalDefaults(r)
	if err := store.ValidateMonitoringExpectedSignal(r); err != nil {
		return err
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO monitoring_expected_signals (`+expectedSignalCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkspaceID, r.SourceID, r.Name,
		r.MatchSubstring, r.MinSeverity, r.MinCount, r.WindowSeconds, r.Severity,
		r.Timezone, r.ActiveDaysMask, r.ActiveStartMinute, r.ActiveEndMinute,
		boolToInt(r.RequireSourceLiveness), r.MaxConsecutiveFailures, boolToInt(r.Enabled),
		nullableTime(r.LastEvaluatedAt), nullableTime(r.LastSignalAt), r.LastOutcome,
		nullableTime(r.LastRaisedAt), nullableTime(r.LastRecoveredAt),
		nullString(r.ActiveIncidentID), formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
	)
	return mapConstraintError(err)
}

// GetMonitoringExpectedSignal returns one rule or ErrMonitoringExpectedSignalNotFound.
func (d *DB) GetMonitoringExpectedSignal(ctx context.Context, id string) (*store.MonitoringExpectedSignal, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+expectedSignalCols+` FROM monitoring_expected_signals WHERE id = ?`, id)
	r, err := scanExpectedSignal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMonitoringExpectedSignalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get monitoring expected signal: %w", err)
	}
	return r, nil
}

// ListMonitoringExpectedSignals returns every rule in a workspace by name.
func (d *DB) ListMonitoringExpectedSignals(
	ctx context.Context, workspaceID string,
) ([]*store.MonitoringExpectedSignal, error) {
	return d.queryExpectedSignals(ctx, `SELECT `+expectedSignalCols+`
		FROM monitoring_expected_signals WHERE workspace_id = ?
		ORDER BY name ASC`, workspaceID)
}

// ListEnabledMonitoringExpectedSignals spans all workspaces and skips rules
// whose source is switched off at the source level only when the source row is
// gone; a disabled source is still evaluated so the collection-health outcome
// can report it (silently skipping is how monitoring goes quiet).
func (d *DB) ListEnabledMonitoringExpectedSignals(
	ctx context.Context,
) ([]*store.MonitoringExpectedSignal, error) {
	return d.queryExpectedSignals(ctx, `SELECT `+expectedSignalCols+`
		FROM monitoring_expected_signals WHERE enabled = 1
		ORDER BY workspace_id ASC, source_id ASC, name ASC`)
}

func (d *DB) queryExpectedSignals(
	ctx context.Context, query string, args ...any,
) ([]*store.MonitoringExpectedSignal, error) {
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list monitoring expected signals: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringExpectedSignal{}
	for rows.Next() {
		r, err := scanExpectedSignal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring expected signal: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateMonitoringExpectedSignal persists the configurable fields. Evaluation
// state (last_*, active_incident_id) is deliberately NOT written here so an
// operator editing a rule cannot rewind its bootstrap latch and cause a
// spurious absence alert on the next tick.
func (d *DB) UpdateMonitoringExpectedSignal(ctx context.Context, r *store.MonitoringExpectedSignal) error {
	if r == nil || r.ID == "" {
		return errors.New("UpdateMonitoringExpectedSignal: id required")
	}
	store.ApplyExpectedSignalDefaults(r)
	if err := store.ValidateMonitoringExpectedSignal(r); err != nil {
		return err
	}
	r.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE monitoring_expected_signals SET source_id = ?, name = ?,
			match_substring = ?, min_severity = ?, min_count = ?,
			window_seconds = ?, severity = ?, timezone = ?,
			active_days_mask = ?, active_start_minute = ?, active_end_minute = ?,
			require_source_liveness = ?, max_consecutive_failures = ?,
			enabled = ?, updated_at = ?
		WHERE id = ?`,
		r.SourceID, r.Name, r.MatchSubstring, r.MinSeverity, r.MinCount,
		r.WindowSeconds, r.Severity, r.Timezone,
		r.ActiveDaysMask, r.ActiveStartMinute, r.ActiveEndMinute,
		boolToInt(r.RequireSourceLiveness), r.MaxConsecutiveFailures,
		boolToInt(r.Enabled), formatTime(r.UpdatedAt), r.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return requireRowAffected(res, store.ErrMonitoringExpectedSignalNotFound)
}

// DeleteMonitoringExpectedSignal hard-deletes a rule. Any incident it raised
// survives: the incident ledger is the operator's history, not the rule's.
func (d *DB) DeleteMonitoringExpectedSignal(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM monitoring_expected_signals WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete monitoring expected signal: %w", err)
	}
	return requireRowAffected(res, store.ErrMonitoringExpectedSignalNotFound)
}

func scanExpectedSignal(row interface{ Scan(...any) error }) (*store.MonitoringExpectedSignal, error) {
	var r store.MonitoringExpectedSignal
	var liveness, enabled int
	var lastEvaluated, lastSignal, lastRaised, lastRecovered, incidentID sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.SourceID, &r.Name,
		&r.MatchSubstring, &r.MinSeverity, &r.MinCount, &r.WindowSeconds, &r.Severity,
		&r.Timezone, &r.ActiveDaysMask, &r.ActiveStartMinute, &r.ActiveEndMinute,
		&liveness, &r.MaxConsecutiveFailures, &enabled,
		&lastEvaluated, &lastSignal, &r.LastOutcome, &lastRaised,
		&lastRecovered, &incidentID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	r.RequireSourceLiveness = liveness != 0
	r.Enabled = enabled != 0
	r.LastEvaluatedAt = nullTimePtr(lastEvaluated)
	r.LastSignalAt = nullTimePtr(lastSignal)
	r.LastRaisedAt = nullTimePtr(lastRaised)
	r.LastRecoveredAt = nullTimePtr(lastRecovered)
	if incidentID.Valid {
		r.ActiveIncidentID = incidentID.String
	}
	r.CreatedAt, r.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return &r, nil
}

func nullTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	return &t
}
