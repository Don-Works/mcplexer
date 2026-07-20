-- 147 — Task-resolution feedback into Monitoring triage.
--
-- Before this migration, closing a logwatch task taught the monitoring layer
-- nothing: the same class kept waking the model, kept filing, kept notifying.
-- monitoring_incidents.task_id already linked incident -> task and
-- tasks.meta.$.logwatch_class already linked task -> class, but nothing read
-- either link when a task reached a terminal status.
--
-- This table is the durable, reversible, attributable record of what a task
-- resolution DID to its incident. It exists so an operator can always answer
-- "what is currently suppressed, why, and who did it" and undo any of it.
-- Every column is written from data the daemon already has; no model is
-- consulted on this path.

CREATE TABLE IF NOT EXISTS monitoring_resolutions (
    -- One live resolution per incident. A cleared row keeps its history via
    -- cleared_at; re-resolving the same incident REPLACEs the row (see the
    -- store's upsert), so the table never grows a row per recurrence.
    incident_id            TEXT PRIMARY KEY
                             REFERENCES monitoring_incidents(id) ON DELETE CASCADE,
    workspace_id           TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    task_id                TEXT NOT NULL,

    -- outcome is the deterministic mapping of the task's terminal vocabulary
    -- kind onto the monitoring vocabulary:
    --   task kind "cancelled" (wontfix/rejected/abandoned) -> 'benign'
    --   task kind "done"      (fixed/resolved/shipped)     -> 'fixed'
    -- 'benign' suppresses; 'fixed' deliberately does NOT. Conflating the two
    -- is how a real regression gets swallowed, so they are stored distinctly.
    outcome                TEXT NOT NULL,
    -- The exact freeform status word that closed the task, kept verbatim so
    -- the operator sees their own vocabulary rather than our bucket name.
    status_text            TEXT NOT NULL DEFAULT '',

    -- Reversal state. disposition_before is the incident disposition at the
    -- moment of suppression; clearing restores exactly this value rather than
    -- guessing "actionable".
    disposition_before     TEXT NOT NULL DEFAULT '',
    severity_at_resolution TEXT NOT NULL DEFAULT '',
    -- Exactly which templates THIS resolution acked. Clearing un-acks only
    -- these, so an operator's own earlier monitoring__ack is never undone by
    -- our reversal.
    acked_template_ids_json TEXT NOT NULL DEFAULT '[]',

    -- Attribution: who/what closed the task.
    resolved_at            DATETIME NOT NULL,
    resolved_by_session    TEXT NOT NULL DEFAULT '',
    resolved_by_actor      TEXT NOT NULL DEFAULT '',

    -- Reversal audit. cleared_at IS NULL means the resolution is live; for
    -- outcome='benign' that also means "currently suppressed".
    cleared_at             DATETIME NULL,
    cleared_reason         TEXT NOT NULL DEFAULT '',
    cleared_by_session     TEXT NOT NULL DEFAULT '',

    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL
);

-- The operator read path: "what is currently suppressed in this workspace".
CREATE INDEX IF NOT EXISTS idx_monitoring_resolutions_live
    ON monitoring_resolutions(workspace_id, resolved_at DESC)
    WHERE cleared_at IS NULL;

-- Reverse lookup from a task id, used when a task is reopened.
CREATE INDEX IF NOT EXISTS idx_monitoring_resolutions_task
    ON monitoring_resolutions(task_id);
