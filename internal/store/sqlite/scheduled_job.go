package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// listScheduledJobCap bounds DueScheduledJobs even when limit <= 0 so a
// runaway catalog can't fan a tick into thousands of in-process commands.
const listScheduledJobCap = 500

const scheduledJobCols = `id, name, kind, spec, command, args_json, env_json, cwd,
    surface, enabled, survive_daemon_down, native_driver, native_id,
    last_run_at, next_run_at, last_status, last_error, worker_id,
    created_at, updated_at`

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateScheduledJob inserts one row. Caller supplies ID; CreatedAt and
// UpdatedAt default to now when zero.
func (d *DB) CreateScheduledJob(ctx context.Context, j *store.ScheduledJob) error {
	if j == nil || j.ID == "" {
		return errors.New("CreateScheduledJob: id required")
	}
	now := time.Now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = now
	}
	if j.ArgsJSON == "" {
		j.ArgsJSON = "[]"
	}
	if j.EnvJSON == "" {
		j.EnvJSON = "{}"
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO scheduled_jobs (`+scheduledJobCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Name, j.Kind, j.Spec, j.Command, j.ArgsJSON, j.EnvJSON, j.CWD,
		j.Surface, boolToInt(j.Enabled), boolToInt(j.SurviveDaemonDown),
		j.NativeDriver, j.NativeID,
		formatTimePtr(j.LastRunAt), formatTimePtr(j.NextRunAt),
		j.LastStatus, j.LastError, nullString(j.WorkerID),
		formatTime(j.CreatedAt), formatTime(j.UpdatedAt),
	)
	return mapConstraintError(err)
}

// GetScheduledJob returns one row or store.ErrNotFound.
func (d *DB) GetScheduledJob(ctx context.Context, id string) (*store.ScheduledJob, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+scheduledJobCols+` FROM scheduled_jobs WHERE id = ?`, id)
	j, err := scanScheduledJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get scheduled_job: %w", err)
	}
	return j, nil
}

// ListScheduledJobs returns every row ordered by created_at descending.
func (d *DB) ListScheduledJobs(ctx context.Context) ([]store.ScheduledJob, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+scheduledJobCols+` FROM scheduled_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list scheduled_jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.ScheduledJob
	for rows.Next() {
		j, err := scanScheduledJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scheduled_job: %w", err)
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// UpdateScheduledJob writes every column for j and bumps updated_at = now.
func (d *DB) UpdateScheduledJob(ctx context.Context, j *store.ScheduledJob) error {
	if j == nil || j.ID == "" {
		return errors.New("UpdateScheduledJob: id required")
	}
	j.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE scheduled_jobs
		SET name = ?, kind = ?, spec = ?, command = ?, args_json = ?, env_json = ?,
		    cwd = ?, surface = ?, enabled = ?, survive_daemon_down = ?,
		    native_driver = ?, native_id = ?,
		    last_run_at = ?, next_run_at = ?, last_status = ?, last_error = ?,
		    worker_id = ?, updated_at = ?
		WHERE id = ?`,
		j.Name, j.Kind, j.Spec, j.Command, j.ArgsJSON, j.EnvJSON,
		j.CWD, j.Surface, boolToInt(j.Enabled), boolToInt(j.SurviveDaemonDown),
		j.NativeDriver, j.NativeID,
		formatTimePtr(j.LastRunAt), formatTimePtr(j.NextRunAt),
		j.LastStatus, j.LastError, nullString(j.WorkerID),
		formatTime(j.UpdatedAt), j.ID,
	)
	if err != nil {
		return fmt.Errorf("update scheduled_job: %w", err)
	}
	return checkRowsAffected(res)
}

// DeleteScheduledJob removes one row by ID. Returns ErrNotFound if absent.
func (d *DB) DeleteScheduledJob(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM scheduled_jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete scheduled_job: %w", err)
	}
	return checkRowsAffected(res)
}

// DueScheduledJobs returns enabled jobs whose next_run_at <= now.
// Ordered next_run_at ASC. limit <= 0 = use internal cap.
func (d *DB) DueScheduledJobs(
	ctx context.Context, now time.Time, limit int,
) ([]store.ScheduledJob, error) {
	if limit <= 0 || limit > listScheduledJobCap {
		limit = listScheduledJobCap
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+scheduledJobCols+`
		FROM scheduled_jobs
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at ASC
		LIMIT ?`,
		formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("due scheduled_jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.ScheduledJob
	for rows.Next() {
		j, err := scanScheduledJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scheduled_job: %w", err)
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func scanScheduledJob(r scanner) (*store.ScheduledJob, error) {
	var (
		j                    store.ScheduledJob
		enabled, survive     int
		lastRun, nextRun     sql.NullString
		workerID             sql.NullString
		createdAt, updatedAt string
	)
	err := r.Scan(
		&j.ID, &j.Name, &j.Kind, &j.Spec, &j.Command, &j.ArgsJSON, &j.EnvJSON,
		&j.CWD, &j.Surface, &enabled, &survive, &j.NativeDriver, &j.NativeID,
		&lastRun, &nextRun, &j.LastStatus, &j.LastError, &workerID,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	j.Enabled = enabled != 0
	j.SurviveDaemonDown = survive != 0
	if lastRun.Valid {
		t := parseTime(lastRun.String)
		j.LastRunAt = &t
	}
	if nextRun.Valid {
		t := parseTime(nextRun.String)
		j.NextRunAt = &t
	}
	if workerID.Valid {
		j.WorkerID = workerID.String
	}
	j.CreatedAt = parseTime(createdAt)
	j.UpdatedAt = parseTime(updatedAt)
	return &j, nil
}
