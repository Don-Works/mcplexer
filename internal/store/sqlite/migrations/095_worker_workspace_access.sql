-- 095 — explicit per-worker workspace access grants.
--
-- Worker.workspace_id remains the preferred/home workspace for identity,
-- scheduling, and default routing. This table is the full workspace
-- visibility set: access='read' permits reads; access='write' permits
-- reads + mutations.

CREATE TABLE IF NOT EXISTS worker_workspace_access (
    worker_id    TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    access       TEXT NOT NULL CHECK(access IN ('read', 'write')),
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (worker_id, workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_worker_workspace_access_workspace
    ON worker_workspace_access(workspace_id, access);

INSERT OR IGNORE INTO worker_workspace_access (
    worker_id, workspace_id, access, created_at, updated_at
)
SELECT id, workspace_id, 'write', created_at, updated_at
FROM workers
WHERE workspace_id <> '';
