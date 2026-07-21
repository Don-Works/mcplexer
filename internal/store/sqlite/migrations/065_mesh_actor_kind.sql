-- 065 — Plumb actor_kind onto mesh_messages.
--
-- Phase 2 of per-workspace tasks (see .planning/tasks/PLAN.md "Notify
-- suppression rules") needs to distinguish messages emitted by a
-- worker subprocess from those emitted by a live agent so the notify
-- gate can suppress worker-driven mutations from buzzing the user's
-- PWA. mesh.Send already collects the call-site info; we just need a
-- column to persist it through to subscribers + the SSE/audit stream.
--
-- Values: 'agent' (default; live-session human-driving-the-LLM) |
-- 'worker' (scheduled in-process agent loop) | 'user' (REST call from
-- the dashboard) | 'peer-import' (came in via libp2p) | 'system'
-- (daemon-internal, e.g. cleanup).
--
-- Pre-065 rows default to 'agent' which is the safe, observable
-- behaviour (notify gates still fire).

ALTER TABLE mesh_messages ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'agent';

CREATE INDEX IF NOT EXISTS idx_mesh_msg_actor_kind
    ON mesh_messages(actor_kind) WHERE actor_kind != 'agent';
