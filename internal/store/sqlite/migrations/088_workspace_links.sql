-- 088 — Linked workspaces: promote workspace_peer_bindings from a
-- one-way offer-routing memo into an optionally-symmetric "link".
--
-- A linked binding opts a (peer, remote_workspace) ↔ local_workspace
-- pair into silent task replication — the same Tier-1 same-user posture
-- memory + skills already use (see internal/replication/). Identity
-- stays LOCAL: a link is an explicit operator declaration, never derived
-- from path / name / branch. See .planning/linked-workspaces/PLAN.md.
--
--   linked              1 = this binding is an explicit bidirectional link
--   link_established_by 'local' (declared here) | 'peer' (mirror) | ''
--   link_established_at  unix seconds; null when not linked
--
-- The columns are added defensively here AND by the ensureWorkspaceLinkColumns
-- schema invariant in migrate.go, so an install whose schema_version was
-- bumped past this migration without applying it still self-heals on boot.

ALTER TABLE workspace_peer_bindings ADD COLUMN linked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspace_peer_bindings ADD COLUMN link_established_by TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_peer_bindings ADD COLUMN link_established_at INTEGER;

-- Send-side lookup: "which linked peers does this local workspace push to?"
CREATE INDEX IF NOT EXISTS idx_workspace_peer_bindings_linked
    ON workspace_peer_bindings(local_workspace_id)
    WHERE linked = 1;
