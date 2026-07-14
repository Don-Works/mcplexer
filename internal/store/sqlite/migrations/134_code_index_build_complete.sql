-- 134 — Persist whether a code-index build covered its full enumeration.
-- Partial rows remain queryable but are reported stale and retried on the next
-- query; pre-existing rows default to complete.

ALTER TABLE code_index_builds ADD COLUMN complete INTEGER NOT NULL DEFAULT 1;
