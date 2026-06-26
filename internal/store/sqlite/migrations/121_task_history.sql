CREATE TABLE IF NOT EXISTS task_history (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    action TEXT NOT NULL,
    actor_kind TEXT NOT NULL DEFAULT '',
    actor_session_id TEXT NOT NULL DEFAULT '',
    actor_peer_id TEXT NOT NULL DEFAULT '',
    actor_user_id TEXT NOT NULL DEFAULT '',
    source_kind TEXT NOT NULL DEFAULT '',
    source_session_id TEXT NOT NULL DEFAULT '',
    source_tool_call_id TEXT NOT NULL DEFAULT '',
    workspace_path TEXT NOT NULL DEFAULT '',
    origin_peer_id TEXT NOT NULL DEFAULT '',
    related_revision INTEGER,
    changed_fields_json TEXT NOT NULL DEFAULT '[]',
    note TEXT NOT NULL DEFAULT '',
    before_json TEXT,
    after_json TEXT,
    created_at INTEGER NOT NULL,
    UNIQUE(task_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_task_history_task_revision
    ON task_history(task_id, revision DESC);

CREATE INDEX IF NOT EXISTS idx_task_history_workspace_created
    ON task_history(workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_task_history_actor_created
    ON task_history(actor_kind, actor_session_id, created_at DESC);
