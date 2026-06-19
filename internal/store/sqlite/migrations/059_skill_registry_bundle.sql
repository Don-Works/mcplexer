-- 059 — Add bundle storage to skill_registry_entries.
--
-- Background: the registry was text-only — body holds the SKILL.md
-- markdown and that's it. Skills with sidecar files (scripts/,
-- reference/) couldn't ride the registry path; the existing
-- source_type='path' marker (migration 038) pointed at a local
-- directory that doesn't travel across machines or mesh peers.
--
-- This migration adds two columns so a skill can carry its full
-- directory bundle through the registry as a tar.gz blob:
--
--   bundle         BLOB   — tar.gz of the skill directory. NULL = no bundle
--                           (inline or path-source skill). Capped in the
--                           publish handler at 25 MiB.
--   bundle_sha256  TEXT   — hex sha256 of the tar.gz bytes for integrity
--                           checks across the mesh share path.
--
-- When a bundle is present, source_type is set to 'bundle' by the
-- publisher. body still mirrors the SKILL.md inside the tar.gz so the
-- search index and skill_get text response keep working without
-- unpacking the bundle.
--
-- Backward compat: existing rows keep bundle = NULL, source_type stays
-- 'inline' or 'path', and every existing query continues to work.

ALTER TABLE skill_registry_entries
    ADD COLUMN bundle BLOB;

ALTER TABLE skill_registry_entries
    ADD COLUMN bundle_sha256 TEXT;
