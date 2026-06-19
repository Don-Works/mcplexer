-- 075 — Skill refinement proposals (W3).
--
-- A proposal is an agent's suggestion that a particular skill version
-- could be improved — it pairs concrete "friction" (what was annoying
-- / failed) with a "suggested_change" (proposed diff or rewrite) and a
-- short rationale. Proposals are NEVER applied automatically: every
-- promotion is a separate, audited human (or quorum-aided) decision.
--
-- The mesh-quorum aggregator counts similar proposals (same skill_name
-- + fuzzy friction match) across agents/peers. When the count crosses
-- the configurable threshold (see internal/skillregistry/refinement_quorum.go)
-- the freshest matching proposal transitions to `candidate`, broadcasts
-- a mesh finding, and surfaces on the dashboard inbox for review.
--
-- Status lifecycle: pending → candidate → promoted | rejected.
-- Reaching `promoted` records the decision; the actual skill_registry
-- version bump is a follow-up step (handler stub the path with a TODO
-- until W3 promotion plumbing lands).
--
-- proposed_by_peer_id is nullable so local-only deployments don't need
-- the libp2p substrate; cross-peer attribution arrives when the gateway
-- has a peer ID for the originating mesh envelope.
CREATE TABLE IF NOT EXISTS skill_refinement_proposals (
    id                     TEXT PRIMARY KEY,
    skill_name             TEXT NOT NULL,
    skill_version          INTEGER NOT NULL,
    friction               TEXT NOT NULL,
    suggested_change       TEXT NOT NULL,
    rationale              TEXT NOT NULL,
    proposed_by_session_id TEXT NOT NULL,
    proposed_by_peer_id    TEXT,
    workspace_id           TEXT NOT NULL,
    created_at             TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'pending',
    candidate_at           TEXT,
    resolved_at            TEXT,
    resolved_by_session_id TEXT,
    resolution_note        TEXT,
    metadata_json          TEXT NOT NULL DEFAULT '{}'
);

-- "list inbox by skill, ordered by recency" — the dashboard's hottest
-- path. status is in the prefix so the index doubles for "pending vs
-- candidate vs resolved" buckets.
CREATE INDEX IF NOT EXISTS idx_refinement_skill_status
    ON skill_refinement_proposals(skill_name, status, created_at DESC);

-- Workspace-scoped listing (per-team review queue).
CREATE INDEX IF NOT EXISTS idx_refinement_workspace
    ON skill_refinement_proposals(workspace_id, created_at DESC);
