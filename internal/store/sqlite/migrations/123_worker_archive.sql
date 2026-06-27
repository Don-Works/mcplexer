-- 123_worker_archive.sql — first-class archived Workers.
--
-- Archived workers preserve config and run history, but are excluded from
-- normal worker lists and must never be scheduled or dispatched.

ALTER TABLE workers ADD COLUMN archived_at DATETIME NULL;
ALTER TABLE workers ADD COLUMN archived_reason TEXT NOT NULL DEFAULT '';

-- Worker names should be reusable after archiving. The old index enforced
-- uniqueness across all historical rows; replace it with a live-row partial
-- unique index.
DROP INDEX IF EXISTS idx_workers_workspace_name;

CREATE UNIQUE INDEX IF NOT EXISTS idx_workers_workspace_name_live
    ON workers(workspace_id, name)
    WHERE archived_at IS NULL;
