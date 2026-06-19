-- 105_task_assignee_user_id.sql - human/user task assignee support.
--
-- Adds assignee_user_id to the tasks table. Human assignees are identified
-- by users.user_id rather than a session_id or peer_id, so tasks can be
-- assigned to a human across machines/sessions without being tied to one
-- agent session. Human-assigned tasks are not reclaimed by lease sweeps.

ALTER TABLE tasks ADD COLUMN assignee_user_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_tasks_assignee_user
    ON tasks(assignee_user_id)
    WHERE deleted_at IS NULL AND assignee_user_id != '';
