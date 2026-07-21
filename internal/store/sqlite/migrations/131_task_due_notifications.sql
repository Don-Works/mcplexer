-- 131 — Durable receipts for human task due notifications.
--
-- The Signal tray is intentionally capped and pruned, so its notification rows
-- cannot also be the long-lived dedupe ledger. One receipt per task/due-time
-- prevents an overdue task from producing a push every minute or after restart.

CREATE TABLE IF NOT EXISTS task_due_notifications (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    due_at DATETIME NOT NULL,
    message_id TEXT NOT NULL,
    notified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (task_id, due_at)
);

CREATE INDEX IF NOT EXISTS task_due_notifications_notified_idx
    ON task_due_notifications(notified_at DESC);
