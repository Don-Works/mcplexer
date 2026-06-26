-- 119 — Fast status-filter discovery for the Tasks page.
--
-- The UI asks for distinct statuses in the active task population. These
-- partial indexes keep the common all-workspaces/state=open case cheap while
-- the existing idx_tasks_workspace_status continues to cover workspace-scoped
-- status counts.

CREATE INDEX IF NOT EXISTS idx_tasks_status_open
    ON tasks(status)
    WHERE deleted_at IS NULL AND closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_status_closed
    ON tasks(status)
    WHERE deleted_at IS NULL AND closed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_status_live
    ON tasks(status)
    WHERE deleted_at IS NULL;
