ALTER TABLE route_rules ADD COLUMN requires_approval INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_rules ADD COLUMN approval_timeout INTEGER NOT NULL DEFAULT 0;

CREATE TABLE tool_approvals (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'pending',
    request_session_id TEXT NOT NULL,
    request_client_type TEXT NOT NULL DEFAULT '',
    request_model TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL,
    arguments TEXT NOT NULL DEFAULT '{}',
    justification TEXT NOT NULL DEFAULT '',
    route_rule_id TEXT NOT NULL DEFAULT '',
    downstream_server_id TEXT NOT NULL DEFAULT '',
    auth_scope_id TEXT NOT NULL DEFAULT '',
    approver_session_id TEXT NOT NULL DEFAULT '',
    approver_type TEXT NOT NULL DEFAULT '',
    resolution TEXT NOT NULL DEFAULT '',
    timeout_sec INTEGER NOT NULL DEFAULT 300,
    created_at TEXT NOT NULL,
    resolved_at TEXT
);
CREATE INDEX idx_tool_approvals_status ON tool_approvals(status);
