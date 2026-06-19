-- M4 — Mesh-triggered Workers.
--
-- A Worker can fire when a matching mesh message arrives, in addition to
-- (or instead of) its cron / interval schedule. Each Worker can have any
-- number of triggers; each trigger is a conjunctive filter over the
-- arriving message (tag overlap, kind, audience, content regex, from
-- filter), throttled per source so a chatty peer can't run the worker
-- into the budget caps.
--
-- max_chain_depth bounds reflexive trigger chains: when the runner's
-- mesh output emits with a chain-depth tag (e.g. "chain-depth:2"), the
-- dispatcher refuses to fire on a message whose depth >= the trigger's
-- cap. This is the loop guard that keeps a "trigger on alert" worker
-- from infinitely re-triggering itself across paired peers.
--
-- from_filter_json holds a JSON array of {peer_id?, agent_name?, role?}
-- structs. ANY field matching means the message is admitted; an empty
-- array means "anyone".
--
-- Per-peer permission is enforced separately via p2p_peers.scopes:
-- the dispatcher checks for "trigger_worker:<worker_name>" or
-- "trigger_worker:*" before firing a worker on a message that came in
-- from a paired peer. Local messages bypass the scope check.

CREATE TABLE IF NOT EXISTS worker_mesh_triggers (
    id TEXT PRIMARY KEY,
    worker_id TEXT NOT NULL,
    tag_match TEXT NOT NULL DEFAULT '',          -- comma-separated; empty = any
    kind_match TEXT NOT NULL DEFAULT '',         -- one mesh kind; empty = any
    audience_match TEXT NOT NULL DEFAULT '',     -- "*", session-id, or role; empty = any
    content_regex TEXT NOT NULL DEFAULT '',      -- regex against MeshMessage.Content; empty = any
    from_filter_json TEXT NOT NULL DEFAULT '[]', -- JSON array of TriggerFromFilter
    throttle_seconds INTEGER NOT NULL DEFAULT 60,
    max_chain_depth INTEGER NOT NULL DEFAULT 3,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_worker_mesh_triggers_worker
    ON worker_mesh_triggers(worker_id);
CREATE INDEX IF NOT EXISTS idx_worker_mesh_triggers_enabled
    ON worker_mesh_triggers(enabled);

-- Trigger provenance on worker_runs so the run ledger records WHY a run
-- fired. Defaults preserve the existing schedule-driven contract: empty
-- trigger_kind reads back as "schedule" via the scan helper.
ALTER TABLE worker_runs ADD COLUMN trigger_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_runs ADD COLUMN trigger_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_runs ADD COLUMN trigger_source_peer TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_runs ADD COLUMN trigger_chain_depth INTEGER NOT NULL DEFAULT 0;
