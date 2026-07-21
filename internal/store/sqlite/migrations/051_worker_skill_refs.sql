-- M0.7 — Worker multi-skill support.
--
-- Today a Worker carries a single (skill_name, skill_version) pair whose
-- body prepends the rendered prompt. This migration adds a JSON-array
-- column `skill_refs_json` that stores an ORDERED list of skill refs;
-- the runner loads each body in order and joins them with a markdown
-- separator before sending to the model.
--
-- BACKWARD COMPAT — the legacy skill_name/skill_version columns are
-- preserved. The Go layer's EffectiveSkillRefs() helper synthesises a
-- single-element array from the legacy columns when skill_refs_json is
-- empty ('[]'), so workers persisted before this migration keep behaving
-- correctly. On write, the Go layer always populates skill_refs_json
-- (the canonical source) and keeps the legacy columns mirrored for
-- forward compat.
--
-- The UPDATE backfills any existing row where skill_name is set so its
-- skill_refs_json reflects the same single-skill ref. Empty skill_name
-- rows get the default '[]'.

ALTER TABLE workers ADD COLUMN skill_refs_json TEXT NOT NULL DEFAULT '[]';

UPDATE workers
SET skill_refs_json = json_array(
        json_object(
            'name',    skill_name,
            'version', skill_version
        )
    )
WHERE skill_name IS NOT NULL AND skill_name != '';
