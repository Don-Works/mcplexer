-- Allow route_rules to have empty workspace_id and downstream_server_id
-- so global deny rules can exist without referencing a specific workspace or server.
CREATE TABLE IF NOT EXISTS route_rules_new (
    id                   TEXT PRIMARY KEY,
    priority             INTEGER NOT NULL DEFAULT 0,
    workspace_id         TEXT NOT NULL DEFAULT '',
    path_glob            TEXT NOT NULL DEFAULT '**',
    tool_match           TEXT NOT NULL DEFAULT '["*"]',
    downstream_server_id TEXT NOT NULL DEFAULT '',
    auth_scope_id        TEXT NOT NULL DEFAULT '',
    policy               TEXT NOT NULL DEFAULT 'allow',
    log_level            TEXT NOT NULL DEFAULT 'info',
    source               TEXT NOT NULL DEFAULT 'api',
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

INSERT INTO route_rules_new SELECT * FROM route_rules;
DROP TABLE route_rules;
ALTER TABLE route_rules_new RENAME TO route_rules;

CREATE INDEX IF NOT EXISTS idx_route_rules_workspace_priority
    ON route_rules(workspace_id, priority DESC);
