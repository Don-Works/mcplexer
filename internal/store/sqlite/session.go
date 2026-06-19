package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) CreateSession(ctx context.Context, s *store.Session) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.ConnectedAt.IsZero() {
		s.ConnectedAt = time.Now().UTC()
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO sessions
			(id, client_type, client_pid, connected_at, disconnected_at,
			 workspace_id, model_hint)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ClientType, s.ClientPID,
		formatTime(s.ConnectedAt), formatTimePtr(s.DisconnectedAt),
		s.WorkspaceID, s.ModelHint,
	)
	return err
}

func (d *DB) GetSession(ctx context.Context, id string) (*store.Session, error) {
	var s store.Session
	var connectedAt string
	var disconnectedAt, workspaceID *string
	err := d.q.QueryRowContext(ctx, `
		SELECT id, client_type, client_pid, connected_at, disconnected_at,
		       workspace_id, model_hint
		FROM sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.ClientType, &s.ClientPID, &connectedAt,
		&disconnectedAt, &workspaceID, &s.ModelHint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.ConnectedAt = parseTime(connectedAt)
	s.DisconnectedAt = parseTimePtr(disconnectedAt)
	s.WorkspaceID = workspaceID
	return &s, nil
}

func (d *DB) DisconnectSession(ctx context.Context, id string) error {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE sessions SET disconnected_at = ? WHERE id = ? AND disconnected_at IS NULL`,
		now, id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) DisconnectAllSessions(ctx context.Context) (int, error) {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE sessions SET disconnected_at = ? WHERE disconnected_at IS NULL`,
		now,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (d *DB) ListActiveSessions(ctx context.Context) ([]store.Session, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, client_type, client_pid, connected_at, disconnected_at,
		       workspace_id, model_hint
		FROM sessions
		WHERE disconnected_at IS NULL
		ORDER BY connected_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Session
	for rows.Next() {
		var s store.Session
		var connectedAt string
		var disconnectedAt, workspaceID *string
		if err := rows.Scan(&s.ID, &s.ClientType, &s.ClientPID, &connectedAt,
			&disconnectedAt, &workspaceID, &s.ModelHint); err != nil {
			return nil, err
		}
		s.ConnectedAt = parseTime(connectedAt)
		s.DisconnectedAt = parseTimePtr(disconnectedAt)
		s.WorkspaceID = workspaceID
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) CleanupStaleSessions(ctx context.Context, before time.Time) (int, error) {
	now := time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE sessions
		SET disconnected_at = ?
		WHERE disconnected_at IS NULL
		  AND connected_at < ?
		  AND NOT EXISTS (
		    SELECT 1 FROM audit_records
		    WHERE audit_records.session_id = sessions.id
		      AND audit_records.timestamp > ?
		  )`,
		formatTime(now), formatTime(before), formatTime(before),
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
