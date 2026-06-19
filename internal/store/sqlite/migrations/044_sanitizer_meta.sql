-- M0-A — Sanitizer Guard per-scope policy + counters.
--
-- Each row is one policy entry: global (scope='global', scope_id=''),
-- per-server (scope='server', scope_id=server_id), or per-tool
-- (scope='tool', scope_id='namespace__tool'). The Sanitizer resolves
-- effective policy by walking tool -> server -> global and taking the
-- most-specific row that exists.
--
-- The four counters track sanitizer events for surfacing in the dashboard;
-- last_event_at lets the UI show "last triggered" without an audit query.

CREATE TABLE IF NOT EXISTS sanitizer_meta (
    id                   TEXT PRIMARY KEY,
    scope                TEXT NOT NULL,
    scope_id             TEXT NOT NULL DEFAULT '',
    denylist_enabled     INTEGER NOT NULL DEFAULT 1,
    envelope_enabled     INTEGER NOT NULL DEFAULT 1,
    classifier_enabled   INTEGER NOT NULL DEFAULT 0,
    classifier_model     TEXT NOT NULL DEFAULT '',
    action_on_match      TEXT NOT NULL DEFAULT 'envelope',
    detected_count       INTEGER NOT NULL DEFAULT 0,
    redacted_count       INTEGER NOT NULL DEFAULT 0,
    blocked_count        INTEGER NOT NULL DEFAULT 0,
    last_event_at        DATETIME NULL,
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- One policy row per (scope, scope_id). Enforced by unique index so an
-- UPSERT lookup can collide deterministically.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_sanitizer_meta_scope
    ON sanitizer_meta(scope, scope_id);
