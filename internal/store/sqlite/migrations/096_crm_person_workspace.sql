-- 096 — Workspace-scope CRM people.
--
-- Person records were introduced as global CRM contacts. That is too open for
-- worker-scoped brain access: a worker granted one workspace could discover
-- every contact. Move legacy rows into a real `crm` workspace, make workspace
-- membership explicit, and change the human-key uniqueness from global
-- `name` to `(workspace_id, name)`.

INSERT INTO workspaces (
    id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at
)
SELECT
    'crm', 'crm', '', NULL, '["crm"]', 'allow', 'system', datetime('now'), datetime('now')
WHERE NOT EXISTS (
    SELECT 1 FROM workspaces WHERE id = 'crm' OR name = 'crm'
);

DROP TRIGGER IF EXISTS crm_person_ai;
DROP TRIGGER IF EXISTS crm_person_au;
DROP TRIGGER IF EXISTS crm_person_ad;
DROP TABLE IF EXISTS crm_person_fts;

DROP TABLE IF EXISTS person_entities_new;
DROP TABLE IF EXISTS crm_person_new;

CREATE TABLE crm_person_new (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT NOT NULL REFERENCES workspaces(id),
    name                 TEXT NOT NULL,
    email                TEXT NOT NULL DEFAULT '',
    phone                TEXT NOT NULL DEFAULT '',
    company              TEXT NOT NULL DEFAULT '',
    role                 TEXT NOT NULL DEFAULT '',
    tags_json            TEXT NOT NULL DEFAULT '[]',
    notes                TEXT NOT NULL DEFAULT '',
    source_kind          TEXT NOT NULL DEFAULT 'agent',
    source_session_id    TEXT,
    source_tool_call_id  TEXT,
    pinned               INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    deleted_at           INTEGER,
    UNIQUE(workspace_id, name)
);

INSERT INTO crm_person_new (
    id, workspace_id, name, email, phone, company, role,
    tags_json, notes, source_kind, source_session_id, source_tool_call_id,
    pinned, created_at, updated_at, deleted_at
)
SELECT
    id,
    COALESCE(
        (SELECT id FROM workspaces WHERE name = 'crm' LIMIT 1),
        (SELECT id FROM workspaces WHERE id = 'crm' LIMIT 1),
        'crm'
    ) AS workspace_id,
    name, email, phone, company, role,
    tags_json, notes, source_kind, source_session_id, source_tool_call_id,
    pinned, created_at, updated_at, deleted_at
FROM crm_person;

CREATE TABLE person_entities_new (
    id              TEXT PRIMARY KEY,
    person_id       TEXT NOT NULL,
    entity_kind     TEXT NOT NULL,
    entity_id       TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'subject',
    created_at      INTEGER NOT NULL,
    created_by      TEXT,
    FOREIGN KEY (person_id) REFERENCES crm_person_new(id) ON DELETE CASCADE
);

INSERT INTO person_entities_new (
    id, person_id, entity_kind, entity_id, role, created_at, created_by
)
SELECT id, person_id, entity_kind, entity_id, role, created_at, created_by
FROM person_entities;

DROP TABLE person_entities;
DROP TABLE crm_person;

ALTER TABLE crm_person_new RENAME TO crm_person;
ALTER TABLE person_entities_new RENAME TO person_entities;

CREATE INDEX idx_crm_person_updated
    ON crm_person(updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_crm_person_workspace_updated
    ON crm_person(workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_crm_person_source_session
    ON crm_person(source_session_id)
    WHERE deleted_at IS NULL AND source_session_id IS NOT NULL;

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

INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
SELECT rowid, name, email, phone, company, role, tags_json, notes, id
FROM crm_person;

CREATE UNIQUE INDEX uniq_person_entities_link
    ON person_entities(person_id, entity_kind, entity_id, role);

CREATE INDEX idx_person_entities_lookup
    ON person_entities(entity_kind, entity_id, person_id);

CREATE INDEX idx_person_entities_person
    ON person_entities(person_id);
