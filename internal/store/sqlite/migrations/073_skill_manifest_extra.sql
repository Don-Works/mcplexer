-- 073 — Skill manifest_extra column.
--
-- Stores the structured frontmatter fields parsed from SKILL.md (or the
-- equivalent TOML manifest of a .mcskill bundle): `requires`, `produces`,
-- `consumes`, `phases`, `refinement`. See internal/skills/manifest_extra.go
-- for the canonical Go type and JSON shape.
--
-- Why a dedicated column instead of stuffing it into metadata_json:
--   * downstream features (W2 telemetry tasks, W3 refinement loop, W6
--     composition graph) need to query/sort on these fields without
--     decoding the freeform metadata blob;
--   * the column has a stable schema; metadata_json is opaque;
--   * NULL/empty rows trivially mean "skill doesn't declare extras"
--     — backward compatibility comes for free.
--
-- DEFAULT '{}' so cold reads on pre-073 rows return the zero-value
-- ManifestExtra without special-casing. Self-heal pattern: see
-- ensureSkillManifestExtra in internal/store/sqlite/migrate.go — that
-- function ALTERs the column in if migration 073 was skipped on a
-- partially-restored install.

ALTER TABLE skill_registry_entries
    ADD COLUMN manifest_extra TEXT NOT NULL DEFAULT '{}';
