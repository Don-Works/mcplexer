// monitoring_channel.go — sqlite impl of the MonitoringChannel slice of
// store.MonitoringStore (migration 128). The secrets rule (config_json
// carries secret:// refs, never plaintext credentials) is enforced on
// every write via store.ValidateMonitoringChannel.
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

const monitoringChannelCols = `id, workspace_id, name, kind, config_json,
    min_severity, enabled, created_at, updated_at,
    consecutive_failures, first_failure_at, last_failure_at,
    last_error, last_success_at,
    targeted_since_success, last_targeted_at`

// monitoringChannelInsertCols is the write set for CreateMonitoringChannel. It
// stops short of the health columns on purpose: those are owned by the
// dispatcher (RecordMonitoringChannelFailure/Success) and a new row starts at
// the schema defaults — zero failures, never delivered, which HealthState
// reports as "unknown" rather than flattering an unproven route as healthy.
const monitoringChannelInsertCols = `id, workspace_id, name, kind, config_json,
    min_severity, enabled, created_at, updated_at`

// CreateMonitoringChannel inserts a new channel row. ID is generated
// when empty; MinSeverity defaults to "high", ConfigJSON to "{}".
func (d *DB) CreateMonitoringChannel(ctx context.Context, c *store.MonitoringChannel) error {
	if c == nil {
		return errors.New("CreateMonitoringChannel: channel required")
	}
	if c.WorkspaceID == "" {
		return errors.New("CreateMonitoringChannel: workspace_id required")
	}
	if c.Name == "" {
		return errors.New("CreateMonitoringChannel: name required")
	}
	applyChannelDefaults(c)
	if err := store.ValidateMonitoringChannel(c); err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO monitoring_channels (`+monitoringChannelInsertCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Kind, c.ConfigJSON,
		c.MinSeverity, boolToInt(c.Enabled),
		formatTime(c.CreatedAt), formatTime(c.UpdatedAt),
	)
	return mapConstraintError(err)
}

func applyChannelDefaults(c *store.MonitoringChannel) {
	if c.MinSeverity == "" {
		c.MinSeverity = store.SeverityError
	}
	if c.ConfigJSON == "" {
		c.ConfigJSON = "{}"
	}
}

// GetMonitoringChannel returns one row by id or ErrMonitoringChannelNotFound.
func (d *DB) GetMonitoringChannel(ctx context.Context, id string) (*store.MonitoringChannel, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+monitoringChannelCols+` FROM monitoring_channels WHERE id = ?`, id)
	c, err := scanMonitoringChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMonitoringChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get monitoring channel: %w", err)
	}
	return c, nil
}

// ListMonitoringChannels returns every channel in a workspace ordered
// by name (disabled rows included so the UI can render toggles).
func (d *DB) ListMonitoringChannels(ctx context.Context, workspaceID string) ([]*store.MonitoringChannel, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+monitoringChannelCols+` FROM monitoring_channels
		WHERE workspace_id = ? ORDER BY name ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list monitoring channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*store.MonitoringChannel{}
	for rows.Next() {
		c, err := scanMonitoringChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateMonitoringChannel persists a full channel row.
func (d *DB) UpdateMonitoringChannel(ctx context.Context, c *store.MonitoringChannel) error {
	if c == nil || c.ID == "" {
		return errors.New("UpdateMonitoringChannel: id required")
	}
	applyChannelDefaults(c)
	if err := store.ValidateMonitoringChannel(c); err != nil {
		return err
	}
	c.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE monitoring_channels SET name = ?, kind = ?, config_json = ?,
			min_severity = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		c.Name, c.Kind, c.ConfigJSON, c.MinSeverity,
		boolToInt(c.Enabled), formatTime(c.UpdatedAt), c.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return requireRowAffected(res, store.ErrMonitoringChannelNotFound)
}

// DeleteMonitoringChannel hard-deletes a channel row.
func (d *DB) DeleteMonitoringChannel(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx, `DELETE FROM monitoring_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete monitoring channel: %w", err)
	}
	return requireRowAffected(res, store.ErrMonitoringChannelNotFound)
}

func scanMonitoringChannel(row interface{ Scan(...any) error }) (*store.MonitoringChannel, error) {
	var c store.MonitoringChannel
	var enabled int
	var createdAt, updatedAt string
	var firstFailure, lastFailure, lastSuccess, lastTargeted sql.NullString
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Kind, &c.ConfigJSON,
		&c.MinSeverity, &enabled, &createdAt, &updatedAt,
		&c.ConsecutiveFailures, &firstFailure, &lastFailure,
		&c.LastError, &lastSuccess,
		&c.TargetedSinceSuccess, &lastTargeted)
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	c.CreatedAt = parseTime(createdAt)
	c.UpdatedAt = parseTime(updatedAt)
	c.FirstFailureAt = nullTime(firstFailure)
	c.LastFailureAt = nullTime(lastFailure)
	c.LastSuccessAt = nullTime(lastSuccess)
	c.LastTargetedAt = nullTime(lastTargeted)
	return &c, nil
}

// nullTime converts a nullable DATETIME column into an optional timestamp.
// An empty-but-non-NULL string is treated as absent: rows written before the
// column existed can carry ” rather than NULL, and a zero-value time.Time
// rendered as "0001-01-01" reads as a real (ancient) delivery in every surface.
func nullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}
