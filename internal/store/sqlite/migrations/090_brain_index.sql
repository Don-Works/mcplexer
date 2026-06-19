-- 090_brain_index.sql — MCPlexer Brain derived-index bookkeeping.
--
-- These two tables back the brain's MD<->SQLite sync engine (docs/brain.md
-- §6.3, §6.5). They are part of the LOGICAL two-database split: both are
-- "index-rebuildable" — pure functions of the on-disk Markdown tree — and
-- the brain-index DB they conceptually belong to is gitignored. M0 keeps
-- the split logical (same physical file); a physical split is optional
-- later (Appendix B decision #3).
--
-- index_files is the incremental fast-path: the watcher stats a changed
-- file, hashes it, and compares (sha, mtime, size) against the recorded
-- row to decide whether a reparse/upsert is needed. path is the natural
-- key (one row per on-disk file). entity_kind/entity_id tie the file back
-- to the row it materialised so deletes + verify can reconcile.
--
-- brain_errors surfaces validation failures (status-not-in-vocab,
-- id!=filename, malformed YAML) to the dashboard rather than silently
-- indexing a record that lies (Astro/Zod discipline — SPEC §6.5). One row
-- per (path) is the common case; the id PK lets multiple historical
-- errors coexist if the indexer chooses to retain them.
--
-- Timestamps are Unix epoch INTEGER seconds, matching the rest of the
-- schema (see task.go / task_attachment.go conventions).

CREATE TABLE IF NOT EXISTS index_files (
    path         TEXT PRIMARY KEY,   -- absolute on-disk path of the .md/.yaml file
    workspace_id TEXT,               -- owning workspace slug/id (nullable for global)
    entity_kind  TEXT,               -- task|memory|workspace|... (nullable until parsed)
    entity_id    TEXT,               -- the materialised row id (nullable until parsed)
    sha          TEXT NOT NULL,      -- hex(sha256(file bytes)) at last index
    mtime        INTEGER,            -- file mtime (unix seconds) at last index
    size         INTEGER,            -- file size in bytes at last index
    indexed_at   INTEGER             -- when this row was last written (unix seconds)
);

-- Workspace-scoped reindex + verify sweeps.
CREATE INDEX IF NOT EXISTS idx_index_files_workspace
    ON index_files(workspace_id);

-- Reconcile-by-entity (delete the file when a row is removed, and vice
-- versa) needs the reverse lookup.
CREATE INDEX IF NOT EXISTS idx_index_files_entity
    ON index_files(entity_kind, entity_id);

CREATE TABLE IF NOT EXISTS brain_errors (
    id           TEXT PRIMARY KEY,   -- ulid
    path         TEXT,               -- offending file path
    entity_kind  TEXT,               -- task|memory|workspace|...
    field        TEXT,               -- frontmatter field that failed validation
    reason       TEXT,               -- human-readable failure reason
    created_at   INTEGER             -- unix seconds
);

-- "Show me the current validation errors for this file" (the dashboard
-- clears + re-records per path on each reindex).
CREATE INDEX IF NOT EXISTS idx_brain_errors_path
    ON brain_errors(path);
