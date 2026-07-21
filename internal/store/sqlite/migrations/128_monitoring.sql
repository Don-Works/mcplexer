-- 128_monitoring.sql — Monitoring / remote log intelligence (M1).
--
-- Remote hosts are SSH targets the collector pulls docker logs from
-- (read-only by construction — see docs/adr/0007). Log sources are one
-- stream on a host. Monitoring channels are workspace-scoped alert
-- outputs (config_json holds secret:// refs only, never plaintext).
-- log_templates + log_lines are the distiller's template store and the
-- bounded raw ring buffer (populated from M3 on; schema ships now so
-- the data model is settled in one migration).

CREATE TABLE IF NOT EXISTS remote_hosts (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    ssh_user      TEXT NOT NULL,
    ssh_host      TEXT NOT NULL,
    ssh_port      INTEGER NOT NULL DEFAULT 22,
    auth_scope_id TEXT NOT NULL,
    host_key_pin  TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_remote_hosts_workspace_name
    ON remote_hosts(workspace_id, name);

CREATE TABLE IF NOT EXISTS log_sources (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    remote_host_id       TEXT NOT NULL REFERENCES remote_hosts(id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    kind                 TEXT NOT NULL DEFAULT 'docker',
    selector             TEXT NOT NULL,
    schedule_spec        TEXT NOT NULL DEFAULT '2m',
    max_pull_bytes       INTEGER NOT NULL DEFAULT 4194304,
    retention_mb         INTEGER NOT NULL DEFAULT 50,
    retention_days       INTEGER NOT NULL DEFAULT 7,
    severity_rules_json  TEXT NOT NULL DEFAULT '',
    enabled              INTEGER NOT NULL DEFAULT 1,
    cursor_ts            DATETIME NULL,
    cursor_hash          TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_log_sources_workspace_name
    ON log_sources(workspace_id, name);
CREATE INDEX IF NOT EXISTS idx_log_sources_host
    ON log_sources(remote_host_id);

CREATE TABLE IF NOT EXISTS monitoring_channels (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    config_json  TEXT NOT NULL DEFAULT '{}',
    min_severity TEXT NOT NULL DEFAULT 'error',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_monitoring_channels_workspace_name
    ON monitoring_channels(workspace_id, name);

CREATE TABLE IF NOT EXISTS log_templates (
    id           TEXT PRIMARY KEY,
    source_id    TEXT NOT NULL REFERENCES log_sources(id) ON DELETE CASCADE,
    masked       TEXT NOT NULL,
    severity     TEXT NOT NULL DEFAULT 'info',
    count        INTEGER NOT NULL DEFAULT 0,
    window_count INTEGER NOT NULL DEFAULT 0,
    first_seen   DATETIME NOT NULL,
    last_seen    DATETIME NOT NULL,
    sample_first TEXT NOT NULL DEFAULT '',
    sample_last  TEXT NOT NULL DEFAULT '',
    acked        INTEGER NOT NULL DEFAULT 0,
    ack_note     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_log_templates_source_seen
    ON log_templates(source_id, last_seen);

CREATE TABLE IF NOT EXISTS log_lines (
    source_id   TEXT NOT NULL,
    template_id TEXT NOT NULL,
    ts          DATETIME NOT NULL,
    line        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_log_lines_source_ts
    ON log_lines(source_id, ts);
