-- Earlier pre-release builds created bridge_* tables. This migration renames
-- them to telegram_* (the final naming) while being safe for fresh installs
-- where bridge_* never existed.

-- Ensure both source and destination exist first (no-op if already there).
CREATE TABLE IF NOT EXISTS bridge_chats (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    native_chat_id TEXT NOT NULL,
    chat_type TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL UNIQUE,
    min_priority TEXT NOT NULL DEFAULT 'normal',
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    UNIQUE(platform, native_chat_id)
);
CREATE TABLE IF NOT EXISTS bridge_pairings (
    code TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    created_by_session_id TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bridge_sent_messages (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    native_chat_id TEXT NOT NULL,
    native_message_id TEXT NOT NULL,
    mesh_message_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(platform, native_chat_id, native_message_id)
);

CREATE TABLE IF NOT EXISTS telegram_chats (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    native_chat_id TEXT NOT NULL,
    chat_type TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL UNIQUE,
    min_priority TEXT NOT NULL DEFAULT 'normal',
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    UNIQUE(platform, native_chat_id)
);
CREATE TABLE IF NOT EXISTS telegram_pairings (
    code TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    created_by_session_id TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS telegram_sent_messages (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    native_chat_id TEXT NOT NULL,
    native_message_id TEXT NOT NULL,
    mesh_message_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(platform, native_chat_id, native_message_id)
);

INSERT OR IGNORE INTO telegram_chats SELECT * FROM bridge_chats;
INSERT OR IGNORE INTO telegram_pairings SELECT * FROM bridge_pairings;
INSERT OR IGNORE INTO telegram_sent_messages SELECT * FROM bridge_sent_messages;

DROP TABLE bridge_sent_messages;
DROP TABLE bridge_pairings;
DROP TABLE bridge_chats;

DROP INDEX IF EXISTS idx_bridge_chats_ws_active;
DROP INDEX IF EXISTS idx_bridge_chats_platform_native;
DROP INDEX IF EXISTS idx_bridge_pairings_expires;
DROP INDEX IF EXISTS idx_bridge_sent_mesh;
DROP INDEX IF EXISTS idx_bridge_sent_lookup;

CREATE INDEX IF NOT EXISTS idx_telegram_chats_ws_active ON telegram_chats(workspace_id, active);
CREATE INDEX IF NOT EXISTS idx_telegram_chats_platform_native ON telegram_chats(platform, native_chat_id);
CREATE INDEX IF NOT EXISTS idx_telegram_pairings_expires ON telegram_pairings(expires_at);
CREATE INDEX IF NOT EXISTS idx_telegram_sent_mesh ON telegram_sent_messages(mesh_message_id);
CREATE INDEX IF NOT EXISTS idx_telegram_sent_lookup ON telegram_sent_messages(platform, native_chat_id, native_message_id);
