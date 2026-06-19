package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
)

// InsertNotification persists a notification. Returns the row ID. If
// message_id duplicates an existing row, the existing ID is returned
// and no new row is created (producers can retry without dups).
func (d *DB) InsertNotification(ctx context.Context, evt notify.Event) (int64, error) {
	source := evt.Source
	if source == "" {
		// Legacy producers without Source set default to "mesh" —
		// historically the only source that fired notify events.
		source = "mesh"
	}
	res, err := d.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO notifications
			(message_id, source, agent_name, role, kind, priority, title, body, tags, link, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evt.MessageID, source, evt.AgentName, evt.Role, evt.Kind, evt.Priority,
		evt.Title, evt.Body, evt.Tags, evt.Link, formatTime(evt.CreatedAt),
	)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		return res.LastInsertId()
	}
	var existing int64
	if err := d.q.QueryRowContext(ctx, `SELECT id FROM notifications WHERE message_id = ?`, evt.MessageID).Scan(&existing); err != nil {
		return 0, err
	}
	return existing, nil
}

// ListNotifications returns notifications matching the filter,
// newest first. The Limit is hard-capped to 200 server-side to
// prevent unbounded payloads.
func (d *DB) ListNotifications(ctx context.Context, f notify.ListFilter) ([]notify.StoredEvent, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var (
		conds []string
		args  []any
	)
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Priority != "" {
		conds = append(conds, "priority = ?")
		args = append(args, f.Priority)
	}
	if f.UnreadOnly {
		conds = append(conds, "read_at IS NULL")
	}
	if f.BeforeID > 0 {
		conds = append(conds, "id < ?")
		args = append(args, f.BeforeID)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := `
		SELECT id, message_id, source, agent_name, role, kind, priority,
		       title, body, tags, link, created_at, read_at
		FROM notifications
		` + where + `
		ORDER BY id DESC
		LIMIT ?`
	args = append(args, limit)
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []notify.StoredEvent
	for rows.Next() {
		var (
			e         notify.StoredEvent
			createdAt string
			readAt    sql.NullString
		)
		if err := rows.Scan(
			&e.ID, &e.MessageID, &e.Source, &e.AgentName, &e.Role, &e.Kind, &e.Priority,
			&e.Title, &e.Body, &e.Tags, &e.Link, &createdAt, &readAt,
		); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(createdAt)
		if readAt.Valid {
			t := parseTime(readAt.String)
			e.ReadAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkNotificationsRead sets read_at = now for the given IDs. Idempotent.
func (d *DB) MarkNotificationsRead(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, formatTime(time.Now().UTC()))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `UPDATE notifications SET read_at = ? WHERE id IN (` + strings.Join(placeholders, ",") + `) AND read_at IS NULL`
	_, err := d.q.ExecContext(ctx, q, args...)
	return err
}

// MarkAllNotificationsRead sets read_at = now for every unread row.
func (d *DB) MarkAllNotificationsRead(ctx context.Context) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE read_at IS NULL`,
		formatTime(time.Now().UTC()),
	)
	return err
}

// UnreadNotificationCount returns the count of unread events.
func (d *DB) UnreadNotificationCount(ctx context.Context) (int, error) {
	var n int
	if err := d.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE read_at IS NULL`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// PruneNotifications evicts oldest-read rows first, then oldest period,
// until the table holds at most `cap` rows. Returns the number deleted.
func (d *DB) PruneNotifications(ctx context.Context, cap int) (int, error) {
	if cap <= 0 {
		return 0, nil
	}
	var total int
	if err := d.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&total); err != nil {
		return 0, err
	}
	if total <= cap {
		return 0, nil
	}
	excess := total - cap
	// Sub-query strategy: delete the `excess` rows ordered by
	// (read_at IS NOT NULL DESC, created_at ASC) — read rows go first,
	// oldest within each bucket. ROWID-based subquery is the only way
	// to LIMIT a DELETE in sqlite without recursive CTE.
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM notifications WHERE id IN (
			SELECT id FROM notifications
			ORDER BY (read_at IS NULL) ASC, created_at ASC
			LIMIT ?
		)`, excess)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
