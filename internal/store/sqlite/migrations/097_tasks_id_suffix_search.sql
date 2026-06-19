-- 097 — Task search by human-facing 6-char suffix.
--
-- The dashboard renders task IDs as the last six ULID characters so humans
-- can remember and type them. Search treats that displayed suffix as a normal
-- task lookup term and returns every matching row; collisions are acceptable
-- because the result list shows the full task context before any action runs.

CREATE INDEX IF NOT EXISTS idx_tasks_id_suffix
    ON tasks(substr(id, -6), workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL;
