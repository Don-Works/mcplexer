-- M0-A — 3-axis allowlist for guard decisions.
--
-- Each row matches against (surface × pattern × directory × ai_session_id)
-- and returns one of allow/deny/prompt. Empty directory or empty
-- ai_session_id means "any" for that axis — that's the wildcard form.
-- Lower priority wins (100 is the default; rules can be promoted by
-- saving them with a smaller number).
--
-- expires_at NULL means "never expires". hit_count + last_hit_at let the UI
-- show "this rule has matched 42 times, last 3m ago" so users can prune
-- their allowlists. created_by is either a session_id (auto-suggested rule
-- accepted from the prompt) or the literal string 'user' for hand-authored
-- rules.

CREATE TABLE IF NOT EXISTS approval_rules (
    id             TEXT PRIMARY KEY,
    surface        TEXT NOT NULL,
    pattern        TEXT NOT NULL,
    directory      TEXT NOT NULL DEFAULT '',
    ai_session_id  TEXT NOT NULL DEFAULT '',
    decision       TEXT NOT NULL,
    priority       INTEGER NOT NULL DEFAULT 100,
    expires_at     DATETIME NULL,
    hit_count      INTEGER NOT NULL DEFAULT 0,
    last_hit_at    DATETIME NULL,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Hot-path index for the matcher: list every rule for a surface ordered
-- by priority (lower = wins).
CREATE INDEX IF NOT EXISTS idx_approval_rules_surface_prio
    ON approval_rules(surface, priority);
