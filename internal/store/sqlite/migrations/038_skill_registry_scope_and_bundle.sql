-- 038_skill_registry_scope_and_bundle.sql — extends 037 with two
-- orthogonal additions:
--
-- 1) workspace_id (NULLable). NULL = global skill, visible everywhere;
--    a non-null workspace_id pins the skill to one workspace, mirroring
--    the route_rules model. Search/list scope to (current workspace ∪
--    global). At publish time the agent picks scope explicitly.
--
-- 2) source_type + source_path. Most skills are "inline" — the body
--    column is the entire SKILL.md. Skills with assets (scripts/,
--    reference/) get source_type = 'path' and a source_path on disk;
--    body still mirrors SKILL.md so search keeps working. 'git' is
--    reserved for the planned clone-to-managed-dir mechanism.
--
-- SQLite UNIQUE on (workspace_id, name, version) treats every NULL as
-- distinct, which would let two NULL-workspace publishes of the same
-- (name, version) succeed. We work around that with a partial unique
-- index using COALESCE(workspace_id, '').

CREATE TABLE skill_registry_entries_new (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    version             INTEGER NOT NULL,
    content_hash        TEXT NOT NULL,
    description         TEXT NOT NULL,
    body                TEXT NOT NULL,
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    tags_json           TEXT NOT NULL DEFAULT '[]',
    author              TEXT NOT NULL DEFAULT '',
    parent_version      INTEGER,
    deleted_at          INTEGER,
    published_at        INTEGER NOT NULL,
    created_by_agent_id TEXT,
    workspace_id        TEXT,
    source_type         TEXT NOT NULL DEFAULT 'inline',
    source_path         TEXT
);

INSERT INTO skill_registry_entries_new (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    deleted_at, published_at, created_by_agent_id
) SELECT
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    deleted_at, published_at, created_by_agent_id
FROM skill_registry_entries;

DROP TABLE skill_registry_entries;
ALTER TABLE skill_registry_entries_new RENAME TO skill_registry_entries;

CREATE INDEX idx_skill_reg_name
    ON skill_registry_entries(name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_skill_reg_hash
    ON skill_registry_entries(name, content_hash)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_skill_reg_workspace
    ON skill_registry_entries(workspace_id, name)
    WHERE deleted_at IS NULL;

-- COALESCE-based UNIQUE: enforces one row per (workspace, name, version)
-- including the NULL (global) case where naive UNIQUE would let dupes in.
CREATE UNIQUE INDEX uniq_skill_reg_scoped
    ON skill_registry_entries(COALESCE(workspace_id, ''), name, version);
