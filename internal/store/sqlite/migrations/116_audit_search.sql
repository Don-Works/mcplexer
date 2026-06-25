-- 116 — Audit search + saved searches.
--
-- Mirrors the memory FTS5 substrate (migration 058) onto audit_records so
-- the audit log gains keyword/BM25 full-text search and a deterministic
-- TF-IDF semantic ranker (built in Go over the FTS candidate pool — no
-- network, no embedding model required). The optional vector tier reuses
-- the memory embedder at query time and lives in the API layer.
--
-- Two artifacts:
--   1. audit_records_fts — FTS5 vtable + ai/au/ad triggers + backfill.
--      Indexes the free-text columns most useful for search. workspace_id
--      and id ride along UNINDEXED so the search query can scope + join
--      back to the source row without a second lookup table.
--   2. audit_saved_searches — persisted alert definitions evaluated on a
--      ticker. When the row count over a search's window crosses its
--      threshold, the evaluator fires a notification and stamps
--      last_fired_at (debounce floor).

-- FTS5 mirror — keyword/BM25 substrate over the searchable audit columns.
-- COALESCE in the triggers keeps NULLs out of the index (FTS5 treats NULL
-- as absent, but COALESCE to '' keeps the column count stable + explicit).
CREATE VIRTUAL TABLE audit_records_fts USING fts5(
    tool_name,
    error_message,
    params_redacted,
    subpath,
    workspace_name,
    client_type,
    model,
    correlation_id,
    actor_id,
    denial_reason,
    workspace_id UNINDEXED,
    id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER audit_records_ai AFTER INSERT ON audit_records BEGIN
    INSERT INTO audit_records_fts(
        rowid, tool_name, error_message, params_redacted, subpath,
        workspace_name, client_type, model, correlation_id, actor_id,
        denial_reason, workspace_id, id)
    VALUES (
        new.rowid,
        COALESCE(new.tool_name, ''),
        COALESCE(new.error_message, ''),
        COALESCE(new.params_redacted, ''),
        COALESCE(new.subpath, ''),
        COALESCE(new.workspace_name, ''),
        COALESCE(new.client_type, ''),
        COALESCE(new.model, ''),
        COALESCE(new.correlation_id, ''),
        COALESCE(new.actor_id, ''),
        COALESCE(new.denial_reason, ''),
        COALESCE(new.workspace_id, ''),
        new.id
    );
END;

CREATE TRIGGER audit_records_au AFTER UPDATE ON audit_records BEGIN
    DELETE FROM audit_records_fts WHERE rowid = old.rowid;
    INSERT INTO audit_records_fts(
        rowid, tool_name, error_message, params_redacted, subpath,
        workspace_name, client_type, model, correlation_id, actor_id,
        denial_reason, workspace_id, id)
    VALUES (
        new.rowid,
        COALESCE(new.tool_name, ''),
        COALESCE(new.error_message, ''),
        COALESCE(new.params_redacted, ''),
        COALESCE(new.subpath, ''),
        COALESCE(new.workspace_name, ''),
        COALESCE(new.client_type, ''),
        COALESCE(new.model, ''),
        COALESCE(new.correlation_id, ''),
        COALESCE(new.actor_id, ''),
        COALESCE(new.denial_reason, ''),
        COALESCE(new.workspace_id, ''),
        new.id
    );
END;

CREATE TRIGGER audit_records_ad AFTER DELETE ON audit_records BEGIN
    DELETE FROM audit_records_fts WHERE rowid = old.rowid;
END;

-- Backfill the index from every existing audit row. COALESCE mirrors the
-- triggers so a re-run produces identical content.
INSERT INTO audit_records_fts(
    rowid, tool_name, error_message, params_redacted, subpath,
    workspace_name, client_type, model, correlation_id, actor_id,
    denial_reason, workspace_id, id)
SELECT
    rowid,
    COALESCE(tool_name, ''),
    COALESCE(error_message, ''),
    COALESCE(params_redacted, ''),
    COALESCE(subpath, ''),
    COALESCE(workspace_name, ''),
    COALESCE(client_type, ''),
    COALESCE(model, ''),
    COALESCE(correlation_id, ''),
    COALESCE(actor_id, ''),
    COALESCE(denial_reason, ''),
    COALESCE(workspace_id, ''),
    id
FROM audit_records;

-- Saved searches — persisted alert definitions. filter_json holds a JSON
-- object of store.AuditFilter fields (the deep-link filter). The evaluator
-- counts matches over [now-window_sec, now] and fires when count >=
-- threshold_count, stamping last_fired_at as a debounce floor.
CREATE TABLE audit_saved_searches (
    id              TEXT PRIMARY KEY,           -- ulid/uuid
    name            TEXT NOT NULL,
    q               TEXT NOT NULL DEFAULT '',   -- free-text FTS query
    filter_json     TEXT NOT NULL DEFAULT '{}', -- AuditFilter subset as JSON
    threshold_count INTEGER NOT NULL DEFAULT 1,
    window_sec      INTEGER NOT NULL DEFAULT 3600,
    workspace_id    TEXT NOT NULL DEFAULT '',   -- '' = all workspaces
    enabled         INTEGER NOT NULL DEFAULT 1,
    last_fired_at   TEXT,                        -- RFC3339; NULL = never fired
    created_at      TEXT NOT NULL
);

CREATE INDEX idx_audit_saved_searches_enabled
    ON audit_saved_searches(enabled, workspace_id);
