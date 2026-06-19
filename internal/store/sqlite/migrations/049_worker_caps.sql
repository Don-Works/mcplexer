-- M1 — Workers safety: per-worker caps + auto-pause metadata + worker
-- approvals table.
--
-- The runner currently uses package-default Caps. Per-worker overrides
-- let an operator set tighter budgets on a long-running worker without
-- editing code. Zero means "use the package default / no cap" so old
-- rows continue to behave as before.
--
-- max_monthly_cost_usd + max_consecutive_failures back the inline auto-
-- pause checks fired by the runner at finalize time. auto_paused_reason
-- records WHY a worker was paused (manual edits leave it empty).
--
-- worker_approvals is a fire-and-forget approval ledger: when a propose-
-- mode worker hits a write-class tool, we persist a row here, fire a
-- mesh alert, and stop the run. Later the operator decides via the UI
-- or `mcplexer__decide_worker_approval` MCP tool; ApproveAndResume
-- fires a NEW run with PreApprovedTools so propose-gating skips the
-- listed tool name. Mid-run resume is a future fix (would need loop
-- snapshotting). See internal/workers/runner/loop.go.

ALTER TABLE workers ADD COLUMN max_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN max_output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN max_tool_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN max_wall_clock_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN max_monthly_cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN max_consecutive_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN auto_paused_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS worker_approvals (
    id           TEXT PRIMARY KEY,
    worker_id    TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    tool_name    TEXT NOT NULL,
    tool_input   TEXT NOT NULL DEFAULT '{}',
    reason       TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    decision     TEXT NOT NULL DEFAULT '',
    decided_by   TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at   DATETIME NULL,
    resumed_run_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_worker_approvals_status_created
    ON worker_approvals(status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_worker_approvals_worker
    ON worker_approvals(worker_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_worker_approvals_run
    ON worker_approvals(run_id);
