-- 071 — Tasks lease column.
--
-- Adds `lease_expires_at` to the tasks table (migration 061). Until
-- this column existed the UI used the row's `updated_at` as a
-- heartbeat proxy — fine for tasks that mutate frequently, but a
-- false-positive source for slow-burn work where the assignee is busy
-- for minutes between touches.
--
-- Lease semantics (enforced in internal/tasks/service.go):
--   * Set when status transitions to "doing" AND the row has an
--     assignee (either explicit or via the auto-claim path) — TTL is
--     5 minutes from the bump.
--   * Bumped by the assignee via task__heartbeat — silent no-op for
--     non-assignees, so peers can't extend each other's leases.
--   * Cleared by the background sweep (Service.SweepExpiredLeases,
--     ticked every minute) — the sweep also nulls the assignee +
--     appends evt=lease_expired to the row's status_history so the
--     audit trail shows why the row was abandoned.
--
-- Pre-071 rows default to NULL, which the UI's leaseStaleness helper
-- treats as "fall back to updated_at" (backward-compat for already-
-- live rows that haven't had their first heartbeat yet).

ALTER TABLE tasks ADD COLUMN lease_expires_at INTEGER;

CREATE INDEX IF NOT EXISTS idx_tasks_lease_expires
    ON tasks(lease_expires_at)
    WHERE lease_expires_at IS NOT NULL;
