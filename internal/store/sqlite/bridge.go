package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (d *DB) UpsertTelegramChat(ctx context.Context, c *store.TelegramChat) error {
	active := 0
	if c.Active {
		active = 1
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO telegram_chats
			(id, platform, native_chat_id, chat_type, title, workspace_id,
			 session_id, min_priority, active, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, native_chat_id) DO UPDATE SET
			chat_type = excluded.chat_type,
			title = excluded.title,
			workspace_id = excluded.workspace_id,
			min_priority = excluded.min_priority,
			active = excluded.active,
			last_seen_at = excluded.last_seen_at`,
		c.ID, c.Platform, c.NativeChatID, c.ChatType, c.Title, c.WorkspaceID,
		c.SessionID, c.MinPriority, active,
		formatTime(c.CreatedAt), formatTime(c.LastSeenAt),
	)
	return err
}

func (d *DB) GetTelegramChat(ctx context.Context, id string) (*store.TelegramChat, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, platform, native_chat_id, chat_type, title, workspace_id,
			   session_id, min_priority, active, created_at, last_seen_at
		FROM telegram_chats WHERE id = ?`, id)
	return scanBridgeChat(row)
}

func (d *DB) GetTelegramChatByNative(ctx context.Context, platform, nativeChatID string) (*store.TelegramChat, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, platform, native_chat_id, chat_type, title, workspace_id,
			   session_id, min_priority, active, created_at, last_seen_at
		FROM telegram_chats WHERE platform = ? AND native_chat_id = ?`,
		platform, nativeChatID)
	return scanBridgeChat(row)
}

func (d *DB) ListTelegramChats(ctx context.Context) ([]store.TelegramChat, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, platform, native_chat_id, chat_type, title, workspace_id,
			   session_id, min_priority, active, created_at, last_seen_at
		FROM telegram_chats
		ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBridgeChatRows(rows)
}

func (d *DB) ListActiveTelegramChatsByWorkspace(ctx context.Context, workspaceID string) ([]store.TelegramChat, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, platform, native_chat_id, chat_type, title, workspace_id,
			   session_id, min_priority, active, created_at, last_seen_at
		FROM telegram_chats
		WHERE workspace_id = ? AND active = 1
		ORDER BY last_seen_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBridgeChatRows(rows)
}

func (d *DB) UpdateTelegramChatMinPriority(ctx context.Context, id, minPriority string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE telegram_chats SET min_priority = ? WHERE id = ?`,
		minPriority, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) DeactivateTelegramChat(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE telegram_chats SET active = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) TouchTelegramChat(ctx context.Context, id string) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE telegram_chats SET last_seen_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), id)
	return err
}

func (d *DB) CreateTelegramPairing(ctx context.Context, p *store.TelegramPairing) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO telegram_pairings
			(code, platform, workspace_id, created_by_session_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.Code, p.Platform, p.WorkspaceID, p.CreatedBySessionID,
		formatTime(p.ExpiresAt), formatTime(p.CreatedAt),
	)
	return mapConstraintError(err)
}

func (d *DB) GetTelegramPairing(ctx context.Context, code string) (*store.TelegramPairing, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT code, platform, workspace_id, created_by_session_id, expires_at, created_at
		FROM telegram_pairings WHERE code = ?`, code)
	var p store.TelegramPairing
	var expires, created string
	if err := row.Scan(&p.Code, &p.Platform, &p.WorkspaceID, &p.CreatedBySessionID, &expires, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	p.ExpiresAt = parseTime(expires)
	p.CreatedAt = parseTime(created)
	return &p, nil
}

func (d *DB) DeleteTelegramPairing(ctx context.Context, code string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM telegram_pairings WHERE code = ?`, code)
	return err
}

func (d *DB) SweepExpiredTelegramPairings(ctx context.Context, now time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM telegram_pairings WHERE expires_at < ?`,
		formatTime(now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) InsertTelegramSentMessage(ctx context.Context, m *store.TelegramSentMessage) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO telegram_sent_messages
			(id, platform, native_chat_id, native_message_id, mesh_message_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, native_chat_id, native_message_id) DO UPDATE SET
			mesh_message_id = excluded.mesh_message_id`,
		m.ID, m.Platform, m.NativeChatID, m.NativeMessageID,
		m.MeshMessageID, formatTime(m.CreatedAt),
	)
	return err
}

func (d *DB) GetTelegramSentMessage(ctx context.Context, platform, nativeChatID, nativeMessageID string) (*store.TelegramSentMessage, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, platform, native_chat_id, native_message_id, mesh_message_id, created_at
		FROM telegram_sent_messages
		WHERE platform = ? AND native_chat_id = ? AND native_message_id = ?`,
		platform, nativeChatID, nativeMessageID)
	var m store.TelegramSentMessage
	var created string
	if err := row.Scan(&m.ID, &m.Platform, &m.NativeChatID, &m.NativeMessageID,
		&m.MeshMessageID, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	m.CreatedAt = parseTime(created)
	return &m, nil
}

type bridgeRow interface {
	Scan(dest ...any) error
}

func scanBridgeChat(row bridgeRow) (*store.TelegramChat, error) {
	var c store.TelegramChat
	var active int
	var created, lastSeen string
	if err := row.Scan(
		&c.ID, &c.Platform, &c.NativeChatID, &c.ChatType, &c.Title,
		&c.WorkspaceID, &c.SessionID, &c.MinPriority, &active,
		&created, &lastSeen,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	c.Active = active != 0
	c.CreatedAt = parseTime(created)
	c.LastSeenAt = parseTime(lastSeen)
	return &c, nil
}

func scanBridgeChatRows(rows *sql.Rows) ([]store.TelegramChat, error) {
	var out []store.TelegramChat
	for rows.Next() {
		c, err := scanBridgeChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bridge chat: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
