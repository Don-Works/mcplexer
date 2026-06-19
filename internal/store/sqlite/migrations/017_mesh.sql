CREATE TABLE mesh_messages (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'event',
    priority TEXT NOT NULL DEFAULT 'normal',
    content TEXT NOT NULL,
    audience TEXT NOT NULL DEFAULT '*',
    tags TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    thread_root TEXT NOT NULL DEFAULT '',
    reply_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'live',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_mesh_msg_ws_status ON mesh_messages(workspace_id, status, id);
CREATE INDEX idx_mesh_msg_expires ON mesh_messages(status, expires_at);
CREATE INDEX idx_mesh_msg_thread ON mesh_messages(thread_root) WHERE thread_root != '';

CREATE TABLE mesh_agents (
    session_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    client_type TEXT NOT NULL DEFAULT '',
    model_hint TEXT NOT NULL DEFAULT '',
    cursor TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_mesh_agents_ws ON mesh_agents(workspace_id);
