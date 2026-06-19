-- M7.3 — Repo + branch scoped mesh signals.
--
-- Add optional repo / branch / workspace_path / repo_remote columns to
-- mesh_messages so cross-machine mesh signals can be filtered by which
-- repo + branch the sender was working in. Backward compat: pre-M7.3
-- peers omit these fields and the columns default to '' on insert.

ALTER TABLE mesh_messages ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE mesh_messages ADD COLUMN branch TEXT NOT NULL DEFAULT '';
ALTER TABLE mesh_messages ADD COLUMN workspace_path TEXT NOT NULL DEFAULT '';
ALTER TABLE mesh_messages ADD COLUMN repo_remote TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_mesh_msg_repo
    ON mesh_messages(repo) WHERE repo != '';
CREATE INDEX IF NOT EXISTS idx_mesh_msg_repo_branch
    ON mesh_messages(repo, branch) WHERE repo != '';
