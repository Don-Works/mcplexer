-- 100 - Applied-migrations ledger.
--
-- The legacy schema_version table stores only the highest applied
-- version number. That single-row watermark cannot tell us *which*
-- migrations were actually applied, what their on-disk content was,
-- or whether a migration file was silently skipped when the watermark
-- was bumped past it (the 072 task-hlc outage that broke the live
-- task ledger).
--
-- This table is the source of truth for "what did this DB actually
-- run?" — one row per applied migration, with the filename and
-- SHA256 checksum of the file as it was at apply time. Combined with
-- a Go-side verifyLedger() guard, it catches four classes of bug:
--
--   1. skipped_migration  - file on disk with version <= MAX(schema_version)
--                           but no row here. The 072 outage pattern.
--   2. checksum_mismatch  - the on-disk file was modified after apply.
--   3. orphan_row         - a row here references a version that has
--                           no on-disk file (partial restore, branch
--                           swap that deleted the file).
--   4. collision          - two on-disk files claiming the same version
--                           (caught at listMigrations() time, but the
--                           PRIMARY KEY on version also enforces it
--                           at the DB layer as a backstop).
--
-- Booting the daemon triggers a one-shot backfill from
-- schema_version on the first run after this migration is added, so
-- existing installs upgrade without a false-positive skipped-migration
-- storm.

CREATE TABLE IF NOT EXISTS applied_migrations (
    version    INTEGER PRIMARY KEY,
    filename   TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_applied_migrations_filename
    ON applied_migrations(filename);
