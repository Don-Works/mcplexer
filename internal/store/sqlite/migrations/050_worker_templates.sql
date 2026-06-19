-- M3 — Worker templates: publishable Worker shapes via the skill registry.
--
-- Two orthogonal additions:
--
-- 1) skill_registry_entries gains a `payload_type` column. Default 'skill'
--    (every existing row backfills to 'skill', preserving the
--    mcpx__skill_* surface). Value 'worker' marks an entry whose body is
--    a JSON-encoded WorkerTemplate (see internal/skillregistry/
--    worker_template.go) rather than markdown.
--
-- 2) workers gains source_template_name + source_template_version columns
--    that track where the worker was installed from (NULL = hand-built).
--    The dashboard surfaces "vN available — review changes" when a newer
--    version of the source template appears.

ALTER TABLE skill_registry_entries
    ADD COLUMN payload_type TEXT NOT NULL DEFAULT 'skill';

CREATE INDEX IF NOT EXISTS idx_skill_reg_payload_type
    ON skill_registry_entries(payload_type)
    WHERE deleted_at IS NULL;

ALTER TABLE workers ADD COLUMN source_template_name    TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN source_template_version INTEGER NOT NULL DEFAULT 0;
