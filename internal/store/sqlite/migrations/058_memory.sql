-- 058 — Memory subsystem.
--
-- The memory layer is the cross-harness, cross-machine, cross-team
-- knowledge store. Surfaces:
--   - Agents over MCP (memory__save / __recall / __search / __forget)
--   - The dashboard ("Memory" pages)
--   - Workers (worker.memory_scope_id pin set in M0.1 already)
--   - Peers via the new libp2p protocol /mcplexer/memory/1.0.0
--
-- Two-tier model in one table:
--   - kind='fact': atomic key/value, one ACTIVE row per (scope, name).
--                  Updates invalidate (t_valid_end stamped) and insert
--                  a new active row. Bi-temporal trail preserved.
--   - kind='note': longer markdown blob, no uniqueness — duplicates OK.
--
-- Scoping mirrors skill_registry + worker_templates: workspace_id NULL
-- = global, else pinned. Additional optional pins: worker_id, run_id,
-- user_id — narrow the visibility further.
--
-- Provenance on every row so a poisoned session can be forensically
-- purged with one DELETE WHERE source_session_id = ?.
--
-- Embedding: pointer columns (embed_model, embed_version) live on the
-- main row; the actual vector lives in memories_vec (vec0 virtual
-- table). embed_model NULL = no vector yet, FTS5 still works.

CREATE TABLE memories (
    id                   TEXT PRIMARY KEY,             -- ulid
    name                 TEXT NOT NULL,                -- short title/key
    kind                 TEXT NOT NULL DEFAULT 'note', -- fact|note
    content              TEXT NOT NULL,                -- markdown body / fact value
    tags_json            TEXT NOT NULL DEFAULT '[]',
    metadata_json        TEXT NOT NULL DEFAULT '{}',
    -- Scoping (NULL = unscoped on that axis)
    workspace_id         TEXT,                         -- pin to workspace; NULL = global
    user_id              TEXT,
    worker_id            TEXT,
    run_id               TEXT,
    -- Provenance (always populated; agent='unknown' is the floor)
    source_kind          TEXT NOT NULL DEFAULT 'agent',
    source_session_id    TEXT,
    source_peer_id       TEXT,
    source_tool_call_id  TEXT,
    origin_peer_id       TEXT,                         -- libp2p peer that wrote this; NULL = local
    -- Embedding pointer (vector itself is in memories_vec)
    embed_model          TEXT,
    embed_version        INTEGER NOT NULL DEFAULT 0,
    -- Bi-temporal validity (Zep/Graphiti style: invalidate, don't delete)
    t_valid_start        INTEGER NOT NULL,             -- unix seconds
    t_valid_end          INTEGER,                      -- NULL = still valid
    invalidated_by       TEXT,                         -- id of memory that superseded this
    -- Lifecycle
    pinned               INTEGER NOT NULL DEFAULT 0,   -- 1 = consolidator won't auto-prune
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    deleted_at           INTEGER                       -- soft delete
);

CREATE INDEX idx_memories_workspace
    ON memories(workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_kind
    ON memories(kind, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_worker
    ON memories(worker_id, updated_at DESC)
    WHERE deleted_at IS NULL AND worker_id IS NOT NULL;

CREATE INDEX idx_memories_source_session
    ON memories(source_session_id)
    WHERE deleted_at IS NULL AND source_session_id IS NOT NULL;

CREATE INDEX idx_memories_origin_peer
    ON memories(origin_peer_id, updated_at DESC)
    WHERE deleted_at IS NULL AND origin_peer_id IS NOT NULL;

-- For 'fact' kind: enforce one ACTIVE (t_valid_end IS NULL) fact per
-- (workspace, worker, name). Updates use INVALIDATE_THEN_INSERT so this
-- always holds.
CREATE UNIQUE INDEX uniq_memory_fact_scoped
    ON memories(
        COALESCE(workspace_id, ''),
        COALESCE(worker_id, ''),
        name
    )
    WHERE kind = 'fact' AND deleted_at IS NULL AND t_valid_end IS NULL;

-- FTS5 mirror — keyword/BM25 substrate. Always populated.
CREATE VIRTUAL TABLE memories_fts USING fts5(
    name,
    content,
    tags,
    workspace_id UNINDEXED,
    id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, name, content, tags, workspace_id, id)
    VALUES (
        new.rowid,
        new.name,
        new.content,
        new.tags_json,
        COALESCE(new.workspace_id, ''),
        new.id
    );
END;

CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    DELETE FROM memories_fts WHERE rowid = old.rowid;
    INSERT INTO memories_fts(rowid, name, content, tags, workspace_id, id)
    VALUES (
        new.rowid,
        new.name,
        new.content,
        new.tags_json,
        COALESCE(new.workspace_id, ''),
        new.id
    );
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    DELETE FROM memories_fts WHERE rowid = old.rowid;
END;

-- Vector mirror — sqlite-vec vec0 virtual table. 1536 dims targets
-- OpenAI text-embedding-3-small (the recommended opt-in provider).
-- Populated only when an embedding provider is configured; absent
-- vectors are fine because FTS5 carries the recall floor.
CREATE VIRTUAL TABLE memories_vec USING vec0(
    memory_id TEXT PRIMARY KEY,
    embedding FLOAT[1536]
);

-- Incoming offers from paired peers via the /mcplexer/memory/1.0.0
-- libp2p protocol. A peer can OFFER a memory by sending a thin descriptor
-- here; the local user (or an admin tool) decides whether to REQUEST the
-- full content, at which point the row gets accepted_as_id pointing at
-- the inserted memories.id.
CREATE TABLE memory_offers (
    id              TEXT PRIMARY KEY,        -- ulid
    peer_id         TEXT NOT NULL,           -- libp2p peer id of the offerer
    peer_name       TEXT NOT NULL DEFAULT '',-- display name at offer-time (best effort)
    remote_id       TEXT NOT NULL,           -- the offerer's memories.id
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL DEFAULT 'note',
    description     TEXT NOT NULL DEFAULT '',
    preview         TEXT NOT NULL DEFAULT '',-- first ~512 chars, for preview
    tags_json       TEXT NOT NULL DEFAULT '[]',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    embed_model     TEXT,                    -- offerer's embedding model (must match to import vector)
    received_at     INTEGER NOT NULL,
    accepted_at     INTEGER,                 -- NULL = not yet accepted
    declined_at     INTEGER,                 -- NULL = not declined
    accepted_as_id  TEXT,                    -- local memories.id once imported
    UNIQUE (peer_id, remote_id)
);

CREATE INDEX idx_memory_offers_pending
    ON memory_offers(received_at DESC)
    WHERE accepted_at IS NULL AND declined_at IS NULL;

CREATE INDEX idx_memory_offers_peer
    ON memory_offers(peer_id, received_at DESC);
