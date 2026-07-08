// monitoring_source.go — sqlite impl of the LogSource slice of
// store.MonitoringStore (migration 128). Selector validation runs on
// every write path — the collector re-validates at dial time (ADR 0007).
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

const logSourceCols = `id, workspace_id, remote_host_id, name, kind,
    selector, schedule_spec, max_pull_bytes, retention_mb, retention_days,
    severity_rules_json, enabled, cursor_ts, cursor_hash,
    consecutive_failures, created_at, updated_at`

// applyLogSourceDefaults fills zero-valued knobs with the ratified
// defaults (2m cadence, 4 MiB pulls, 50 MB / 7 days retention).
func applyLogSourceDefaults(s *store.LogSource) {
	if s.Kind == "" {
		s.Kind = store.LogSourceKindDocker
	}
	if s.ScheduleSpec == "" {
		s.ScheduleSpec = "2m"
	}
	if s.MaxPullBytes <= 0 {
		s.MaxPullBytes = 4 * 1024 * 1024
	}
	if s.RetentionMB <= 0 {
		s.RetentionMB = 50
	}
	if s.RetentionDays <= 0 {
		s.RetentionDays = 7
	}
}

// CreateLogSource inserts a new source row. ID is generated when empty.
func (d *DB) CreateLogSource(ctx context.Context, s *store.LogSource) error {
	if s == nil {
		return errors.New("CreateLogSource: source required")
	}
	if s.WorkspaceID == "" {
		return errors.New("CreateLogSource: workspace_id required")
	}
	applyLogSourceDefaults(s)
	if err := store.ValidateLogSource(s); err != nil {
		return err
	}
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO log_sources (`+logSourceCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.WorkspaceID, s.RemoteHostID, s.Name, s.Kind,
		s.Selector, s.ScheduleSpec, s.MaxPullBytes, s.RetentionMB, s.RetentionDays,
		s.SeverityRulesJSON, boolToInt(s.Enabled), nullableTime(s.CursorTS), s.CursorHash,
		s.ConsecutiveFailures, formatTime(s.CreatedAt), formatTime(s.UpdatedAt),
	)
	return mapConstraintError(err)
}

// GetLogSource returns one row by id or ErrLogSourceNotFound.
func (d *DB) GetLogSource(ctx context.Context, id string) (*store.LogSource, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+logSourceCols+` FROM log_sources WHERE id = ?`, id)
	s, err := scanLogSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrLogSourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get log source: %w", err)
	}
	return s, nil
}

// ListLogSources returns every source in a workspace ordered by name.
func (d *DB) ListLogSources(ctx context.Context, workspaceID string) ([]*store.LogSource, error) {
	return d.queryLogSources(ctx,
		`SELECT `+logSourceCols+` FROM log_sources
		 WHERE workspace_id = ? ORDER BY name ASC`, workspaceID)
}

// ListEnabledLogSources spans all workspaces — the collector's
// scheduling view.
func (d *DB) ListEnabledLogSources(ctx context.Context) ([]*store.LogSource, error) {
	return d.queryLogSources(ctx,
		`SELECT `+logSourceCols+` FROM log_sources
		 WHERE enabled = 1 ORDER BY workspace_id ASC, name ASC`)
}

func (d *DB) queryLogSources(ctx context.Context, query string, args ...any) ([]*store.LogSource, error) {
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list log sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*store.LogSource{}
	for rows.Next() {
		s, err := scanLogSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log source: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateLogSource persists a full source row (read-modify-write at the
// caller). Cursor fields are NOT touched here — use UpdateLogSourceCursor
// so a config edit can't rewind the collector.
func (d *DB) UpdateLogSource(ctx context.Context, s *store.LogSource) error {
	if s == nil || s.ID == "" {
		return errors.New("UpdateLogSource: id required")
	}
	applyLogSourceDefaults(s)
	if err := store.ValidateLogSource(s); err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE log_sources SET remote_host_id = ?, name = ?, kind = ?,
			selector = ?, schedule_spec = ?, max_pull_bytes = ?,
			retention_mb = ?, retention_days = ?, severity_rules_json = ?,
			enabled = ?, updated_at = ?
		WHERE id = ?`,
		s.RemoteHostID, s.Name, s.Kind, s.Selector, s.ScheduleSpec,
		s.MaxPullBytes, s.RetentionMB, s.RetentionDays, s.SeverityRulesJSON,
		boolToInt(s.Enabled), formatTime(s.UpdatedAt), s.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return requireRowAffected(res, store.ErrLogSourceNotFound)
}

// DeleteLogSource hard-deletes a source; templates + lines cascade.
func (d *DB) DeleteLogSource(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx, `DELETE FROM log_sources WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete log source: %w", err)
	}
	return requireRowAffected(res, store.ErrLogSourceNotFound)
}

// UpdateLogSourceCursor advances the incremental-pull cursor and resets
// the failure counter — a successful pull is the only caller.
func (d *DB) UpdateLogSourceCursor(ctx context.Context, id string, ts time.Time, hash string) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE log_sources SET cursor_ts = ?, cursor_hash = ?,
			consecutive_failures = 0, updated_at = ?
		WHERE id = ?`,
		formatTime(ts.UTC()), hash, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update log source cursor: %w", err)
	}
	return requireRowAffected(res, store.ErrLogSourceNotFound)
}

// SetLogSourceFailures records the consecutive-failure count (0 resets).
func (d *DB) SetLogSourceFailures(ctx context.Context, id string, n int) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE log_sources SET consecutive_failures = ?, updated_at = ?
		WHERE id = ?`, n, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set log source failures: %w", err)
	}
	return requireRowAffected(res, store.ErrLogSourceNotFound)
}

func scanLogSource(row interface{ Scan(...any) error }) (*store.LogSource, error) {
	var s store.LogSource
	var enabled int
	var cursorTS sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.WorkspaceID, &s.RemoteHostID, &s.Name, &s.Kind,
		&s.Selector, &s.ScheduleSpec, &s.MaxPullBytes, &s.RetentionMB, &s.RetentionDays,
		&s.SeverityRulesJSON, &enabled, &cursorTS, &s.CursorHash,
		&s.ConsecutiveFailures, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	if cursorTS.Valid && cursorTS.String != "" {
		t := parseTime(cursorTS.String)
		s.CursorTS = &t
	}
	s.CreatedAt = parseTime(createdAt)
	s.UpdatedAt = parseTime(updatedAt)
	return &s, nil
}
