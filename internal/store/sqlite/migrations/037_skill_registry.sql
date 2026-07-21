-- 037_skill_registry.sql — agent-facing skills registry.
--
-- Distinct from installed_skills (signed .mcskill bundles, ADR 0002):
-- registry rows are pure SKILL.md text exposed via mcpx__skill_search
-- /get/publish so any agent can ASK for a skill in natural language and
-- get the most relevant one back, contribute new ones, or iterate on
-- existing ones with versioning.
--
-- Versioning model (LangSmith-style): linear monotonic int per name,
-- plus content_hash for dedup (no version bump if body unchanged).
-- Tags table holds manually-set labels like @stable; @latest is derived
-- as MAX(version) WHERE deleted_at IS NULL.
--
-- Per-machine. NOT auto-shared across mesh peers — see registry.go.

CREATE TABLE skill_registry_entries (
    id                  TEXT PRIMARY KEY,           -- uuid
    name                TEXT NOT NULL,              -- agentskills.io name
    version             INTEGER NOT NULL,           -- 1, 2, 3 …
    content_hash        TEXT NOT NULL,              -- sha256 of body
    description         TEXT NOT NULL,              -- frontmatter description (≤1024)
    body                TEXT NOT NULL,              -- full SKILL.md verbatim
    metadata_json       TEXT NOT NULL DEFAULT '{}', -- parsed frontmatter as JSON
    tags_json           TEXT NOT NULL DEFAULT '[]', -- ["foo","bar"]
    author              TEXT NOT NULL DEFAULT '',   -- "system" | agent_id | user
    parent_version      INTEGER,                    -- nullable; what was edited
    deleted_at          INTEGER,                    -- soft delete (unix sec)
    published_at        INTEGER NOT NULL,           -- unix seconds
    created_by_agent_id TEXT,
    UNIQUE(name, version)
);

CREATE INDEX idx_skill_reg_name
    ON skill_registry_entries(name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_skill_reg_hash
    ON skill_registry_entries(name, content_hash)
    WHERE deleted_at IS NULL;

-- Manually-set tags. @latest is derived (MAX version) — never stored.
CREATE TABLE skill_registry_tags (
    name    TEXT NOT NULL,
    tag     TEXT NOT NULL,                          -- "@stable", custom
    version INTEGER NOT NULL,
    set_at  INTEGER NOT NULL,
    set_by  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (name, tag)
);
