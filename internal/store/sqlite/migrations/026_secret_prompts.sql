-- secret_prompts: human-in-the-loop secret injection.
--
-- The agent calls the secret__prompt MCP tool. mcplexer creates a row here in
-- pending status, fires a UI/native notification, and blocks the agent's RPC.
-- When the user submits a value, mcplexer writes it to a 0600-perm file under
-- {data_dir}/secrets/ephemeral/<unguessable random id> owned by the daemon and
-- returns the path to the agent (the secret value never appears in any tool
-- result, audit row, or broadcast).
--
-- file_path is NEVER exposed via SSE / audit. Status transitions:
--   pending -> submitted | cancelled | timeout
--
-- delete_on_read = 1 means the manager hard-deletes the file on the first
-- successful read (kqueue on macOS, inotify on Linux).

CREATE TABLE secret_prompts (
    id              TEXT PRIMARY KEY,
    reason          TEXT NOT NULL,
    label           TEXT NOT NULL,
    requester       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending',
    file_path       TEXT,
    expires_at      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    completed_at    TEXT,
    delete_on_read  INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_secret_prompts_pending
    ON secret_prompts(status, expires_at)
    WHERE status = 'pending';
