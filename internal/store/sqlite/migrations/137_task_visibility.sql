-- 137 — Per-task collaboration visibility and immutable disclosure receipts.
--
-- Every pre-existing task is private and owned by the local principal when a
-- local principal exists. A model or legacy peer cannot widen a task through a
-- normal task update; only the dedicated visibility service mutates these
-- columns and the exact restricted audience rows below.

ALTER TABLE tasks ADD COLUMN owner_principal_id TEXT REFERENCES p2p_principals(id);
ALTER TABLE tasks ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private', 'restricted', 'workspace'));
ALTER TABLE tasks ADD COLUMN visibility_epoch INTEGER NOT NULL DEFAULT 1
    CHECK (visibility_epoch >= 1);
ALTER TABLE tasks ADD COLUMN visibility_updated_by_principal_id TEXT REFERENCES p2p_principals(id);
ALTER TABLE tasks ADD COLUMN visibility_updated_at INTEGER;

UPDATE tasks
SET owner_principal_id = (
        SELECT id FROM p2p_principals WHERE is_local_owner = 1 LIMIT 1
    ),
    visibility = 'private',
    visibility_epoch = 1,
    visibility_updated_by_principal_id = (
        SELECT id FROM p2p_principals WHERE is_local_owner = 1 LIMIT 1
    ),
    visibility_updated_at = updated_at;

CREATE INDEX idx_tasks_visibility
    ON tasks(workspace_id, visibility, updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE task_visibility_audience (
    id                      TEXT PRIMARY KEY,
    task_id                 TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    principal_id            TEXT NOT NULL REFERENCES p2p_principals(id),
    added_by_principal_id   TEXT NOT NULL REFERENCES p2p_principals(id),
    visibility_epoch        INTEGER NOT NULL CHECK (visibility_epoch >= 1),
    added_at                INTEGER NOT NULL,
    revoked_at              INTEGER
);

CREATE UNIQUE INDEX idx_task_visibility_audience_active
    ON task_visibility_audience(task_id, principal_id)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_task_visibility_audience_principal
    ON task_visibility_audience(principal_id, task_id, revoked_at);

CREATE TABLE task_disclosures (
    id                     TEXT PRIMARY KEY,
    task_id                TEXT NOT NULL REFERENCES tasks(id),
    share_id               TEXT NOT NULL REFERENCES p2p_workspace_shares(share_id),
    recipient_principal_id TEXT NOT NULL REFERENCES p2p_principals(id),
    recipient_device_id    TEXT REFERENCES p2p_principal_devices(id),
    recipient_peer_id      TEXT NOT NULL,
    access_epoch           INTEGER NOT NULL CHECK (access_epoch >= 1),
    visibility_epoch       INTEGER NOT NULL CHECK (visibility_epoch >= 1),
    projection_sha256      TEXT NOT NULL CHECK (length(projection_sha256) = 64),
    projection_bytes       INTEGER NOT NULL CHECK (projection_bytes >= 0),
    egress_profile         TEXT NOT NULL,
    disclosed_at           INTEGER NOT NULL
);

CREATE INDEX idx_task_disclosures_task
    ON task_disclosures(task_id, disclosed_at DESC);
CREATE INDEX idx_task_disclosures_recipient
    ON task_disclosures(recipient_principal_id, disclosed_at DESC);
