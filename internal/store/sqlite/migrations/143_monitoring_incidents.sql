-- 143 — Durable Monitoring incident classes, occurrences, and triage effects.
--
-- A log template is the collector's exact masked shape. An incident is the
-- stable operational class that one or more templates belong to. Keeping the
-- two separate lets repeated observations update one canonical task without
-- asking an AI worker to rediscover/list/dedupe the task every time.

ALTER TABLE log_templates ADD COLUMN triaged_at DATETIME NULL;
ALTER TABLE log_templates ADD COLUMN triaged_severity TEXT NOT NULL DEFAULT '';

-- Do not wake the AI for the entire historical backlog on upgrade. Existing
-- templates keep their current behaviour; newly inserted templates start with
-- triaged_at=NULL and are handled by the durable pending queue.
UPDATE log_templates
SET triaged_at = last_seen,
    triaged_severity = severity
WHERE triaged_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_log_templates_pending
    ON log_templates(source_id, last_seen DESC)
    WHERE acked = 0 AND triaged_at IS NULL;

CREATE TABLE IF NOT EXISTS monitoring_incidents (
    id                     TEXT PRIMARY KEY,
    workspace_id           TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    class_key              TEXT NOT NULL,
    task_id                TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    disposition            TEXT NOT NULL,
    severity               TEXT NOT NULL,
    title                  TEXT NOT NULL DEFAULT '',
    occurrence_count       INTEGER NOT NULL DEFAULT 0,
    event_count            INTEGER NOT NULL DEFAULT 0,
    first_seen             DATETIME NOT NULL,
    last_seen              DATETIME NOT NULL,
    last_notified_at       DATETIME NULL,
    last_notified_severity TEXT NOT NULL DEFAULT '',
    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL,
    UNIQUE(workspace_id, class_key)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_incidents_task
    ON monitoring_incidents(task_id);
CREATE INDEX IF NOT EXISTS idx_monitoring_incidents_workspace_seen
    ON monitoring_incidents(workspace_id, last_seen DESC);

CREATE TABLE IF NOT EXISTS monitoring_incident_templates (
    template_id TEXT PRIMARY KEY REFERENCES log_templates(id) ON DELETE CASCADE,
    incident_id TEXT NOT NULL REFERENCES monitoring_incidents(id) ON DELETE CASCADE,
    linked_at   DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_monitoring_incident_templates_incident
    ON monitoring_incident_templates(incident_id);

CREATE TABLE IF NOT EXISTS monitoring_occurrences (
    id                TEXT PRIMARY KEY,
    incident_id       TEXT NOT NULL REFERENCES monitoring_incidents(id) ON DELETE CASCADE,
    occurrence_key    TEXT NOT NULL,
    source_id         TEXT NOT NULL DEFAULT '',
    template_ids_json TEXT NOT NULL DEFAULT '[]',
    severity          TEXT NOT NULL,
    event_count       INTEGER NOT NULL DEFAULT 0,
    first_seen        DATETIME NOT NULL,
    last_seen         DATETIME NOT NULL,
    evidence          TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL,
    UNIQUE(incident_id, occurrence_key)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_occurrences_incident_seen
    ON monitoring_occurrences(incident_id, last_seen DESC);

-- A completed receipt is the runner's domain postcondition. A model response
-- is not considered a successful log-watch run unless its run_id appears here.
CREATE TABLE IF NOT EXISTS monitoring_triage_receipts (
    run_id       TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    incident_id  TEXT NULL REFERENCES monitoring_incidents(id) ON DELETE SET NULL,
    disposition  TEXT NOT NULL,
    completed_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_monitoring_triage_receipts_workspace
    ON monitoring_triage_receipts(workspace_id, completed_at DESC);

-- The pending digest claims exactly the templates it rendered for a worker
-- run. A boolean "some effect happened" is insufficient when one batched
-- decision succeeds and a later one fails; every claimed template must be
-- completed before the post-execute check passes.
CREATE TABLE IF NOT EXISTS monitoring_triage_claims (
    run_id       TEXT NOT NULL REFERENCES worker_runs(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    template_id  TEXT NOT NULL REFERENCES log_templates(id) ON DELETE CASCADE,
    completed    INTEGER NOT NULL DEFAULT 0,
    claimed_at   DATETIME NOT NULL,
    completed_at DATETIME NULL,
    PRIMARY KEY(run_id, template_id)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_triage_claims_incomplete
    ON monitoring_triage_claims(run_id, completed)
    WHERE completed = 0;

-- Close the historical list-then-create race at the database boundary. The
-- task service can optimistically create and recover the winning canonical row
-- on ErrAlreadyExists without ever producing two live tasks for one class.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_logwatch_class_unique
    ON tasks(workspace_id,
        CASE WHEN json_valid(meta)
             THEN json_extract(meta, '$.logwatch_class')
             ELSE NULL END)
    WHERE deleted_at IS NULL
      AND CASE WHEN json_valid(meta)
               THEN json_type(meta, '$.logwatch_class') = 'text'
                    AND json_extract(meta, '$.logwatch_class') <> ''
               ELSE 0 END;
