-- 114 — Ephemeral data workbench scratch collections.
--
-- Agent-facing data__ tools use these tables for workspace-scoped scratch
-- datasets. The surface is mediated by Go code; agents never receive a raw
-- handle to the live mcplexer database.

CREATE TABLE data_workbench_collections (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL DEFAULT 'table',
    tags_json         TEXT NOT NULL DEFAULT '[]',
    schema_json       TEXT NOT NULL DEFAULT '{}',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    row_count         INTEGER NOT NULL DEFAULT 0,
    doc_count         INTEGER NOT NULL DEFAULT 0,
    pinned            INTEGER NOT NULL DEFAULT 0,
    ttl_expires_at    TEXT,
    source_session_id TEXT NOT NULL DEFAULT '',
    deleted_at        TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE UNIQUE INDEX uniq_data_workbench_active_name
    ON data_workbench_collections(workspace_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_data_workbench_collection_workspace
    ON data_workbench_collections(workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_data_workbench_collection_ttl
    ON data_workbench_collections(ttl_expires_at)
    WHERE deleted_at IS NULL AND ttl_expires_at IS NOT NULL;

CREATE TABLE data_workbench_items (
    id            TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL,
    ordinal       INTEGER NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'row',
    payload_json  TEXT NOT NULL,
    text          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    FOREIGN KEY(collection_id) REFERENCES data_workbench_collections(id) ON DELETE CASCADE
);

CREATE INDEX idx_data_workbench_items_collection
    ON data_workbench_items(collection_id, ordinal);

CREATE VIRTUAL TABLE data_workbench_items_fts USING fts5(
    name,
    payload,
    text,
    tags,
    workspace_id UNINDEXED,
    collection_id UNINDEXED,
    item_id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);
