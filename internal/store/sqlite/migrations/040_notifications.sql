-- Persist user-facing notifications so the Signal tray survives reloads
-- and supports backfill on open. Producers still write via notify.Bus;
-- the bus now durably stores before fanning out via SSE.
--
-- read_at NULL == unread. Frontend's read-state lookup is the hot path
-- (sidebar counter on every poll), so it gets a dedicated partial index.
--
-- Retention: bounded by row count via background pruner (oldest-read
-- first, then oldest period). No per-row TTL — the user's call when
-- they hit the cap.

-- Migration 040 creates the table without `source`; 041 adds the
-- `source` column so existing installs that hit 040 before the Signal
-- redesign upgrade cleanly. Together they describe the final shape.

CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    priority TEXT NOT NULL DEFAULT 'normal',
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',
    link TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at DATETIME NULL,
    UNIQUE(message_id)
);

CREATE INDEX IF NOT EXISTS notifications_created_at_idx ON notifications(created_at DESC);
CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications(created_at DESC) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS notifications_kind_idx ON notifications(kind);
