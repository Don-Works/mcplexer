-- 102 — Recipe store for harvested tool-call patterns.
--
-- Recipes are mined from audit records: frequent successful tool-call
-- patterns (tool_name + param structure) are extracted, deduplicated,
-- and ranked by success rate, frequency, recency, and session diversity.
--
-- The table is write-mostly by the harvest loop and read-mostly by
-- mcpx__search_recipes / search surfaces. No PII or secrets ever land
-- here — params_pattern stores only the JSON key structure, not values.
-- source_audit_ids is a JSON array of audit record IDs for provenance
-- (the audit records themselves are redacted at the source).

CREATE TABLE IF NOT EXISTS recipes (
    id              TEXT PRIMARY KEY,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    tool_name       TEXT NOT NULL UNIQUE,
    namespace       TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    params_pattern  TEXT,           -- JSON: {"keys":["a","b"],"optional":["c"]}
    success_count   INTEGER NOT NULL DEFAULT 0,
    total_count     INTEGER NOT NULL DEFAULT 0,
    avg_latency_ms  REAL NOT NULL DEFAULT 0,
    error_rate      REAL NOT NULL DEFAULT 0,
    score           REAL NOT NULL DEFAULT 0,
    session_count   INTEGER NOT NULL DEFAULT 0,
    last_used_at    TEXT,
    tags            TEXT,           -- JSON array of tags
    source_audit_ids TEXT           -- JSON array of audit record IDs
);

CREATE INDEX IF NOT EXISTS idx_recipes_tool_name ON recipes(tool_name);
CREATE INDEX IF NOT EXISTS idx_recipes_score ON recipes(score DESC);
CREATE INDEX IF NOT EXISTS idx_recipes_namespace ON recipes(namespace);
CREATE INDEX IF NOT EXISTS idx_recipes_updated ON recipes(updated_at DESC);

-- FTS5 index for full-text search over tool_name, namespace, description, tags
CREATE VIRTUAL TABLE IF NOT EXISTS recipes_fts USING fts5(
    tool_name, namespace, description, tags,
    content='recipes',
    content_rowid='rowid',
    tokenize='porter unicode61'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS recipes_ai AFTER INSERT ON recipes BEGIN
    INSERT INTO recipes_fts(rowid, tool_name, namespace, description, tags)
    VALUES (new.rowid, new.tool_name, new.namespace, new.description, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS recipes_ad AFTER DELETE ON recipes BEGIN
    INSERT INTO recipes_fts(recipes_fts, rowid, tool_name, namespace, description, tags)
    VALUES ('delete', old.rowid, old.tool_name, old.namespace, old.description, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS recipes_au AFTER UPDATE ON recipes BEGIN
    INSERT INTO recipes_fts(recipes_fts, rowid, tool_name, namespace, description, tags)
    VALUES ('delete', old.rowid, old.tool_name, old.namespace, old.description, old.tags);
    INSERT INTO recipes_fts(rowid, tool_name, namespace, description, tags)
    VALUES (new.rowid, new.tool_name, new.namespace, new.description, new.tags);
END;
