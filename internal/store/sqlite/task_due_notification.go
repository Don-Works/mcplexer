package sqlite

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (d *DB) HasTaskDueNotification(ctx context.Context, taskID string, dueAt time.Time) (bool, error) {
	if strings.TrimSpace(taskID) == "" || dueAt.IsZero() {
		return false, errors.New("task id and due_at are required")
	}
	var exists int
	err := d.q.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM task_due_notifications
			WHERE task_id = ? AND due_at = ?
		)`, taskID, formatTime(dueAt.UTC().Truncate(time.Second))).Scan(&exists)
	return exists == 1, err
}

func (d *DB) RecordTaskDueNotification(
	ctx context.Context,
	taskID string,
	dueAt time.Time,
	messageID string,
	notifiedAt time.Time,
) error {
	if strings.TrimSpace(taskID) == "" || dueAt.IsZero() || strings.TrimSpace(messageID) == "" {
		return errors.New("task id, due_at, and message id are required")
	}
	if notifiedAt.IsZero() {
		notifiedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO task_due_notifications
			(task_id, due_at, message_id, notified_at)
		VALUES (?, ?, ?, ?)`,
		taskID, formatTime(dueAt.UTC().Truncate(time.Second)), messageID, formatTime(notifiedAt),
	)
	return err
}
