-- 067 — Google Chat bridge.
--
-- Mirrors the telegram_chats / telegram_pairings / telegram_sent_messages
-- shape (migration 020 + 021) but for Google Chat spaces. The bridge in
-- internal/googlechat/ reads + writes through these tables; the existing
-- TelegramStore on store.Store stays untouched.
--
-- space_type: 'dm' (1:1), 'group' (named group), 'space' (Google Workspace
--             "space" — the modern term for a multi-person room).
-- listen_mode: 'mention' (default — only forward when the bot is @-mentioned
--              or replied-to) | 'all' (every message in the space gets
--              forwarded to mesh). Matches telegram's group behaviour where
--              groups default to mention-only.
-- session_id: stable per-space identifier ("googlechat-<space_name>") so the
--             mesh sees one logical conversation per Google Chat space.

CREATE TABLE IF NOT EXISTS googlechat_spaces (
    id TEXT PRIMARY KEY,
    space_name TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    space_type TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL UNIQUE,
    min_priority TEXT NOT NULL DEFAULT 'normal',
    listen_mode TEXT NOT NULL DEFAULT 'mention',
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    last_seen_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_googlechat_spaces_workspace
    ON googlechat_spaces(workspace_id, active);
CREATE INDEX IF NOT EXISTS idx_googlechat_spaces_space_name
    ON googlechat_spaces(space_name);

CREATE TABLE IF NOT EXISTS googlechat_pairings (
    code TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    created_by_session_id TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_googlechat_pairings_expires
    ON googlechat_pairings(expires_at);

CREATE TABLE IF NOT EXISTS googlechat_sent_messages (
    id TEXT PRIMARY KEY,
    space_name TEXT NOT NULL,
    thread_name TEXT NOT NULL DEFAULT '',
    native_message_id TEXT NOT NULL,
    mesh_message_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(space_name, native_message_id)
);
CREATE INDEX IF NOT EXISTS idx_googlechat_sent_mesh
    ON googlechat_sent_messages(mesh_message_id);
CREATE INDEX IF NOT EXISTS idx_googlechat_sent_lookup
    ON googlechat_sent_messages(space_name, native_message_id);
