-- 063 — Denormalize workspace_id onto worker_runs (G5 from the
-- workspace-scoping audit).
--
-- Why: worker_runs is the execution ledger. Without workspace_id on the
-- row, scoping reads by workspace requires a JOIN to workers, and a
-- hard-delete of the parent worker (workers.go's DeleteWorker keeps
-- runs around on purpose so the audit ledger survives) leaves orphaned
-- rows un-attributable to a workspace. Denormalizing is the cheapest
-- fix and matches the pattern of mesh_messages / audit_records.
--
-- Backfill: copy workspace_id across from the workers table by JOIN.
-- Rows whose parent worker is already gone (a hard-delete that
-- preceded this migration) end up with workspace_id = '' — flagged in
-- the index name so the operator can spot them.

ALTER TABLE worker_runs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';

UPDATE worker_runs
SET workspace_id = (
    SELECT workers.workspace_id
    FROM workers
    WHERE workers.id = worker_runs.worker_id
)
WHERE workspace_id = '' AND EXISTS (
    SELECT 1 FROM workers WHERE workers.id = worker_runs.worker_id
);

CREATE INDEX IF NOT EXISTS idx_worker_runs_workspace_started
    ON worker_runs(workspace_id, started_at DESC);
