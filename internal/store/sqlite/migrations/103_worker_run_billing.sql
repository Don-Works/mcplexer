-- 103 — Add billing columns to worker_runs for per-run billing reality.
--
-- Why: downstream accounting needs to record the billing model
-- (metered vs subscription vs free), the subscription bucket that
-- covers the run, and the real out-of-pocket cost. These are set by
-- callers at finalize time; this migration is pure plumbing.

ALTER TABLE worker_runs ADD COLUMN billing_model TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_runs ADD COLUMN subscription_bucket TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_runs ADD COLUMN real_cost_usd REAL NOT NULL DEFAULT 0;
