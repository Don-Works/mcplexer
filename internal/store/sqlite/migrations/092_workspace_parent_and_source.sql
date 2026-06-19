-- 092_workspace_parent_and_source.sql
--
-- M6 (Brain): hierarchy + federation (docs/brain.md Appendix C.1 + C.2).
--
-- C.1 — Grouping hierarchy above the workspace. A client/org is modelled as
-- a PARENT workspace; its repos' workspaces are children. The child's
-- recall/list scope fuses with the parent's (workspace ∪ parent ∪ global).
-- parent_id is nullable — a workspace with no parent is a root, exactly as
-- today. It references another workspaces.id; we deliberately do NOT add a
-- FOREIGN KEY (SQLite FKs are off by default in this codebase and a dangling
-- parent must degrade to "no parent" rather than block the write — the
-- brain file is canonical, and a parent referenced before it is indexed
-- self-heals on the next reindex).
--
-- C.2 — Distributed .mcplexer/ folders. A workspace's brain files may live
-- either in the CENTRAL brain (~/mcplexer-brain/workspaces/<slug>/) or in a
-- per-repo .mcplexer/ directory that rides the project's own git history.
-- index_files.source records which produced a given row so the precedence
-- rule (repo-local is canonical when present) is auditable. Default
-- 'central' so every pre-existing row keeps its meaning.
--
-- Both columns are plain TEXT; SQLite cannot ALTER TABLE ... ADD CONSTRAINT,
-- and a documentary enum is not worth a 12-step table rebuild. Re-runs are
-- guarded by the migration runner (each file applies once), so the ALTERs
-- are written without IF NOT EXISTS (SQLite does not support it on ADD
-- COLUMN); the indexes use IF NOT EXISTS.

ALTER TABLE workspaces ADD COLUMN parent_id TEXT;

ALTER TABLE index_files ADD COLUMN source TEXT NOT NULL DEFAULT 'central';

-- Ancestor-chain walks (resolveChainForPath follows parent_id upward).
CREATE INDEX IF NOT EXISTS idx_workspaces_parent ON workspaces(parent_id);

-- Precedence sweeps + per-source verify ("show me the repo-sourced rows").
CREATE INDEX IF NOT EXISTS idx_index_files_source ON index_files(source);
