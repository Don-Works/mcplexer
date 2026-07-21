package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (d *DB) UpsertGoogleChatSpace(ctx context.Context, s *store.GoogleChatSpace) error {
	active := 0
	if s.Active {
		active = 1
	}
	var lastSeen any
	if !s.LastSeenAt.IsZero() {
		lastSeen = formatTime(s.LastSeenAt)
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO googlechat_spaces
			(id, space_name, title, space_type, workspace_id, session_id,
			 min_priority, listen_mode, active, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(space_name) DO UPDATE SET
			title = excluded.title,
			space_type = excluded.space_type,
			workspace_id = excluded.workspace_id,
			min_priority = excluded.min_priority,
			listen_mode = excluded.listen_mode,
			active = excluded.active,
			last_seen_at = excluded.last_seen_at`,
		s.ID, s.SpaceName, s.Title, s.SpaceType, s.WorkspaceID,
		s.SessionID, s.MinPriority, s.ListenMode, active,
		formatTime(s.CreatedAt), lastSeen,
	)
	return err
}

func (d *DB) GetGoogleChatSpace(ctx context.Context, id string) (*store.GoogleChatSpace, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, space_name, title, space_type, workspace_id, session_id,
			   min_priority, listen_mode, active, created_at, last_seen_at
		FROM googlechat_spaces WHERE id = ?`, id)
	return scanGoogleChatSpace(row)
}

func (d *DB) GetGoogleChatSpaceByName(ctx context.Context, spaceName string) (*store.GoogleChatSpace, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, space_name, title, space_type, workspace_id, session_id,
			   min_priority, listen_mode, active, created_at, last_seen_at
		FROM googlechat_spaces WHERE space_name = ?`, spaceName)
	return scanGoogleChatSpace(row)
}

func (d *DB) ListGoogleChatSpaces(ctx context.Context) ([]store.GoogleChatSpace, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, space_name, title, space_type, workspace_id, session_id,
			   min_priority, listen_mode, active, created_at, last_seen_at
		FROM googlechat_spaces
		ORDER BY COALESCE(last_seen_at, created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanGoogleChatSpaceRows(rows)
}

func (d *DB) ListActiveGoogleChatSpacesByWorkspace(ctx context.Context, workspaceID string) ([]store.GoogleChatSpace, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, space_name, title, space_type, workspace_id, session_id,
			   min_priority, listen_mode, active, created_at, last_seen_at
		FROM googlechat_spaces
		WHERE workspace_id = ? AND active = 1
		ORDER BY COALESCE(last_seen_at, created_at) DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanGoogleChatSpaceRows(rows)
}

func (d *DB) UpdateGoogleChatSpaceMinPriority(ctx context.Context, id, minPriority string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE googlechat_spaces SET min_priority = ? WHERE id = ?`,
		minPriority, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) UpdateGoogleChatSpaceListenMode(ctx context.Context, id, listenMode string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE googlechat_spaces SET listen_mode = ? WHERE id = ?`,
		listenMode, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) DeactivateGoogleChatSpace(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE googlechat_spaces SET active = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) TouchGoogleChatSpace(ctx context.Context, id string) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE googlechat_spaces SET last_seen_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), id)
	return err
}

func (d *DB) CreateGoogleChatPairing(ctx context.Context, p *store.GoogleChatPairing) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO googlechat_pairings
			(code, workspace_id, created_by_session_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		p.Code, p.WorkspaceID, p.CreatedBySessionID,
		formatTime(p.ExpiresAt), formatTime(p.CreatedAt),
	)
	return mapConstraintError(err)
}

func (d *DB) GetGoogleChatPairing(ctx context.Context, code string) (*store.GoogleChatPairing, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT code, workspace_id, created_by_session_id, expires_at, created_at
		FROM googlechat_pairings WHERE code = ?`, code)
	var p store.GoogleChatPairing
	var expires, created string
	if err := row.Scan(&p.Code, &p.WorkspaceID, &p.CreatedBySessionID, &expires, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	p.ExpiresAt = parseTime(expires)
	p.CreatedAt = parseTime(created)
	return &p, nil
}

func (d *DB) DeleteGoogleChatPairing(ctx context.Context, code string) error {
	_, err := d.q.ExecContext(ctx,
		`DELETE FROM googlechat_pairings WHERE code = ?`, code)
	return err
}

func (d *DB) SweepExpiredGoogleChatPairings(ctx context.Context, now time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM googlechat_pairings WHERE expires_at < ?`,
		formatTime(now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) InsertGoogleChatSentMessage(ctx context.Context, m *store.GoogleChatSentMessage) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO googlechat_sent_messages
			(id, space_name, thread_name, native_message_id, mesh_message_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(space_name, native_message_id) DO UPDATE SET
			mesh_message_id = excluded.mesh_message_id,
			thread_name = excluded.thread_name`,
		m.ID, m.SpaceName, m.ThreadName, m.NativeMessageID,
		m.MeshMessageID, formatTime(m.CreatedAt),
	)
	return err
}

func (d *DB) GetGoogleChatSentMessage(ctx context.Context, spaceName, nativeMessageID string) (*store.GoogleChatSentMessage, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, space_name, thread_name, native_message_id, mesh_message_id, created_at
		FROM googlechat_sent_messages
		WHERE space_name = ? AND native_message_id = ?`,
		spaceName, nativeMessageID)
	var m store.GoogleChatSentMessage
	var created string
	if err := row.Scan(&m.ID, &m.SpaceName, &m.ThreadName, &m.NativeMessageID,
		&m.MeshMessageID, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	m.CreatedAt = parseTime(created)
	return &m, nil
}

func scanGoogleChatSpace(row bridgeRow) (*store.GoogleChatSpace, error) {
	var s store.GoogleChatSpace
	var active int
	var created string
	var lastSeen sql.NullString
	if err := row.Scan(
		&s.ID, &s.SpaceName, &s.Title, &s.SpaceType,
		&s.WorkspaceID, &s.SessionID, &s.MinPriority, &s.ListenMode,
		&active, &created, &lastSeen,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	s.Active = active != 0
	s.CreatedAt = parseTime(created)
	if lastSeen.Valid {
		s.LastSeenAt = parseTime(lastSeen.String)
	}
	return &s, nil
}

func scanGoogleChatSpaceRows(rows *sql.Rows) ([]store.GoogleChatSpace, error) {
	var out []store.GoogleChatSpace
	for rows.Next() {
		s, err := scanGoogleChatSpace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan googlechat space: %w", err)
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}
