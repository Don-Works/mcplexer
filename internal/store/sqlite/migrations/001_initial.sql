CREATE TABLE IF NOT EXISTS workspaces (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    root_path      TEXT NOT NULL DEFAULT '',
    tags           TEXT NOT NULL DEFAULT '[]',
    default_policy TEXT NOT NULL DEFAULT 'allow',
    source         TEXT NOT NULL DEFAULT 'api',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_providers (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL UNIQUE,
    template_id             TEXT NOT NULL DEFAULT '',
    authorize_url           TEXT NOT NULL DEFAULT '',
    token_url               TEXT NOT NULL DEFAULT '',
    client_id               TEXT NOT NULL DEFAULT '',
    encrypted_client_secret BLOB,
    scopes                  TEXT NOT NULL DEFAULT '[]',
    use_pkce                INTEGER NOT NULL DEFAULT 0,
    source                  TEXT NOT NULL DEFAULT 'api',
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_scopes (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    type              TEXT NOT NULL DEFAULT 'env',
    encrypted_data    BLOB,
    redaction_hints   TEXT NOT NULL DEFAULT '[]',
    oauth_provider_id TEXT NOT NULL DEFAULT '',
    oauth_token_data  BLOB,
    source            TEXT NOT NULL DEFAULT 'api',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS downstream_servers (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    transport          TEXT NOT NULL DEFAULT 'stdio',
    command            TEXT NOT NULL DEFAULT '',
    args               TEXT NOT NULL DEFAULT '[]',
    url                TEXT,
    tool_namespace     TEXT NOT NULL,
    capabilities_cache TEXT NOT NULL DEFAULT '{}',
    idle_timeout_sec   INTEGER NOT NULL DEFAULT 300,
    max_instances      INTEGER NOT NULL DEFAULT 1,
    restart_policy     TEXT NOT NULL DEFAULT 'on-failure',
    source             TEXT NOT NULL DEFAULT 'api',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS route_rules (
    id                   TEXT PRIMARY KEY,
    priority             INTEGER NOT NULL DEFAULT 0,
    workspace_id         TEXT NOT NULL REFERENCES workspaces(id),
    path_glob            TEXT NOT NULL DEFAULT '**',
    tool_match           TEXT NOT NULL DEFAULT '["*"]',
    downstream_server_id TEXT NOT NULL REFERENCES downstream_servers(id),
    auth_scope_id        TEXT NOT NULL DEFAULT '',
    policy               TEXT NOT NULL DEFAULT 'allow',
    log_level            TEXT NOT NULL DEFAULT 'info',
    source               TEXT NOT NULL DEFAULT 'api',
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_route_rules_workspace_priority
    ON route_rules(workspace_id, priority DESC);

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    client_type     TEXT NOT NULL DEFAULT '',
    client_pid      INTEGER,
    connected_at    TEXT NOT NULL,
    disconnected_at TEXT,
    workspace_id    TEXT,
    model_hint      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_disconnected
    ON sessions(disconnected_at);

CREATE TABLE IF NOT EXISTS audit_records (
    id                     TEXT PRIMARY KEY,
    timestamp              TEXT NOT NULL,
    session_id             TEXT NOT NULL DEFAULT '',
    client_type            TEXT NOT NULL DEFAULT '',
    model                  TEXT NOT NULL DEFAULT '',
    workspace_id           TEXT NOT NULL DEFAULT '',
    subpath                TEXT NOT NULL DEFAULT '',
    tool_name              TEXT NOT NULL DEFAULT '',
    params_redacted        TEXT NOT NULL DEFAULT '{}',
    route_rule_id          TEXT NOT NULL DEFAULT '',
    downstream_server_id   TEXT NOT NULL DEFAULT '',
    downstream_instance_id TEXT NOT NULL DEFAULT '',
    auth_scope_id          TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT '',
    error_code             TEXT NOT NULL DEFAULT '',
    error_message          TEXT NOT NULL DEFAULT '',
    latency_ms             INTEGER NOT NULL DEFAULT 0,
    response_size          INTEGER NOT NULL DEFAULT 0,
    created_at             TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_workspace_ts
    ON audit_records(workspace_id, timestamp);

CREATE INDEX IF NOT EXISTS idx_audit_tool_ts
    ON audit_records(tool_name, timestamp);

CREATE INDEX IF NOT EXISTS idx_audit_status_ts
    ON audit_records(status, timestamp);
