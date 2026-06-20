-- 115 — Code-mode key/value scratch state.
--
-- Agent-facing kv__ tools use this table to persist arbitrary JSON values
-- across mcpx__execute_code calls (each call runs in a fresh goja VM, so
-- in-memory JS state does not survive). Workspace-scoped, TTL'd scratch — not
-- a durable store. The surface is mediated by Go code; agents never receive a
-- raw handle to the live mcplexer database.

CREATE TABLE code_state (
    workspace_id      TEXT NOT NULL,
    key               TEXT NOT NULL,
    value_json        TEXT NOT NULL,
    bytes             INTEGER NOT NULL DEFAULT 0,
    pinned            INTEGER NOT NULL DEFAULT 0,
    ttl_expires_at    TEXT,
    source_session_id TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (workspace_id, key)
);

CREATE INDEX idx_code_state_workspace
    ON code_state(workspace_id, updated_at DESC);

CREATE INDEX idx_code_state_ttl
    ON code_state(ttl_expires_at)
    WHERE ttl_expires_at IS NOT NULL;
