-- 076 — Memory entity links (associative / "aboutness" axis).
--
-- The memory subsystem (migration 058) already has STRUCTURAL scope:
-- workspace_id, user_id, worker_id, run_id, source_peer_id, origin_peer_id.
-- Scope answers "where is this memory visible / who can recall it".
--
-- Entities answer a different question: "what is this memory ABOUT".
-- Human memory is object-contextual — you remember things about a person,
-- a place, a task, an organisation, a tool. A single memory can be about
-- multiple entities simultaneously, so this is a many-to-many join, not
-- additional columns on memories.
--
-- Entity vocabulary is freeform (tasks establish status vocab the same
-- way). The reserved kinds we ship with:
--   task | person | place | peer | agent | org | skill |
--   artifact | event | workspace
-- ID shapes by kind:
--   task        ULID
--   peer        libp2p peerID
--   agent       <peer>:<agent_name> (matches task assignee format)
--   person      email when known, else "@handle"
--   org         slug
--   skill       skill slug
--   workspace   workspace ULID
--   artifact    URL or "gh:owner/repo#N" / "gh:owner/repo@sha"
--   place       absolute path or slug
--   event       ULID generated at link time
-- IDs are stored lower-cased on the way in (write path) so recall is
-- case-insensitive without index hacks.
--
-- ROLE distinguishes how the memory relates to the entity:
--   subject       — this memory is fundamentally about this entity (default)
--   mentioned     — this memory references the entity in passing
--   derived_from  — this memory was extracted from this entity (e.g. an email)
-- Recall defaults to subject + mentioned; derived_from is consolidator-
-- specific so it does not pollute associative queries.
--
-- CROSS-PEER IDENTITY: some kinds are globally identifiable (task, peer,
-- agent, artifact-URL, person-email, org, skill, workspace) — they sync
-- verbatim across the mesh. Others are peer-local (place=abs-path,
-- person-handle, event) — those links are stripped on outgoing mesh
-- payloads to avoid fabricating a "place:/Users/example/foo" link on a
-- different machine. The strip rule lives in code (peer_local_kinds map
-- in internal/memory/entities.go), not the DB — kinds may evolve and we
-- want to ship a code patch, not a migration, when reclassifying.

CREATE TABLE memory_entities (
    id              TEXT PRIMARY KEY,         -- ulid
    memory_id       TEXT NOT NULL,
    entity_kind     TEXT NOT NULL,
    entity_id       TEXT NOT NULL,            -- normalised lower-case
    role            TEXT NOT NULL DEFAULT 'subject',
    created_at      INTEGER NOT NULL,         -- unix seconds
    created_by      TEXT,                     -- source_session_id of the writer
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

-- Dedup: a memory may only carry one row per (entity_kind, entity_id, role).
-- Re-saving the same link is a no-op.
CREATE UNIQUE INDEX uniq_memory_entities_link
    ON memory_entities(memory_id, entity_kind, entity_id, role);

-- Recall by entity: "every memory about task:T" — the hot read path.
-- Composite of (kind, id) so the planner short-circuits per kind first.
CREATE INDEX idx_memory_entities_lookup
    ON memory_entities(entity_kind, entity_id, memory_id);

-- Cascade-friendly: looking up all links for one memory.
CREATE INDEX idx_memory_entities_memory
    ON memory_entities(memory_id);
