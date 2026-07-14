-- 132 — Code index source chunks: FTS5 keyword retrieval + sqlite-vec KNN.
--
-- Extends migration 127 with per-file source chunks (symbol-scoped slices),
-- a porter+unicode61 FTS5 mirror for BM25 search, and a vec0 FLOAT[1536]
-- embedding plane keyed by chunk id. Chunk rows carry embed_model/embed_version
-- so backfill can detect stale vectors; vec rows are deleted explicitly before
-- chunk replacement because vec0 has no FK cascade.

ALTER TABLE code_index_files ADD COLUMN chunk_version INTEGER NOT NULL DEFAULT 0;

ALTER TABLE code_index_builds ADD COLUMN chunk_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE code_index_chunks (
    id             INTEGER PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    file_id        INTEGER NOT NULL,
    path           TEXT NOT NULL,
    path_tokens    TEXT NOT NULL DEFAULT '',
    ordinal        INTEGER NOT NULL,
    kind           TEXT NOT NULL DEFAULT '',
    symbol_name    TEXT NOT NULL DEFAULT '',
    symbol_tokens  TEXT NOT NULL DEFAULT '',
    code_tokens    TEXT NOT NULL DEFAULT '',
    start_line     INTEGER NOT NULL DEFAULT 0,
    end_line       INTEGER NOT NULL DEFAULT 0,
    content        TEXT NOT NULL DEFAULT '',
    content_hash   TEXT NOT NULL DEFAULT '',
    embed_model    TEXT NOT NULL DEFAULT '',
    embed_version  INTEGER NOT NULL DEFAULT 0,
    indexed_at     TIMESTAMP NOT NULL,
    UNIQUE(file_id, ordinal)
);
CREATE INDEX idx_code_index_chunks_ws ON code_index_chunks(workspace_id);
CREATE INDEX idx_code_index_chunks_file ON code_index_chunks(file_id);
CREATE INDEX idx_code_index_chunks_ws_path ON code_index_chunks(workspace_id, path);

-- Chunk FTS5 mirror — porter+unicode61 for prose + identifier tokens.
-- Column order matches bm25() weight args in SearchCodeIndexChunks:
--   symbol_tokens (10) > symbol_name (8) > path_tokens (5) > code_tokens (2) > content (1)
CREATE VIRTUAL TABLE code_index_chunks_fts USING fts5(
    symbol_tokens,
    symbol_name,
    path_tokens,
    code_tokens,
    content,
    workspace_id UNINDEXED,
    chunk_id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER code_index_chunks_ai AFTER INSERT ON code_index_chunks BEGIN
    INSERT INTO code_index_chunks_fts(
        rowid, symbol_tokens, symbol_name, path_tokens, code_tokens, content,
        workspace_id, chunk_id)
    VALUES (
        new.rowid,
        new.symbol_tokens,
        new.symbol_name,
        new.path_tokens,
        new.code_tokens,
        new.content,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_chunks_au AFTER UPDATE ON code_index_chunks BEGIN
    DELETE FROM code_index_chunks_fts WHERE rowid = old.rowid;
    INSERT INTO code_index_chunks_fts(
        rowid, symbol_tokens, symbol_name, path_tokens, code_tokens, content,
        workspace_id, chunk_id)
    VALUES (
        new.rowid,
        new.symbol_tokens,
        new.symbol_name,
        new.path_tokens,
        new.code_tokens,
        new.content,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER code_index_chunks_ad AFTER DELETE ON code_index_chunks BEGIN
    DELETE FROM code_index_chunks_fts WHERE rowid = old.rowid;
END;

-- Vector mirror — sqlite-vec vec0, 1536 dims (OpenAI text-embedding-3-small).
CREATE VIRTUAL TABLE code_index_chunks_vec USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    embedding FLOAT[1536]
);