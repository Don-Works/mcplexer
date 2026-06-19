-- 057 — Split worker templates into their own table.
--
-- Background: in 050 we added a `payload_type` column to
-- skill_registry_entries so the same table could store both
-- SKILL.md markdown skills (payload_type='skill') AND
-- JSON-encoded WorkerTemplate rows (payload_type='worker').
-- That shortcut was wrong: the agent-facing mcpx__skill_*
-- handlers don't filter by payload_type, so worker templates
-- leaked into the skill catalog as JSON blobs.
--
-- This migration separates concerns:
--   1. Create worker_templates with the columns that actually
--      apply to worker shapes — no source_type / source_path /
--      payload_type ceremony.
--   2. Copy the worker rows out of skill_registry_entries.
--   3. Delete those rows from skill_registry_entries so the
--      skill catalog is markdown-only again.
--
-- The bundled rows from migration 052 are moved over by the
-- INSERT…SELECT below; we don't need to re-seed.
--
-- We leave the payload_type column on skill_registry_entries
-- in place. SQLite DROP COLUMN is fiddly (rebuild dance),
-- callers no longer reference it after this PR, and the
-- column is harmless once every row is 'skill'.

CREATE TABLE worker_templates (
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
    workspace_id        TEXT
);

CREATE INDEX idx_worker_templates_name
    ON worker_templates(name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_worker_templates_hash
    ON worker_templates(name, content_hash)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_worker_templates_workspace
    ON worker_templates(workspace_id, name)
    WHERE deleted_at IS NULL;

-- Mirror skill_registry_entries' COALESCE-based uniqueness:
-- one row per (workspace, name, version), treating NULL workspace
-- as a single bucket.
CREATE UNIQUE INDEX uniq_worker_templates_scoped
    ON worker_templates(COALESCE(workspace_id, ''), name, version);

INSERT INTO worker_templates (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    deleted_at, published_at, created_by_agent_id, workspace_id
)
SELECT
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    deleted_at, published_at, created_by_agent_id, workspace_id
FROM skill_registry_entries
WHERE payload_type = 'worker';

DELETE FROM skill_registry_entries WHERE payload_type = 'worker';
