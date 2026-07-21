-- 053_audit_actor_columns.sql
--
-- Cross-actor incident queries motivated three new columns on
-- audit_records:
--
--   - actor_kind     — categorical: user, worker, scheduler, api, mesh,
--                      secrets, worker_admin (matches the existing
--                      ClientType conventions used by emit sites).
--   - actor_id       — the specific worker_id / run_id / peer_id /
--                      user_id / scope_id that identifies "who did this".
--   - correlation_id — request_id / run_id / execution_id tying multiple
--                      audit rows to one logical operation.
--
-- All three are TEXT NOT NULL DEFAULT '' so we can ALTER TABLE without
-- a rebuild and keep legacy rows valid. The composite index
-- (actor_kind, actor_id, timestamp DESC) is the workhorse for
-- "show me everything worker X did in the last hour" queries — without
-- it we were forced to grep ParamsRedacted JSON.
--
-- The CASE-based backfill derives actor_kind + actor_id from the
-- patterns already baked into existing emit sites (worker:<id>,
-- scope:<id>) so historical rows participate in the new index without
-- a downstream replay. correlation_id is left empty on backfill — no
-- reliable source exists for legacy rows and a downstream ctx-threading
-- PR will populate it for new rows.

ALTER TABLE audit_records ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_records ADD COLUMN actor_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_records ADD COLUMN correlation_id TEXT NOT NULL DEFAULT '';

-- Backfill actor_kind from client_type + session_id heuristics.
--
-- Branch order matters here. The original draft had
-- `client_type LIKE 'worker%'` first, which is greedy and catches BOTH
-- `worker` and `worker_admin` — collapsing two distinct actor classes
-- into the same forensics bucket. We branch on the more specific
-- `worker_admin` literal first, then fall through to the LIKE pattern
-- for the runner-class rows. Migration 054 corrects historical rows
-- that were backfilled by the buggy ordering on deployments which ran
-- 053 before this in-place fix shipped.
UPDATE audit_records SET actor_kind = CASE
    WHEN client_type = 'worker_admin' THEN 'worker_admin'
    WHEN client_type LIKE 'worker%' THEN 'worker'
    WHEN session_id LIKE 'scope:%' THEN 'secrets'
    WHEN client_type = 'claude-code' THEN 'user'
    ELSE client_type
END
WHERE actor_kind = '';

-- Backfill actor_id from the existing session_id prefixes.
UPDATE audit_records SET actor_id = CASE
    WHEN session_id LIKE 'worker:%' THEN substr(session_id, 8)
    WHEN session_id LIKE 'scope:%' THEN substr(session_id, 7)
    ELSE session_id
END
WHERE actor_id = '';

CREATE INDEX IF NOT EXISTS idx_audit_records_actor
    ON audit_records(actor_kind, actor_id, timestamp DESC);
