-- The usage dashboard reads worker runs across every worker for a bounded
-- time window. Existing indexes lead with worker_id or workspace_id, so they
-- cannot satisfy this global started_at range scan.
CREATE INDEX IF NOT EXISTS idx_worker_runs_started_at
    ON worker_runs(started_at DESC);
