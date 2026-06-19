-- 094 — CRM Person entity.
--
-- A Person was introduced here as a workspace-agnostic CRM contact record.
-- Migration 096 supersedes that with explicit workspace scoping. This initial
-- shape is kept as history for databases migrating through 094 first.
--
-- Atomic record (no bi-temporal chain like memory facts): name is the unique
-- human key (filename stem == name). Human-editable fields (email, phone,
-- company, role, tags, notes, pinned) are reconciled in place on every index
-- pass; provenance (source_*) is stamped once at create.
--
-- person_entities is the "what is this person linked to" axis — links to
-- org/deal/task/peer/agent/skill/artifact, reusing the EntityRef vocabulary
-- the memory subsystem established.

CREATE TABLE crm_person (
    id                   TEXT PRIMARY KEY,             -- ulid
    name                 TEXT NOT NULL UNIQUE,         -- unique human key (filename stem)
    email                TEXT NOT NULL DEFAULT '',
    phone                TEXT NOT NULL DEFAULT '',
    company              TEXT NOT NULL DEFAULT '',
    role                 TEXT NOT NULL DEFAULT '',     -- job title
    tags_json            TEXT NOT NULL DEFAULT '[]',
    notes                TEXT NOT NULL DEFAULT '',     -- markdown body
    -- Provenance (always populated; agent='agent' is the floor)
    source_kind          TEXT NOT NULL DEFAULT 'agent',
    source_session_id    TEXT,
    source_tool_call_id  TEXT,
    -- Lifecycle
    pinned               INTEGER NOT NULL DEFAULT 0,   -- 1 = consolidator won't auto-prune
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    deleted_at           INTEGER                       -- soft delete
);

CREATE INDEX idx_crm_person_updated
    ON crm_person(updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_crm_person_source_session
    ON crm_person(source_session_id)
    WHERE deleted_at IS NULL AND source_session_id IS NOT NULL;

-- FTS5 mirror — keyword/BM25 substrate over every text field.
CREATE VIRTUAL TABLE crm_person_fts USING fts5(
    name,
    email,
    phone,
    company,
    role,
    tags,
    notes,
    id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER crm_person_ai AFTER INSERT ON crm_person BEGIN
    INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
    VALUES (
        new.rowid,
        new.name,
        new.email,
        new.phone,
        new.company,
        new.role,
        new.tags_json,
        new.notes,
        new.id
    );
END;

CREATE TRIGGER crm_person_au AFTER UPDATE ON crm_person BEGIN
    DELETE FROM crm_person_fts WHERE rowid = old.rowid;
    INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
    VALUES (
        new.rowid,
        new.name,
        new.email,
        new.phone,
        new.company,
        new.role,
        new.tags_json,
        new.notes,
        new.id
    );
END;

CREATE TRIGGER crm_person_ad AFTER DELETE ON crm_person BEGIN
    DELETE FROM crm_person_fts WHERE rowid = old.rowid;
END;

-- Entity links — the "what is this person linked to" axis. Mirrors
-- memory_entities (migration 076): freeform kind vocabulary, lower-cased
-- ids, role defaults to 'subject'. ON DELETE CASCADE so links die with the
-- person.
CREATE TABLE person_entities (
    id              TEXT PRIMARY KEY,         -- ulid
    person_id       TEXT NOT NULL,
    entity_kind     TEXT NOT NULL,
    entity_id       TEXT NOT NULL,            -- normalised lower-case
    role            TEXT NOT NULL DEFAULT 'subject',
    created_at      INTEGER NOT NULL,         -- unix seconds
    created_by      TEXT,                     -- source_session_id of the writer
    FOREIGN KEY (person_id) REFERENCES crm_person(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX uniq_person_entities_link
    ON person_entities(person_id, entity_kind, entity_id, role);

CREATE INDEX idx_person_entities_lookup
    ON person_entities(entity_kind, entity_id, person_id);

CREATE INDEX idx_person_entities_person
    ON person_entities(person_id);
