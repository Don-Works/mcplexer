-- 127 — Local codebase index (index__* namespace).
--
-- Backs the builtin `index` tools (build/status/symbols/deps/tests_for/
-- summary/recent_changes/map_failure/context). A workspace's repo is
-- enumerated (git ls-files, .gitignore-honoring) and its Go + TS/JS files
-- have symbols + import edges extracted into a searchable map.
--
-- Four base tables + two FTS5 mirrors:
--   - code_index_builds  — one row per workspace: freshness + counters.
--   - code_index_files   — one row per indexed file (root-relative path).
--   - code_index_symbols — funcs/methods/types/consts/classes/components.
--   - code_index_edges   — file-level import graph (NOT a call graph).
--   - code_index_symbols_fts / code_index_files_fts — keyword/BM25 substrate,
--     kept in sync by ai/au/ad triggers with rowid linkage exactly as
--     058_memory.sql / 116_audit_search.sql.
--
-- Tokenizer note: the symbols FTS uses unicode61 WITHOUT porter — porter
-- stemming mangles identifiers (HandleKVSet must stay searchable by its
-- camelCase-split name_tokens). The files FTS keeps porter because
-- doc_summary is natural-language prose.
--
-- No FK constraints are declared: UpsertCodeIndexedFiles deletes child
-- symbols/edges by the stable file_id before reinsert, and the ad triggers
-- clean the FTS mirrors. (This DB does set PRAGMA foreign_keys = ON, but the
-- explicit per-file child delete is kept for clarity and batch semantics.)

CREATE TABLE code_index_builds (
    workspace_id  TEXT PRIMARY KEY,
    root_path     TEXT NOT NULL,
    git_head      TEXT NOT NULL DEFAULT '',
    dirty_count   INTEGER NOT NULL DEFAULT 0, -- git status --porcelain count at build
    built_at      TIMESTAMP NOT NULL,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    file_count    INTEGER NOT NULL DEFAULT 0,
    symbol_count  INTEGER NOT NULL DEFAULT 0,
    warnings_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE code_index_files (
    id             INTEGER PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    path           TEXT NOT NULL,            -- root-relative, forward slashes
    path_tokens    TEXT NOT NULL DEFAULT '', -- splitIdent(path), for FTS
    language       TEXT NOT NULL DEFAULT '',
    package        TEXT NOT NULL DEFAULT '', -- Go package / TS module dir
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    line_count     INTEGER NOT NULL DEFAULT 0,
    mtime_unix     INTEGER NOT NULL DEFAULT 0,
    content_hash   TEXT NOT NULL DEFAULT '',
    doc_summary    TEXT NOT NULL DEFAULT '',
    is_test        INTEGER NOT NULL DEFAULT 0,
    skipped_reason TEXT NOT NULL DEFAULT '',
    indexed_at     TIMESTAMP NOT NULL,
    UNIQUE(workspace_id, path)
);
CREATE INDEX idx_code_index_files_ws ON code_index_files(workspace_id);

CREATE TABLE code_index_symbols (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id      INTEGER NOT NULL,
    name         TEXT NOT NULL,
    name_tokens  TEXT NOT NULL DEFAULT '',  -- splitIdent(name), for FTS
    kind         TEXT NOT NULL,             -- func|method|type|const|var|class|interface|enum|component|export
    receiver     TEXT NOT NULL DEFAULT '',
    signature    TEXT NOT NULL DEFAULT '',
    doc          TEXT NOT NULL DEFAULT '',
    start_line   INTEGER NOT NULL,
    end_line     INTEGER NOT NULL DEFAULT 0,
    exported     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_code_index_symbols_file ON code_index_symbols(file_id);
CREATE INDEX idx_code_index_symbols_ws_name ON code_index_symbols(workspace_id, name);

CREATE TABLE code_index_edges (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_file_id INTEGER NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'import',
    to_path      TEXT NOT NULL DEFAULT '',  -- resolved root-relative file (TS) or package dir (Go); '' if external
    to_module    TEXT NOT NULL DEFAULT ''   -- raw specifier when external/unresolved
);
CREATE INDEX idx_code_index_edges_from ON code_index_edges(from_file_id);
CREATE INDEX idx_code_index_edges_ws_to ON code_index_edges(workspace_id, to_path);

-- Symbol FTS5 mirror — unicode61 WITHOUT porter (identifiers, not prose).
CREATE VIRTUAL TABLE code_index_symbols_fts USING fts5(
    name,
    name_tokens,
    signature,
    doc,
    workspace_id UNINDEXED,
    symbol_id UNINDEXED,
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER code_index_symbols_ai AFTER INSERT ON code_index_symbols BEGIN
    INSERT INTO code_index_symbols_fts(
        rowid, name, name_tokens, signature, doc, workspace_id, symbol_id)
    VALUES (
        new.rowid,
        new.name,
        new.name_tokens,
        new.signature,
        new.doc,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_symbols_au AFTER UPDATE ON code_index_symbols BEGIN
    DELETE FROM code_index_symbols_fts WHERE rowid = old.rowid;
    INSERT INTO code_index_symbols_fts(
        rowid, name, name_tokens, signature, doc, workspace_id, symbol_id)
    VALUES (
        new.rowid,
        new.name,
        new.name_tokens,
        new.signature,
        new.doc,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_symbols_ad AFTER DELETE ON code_index_symbols BEGIN
    DELETE FROM code_index_symbols_fts WHERE rowid = old.rowid;
END;

-- File FTS5 mirror — porter (doc_summary is natural-language prose).
CREATE VIRTUAL TABLE code_index_files_fts USING fts5(
    path,
    path_tokens,
    package,
    doc_summary,
    workspace_id UNINDEXED,
    file_id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER code_index_files_ai AFTER INSERT ON code_index_files BEGIN
    INSERT INTO code_index_files_fts(
        rowid, path, path_tokens, package, doc_summary, workspace_id, file_id)
    VALUES (
        new.rowid,
        new.path,
        new.path_tokens,
        new.package,
        new.doc_summary,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_files_au AFTER UPDATE ON code_index_files BEGIN
    DELETE FROM code_index_files_fts WHERE rowid = old.rowid;
    INSERT INTO code_index_files_fts(
        rowid, path, path_tokens, package, doc_summary, workspace_id, file_id)
    VALUES (
        new.rowid,
        new.path,
        new.path_tokens,
        new.package,
        new.doc_summary,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_files_ad AFTER DELETE ON code_index_files BEGIN
    DELETE FROM code_index_files_fts WHERE rowid = old.rowid;
END;
