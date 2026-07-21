-- 091_workspace_source_brain.sql
--
-- M4 (Brain): the source=brain config-apply path. The workspaces.source,
-- downstream_servers.source, and route_rules.source columns are already
-- plain TEXT with no CHECK constraint (migration 001), so 'brain' is
-- already storable — SQLite cannot ALTER TABLE ... ADD CONSTRAINT without a
-- full table rebuild, which is not worth it for a documentary enum.
--
-- This migration's only real content is an index on the source column for
-- the workspaces and the two config tables the brain prune query scans
-- (ApplyBrain lists by source and prunes stale source='brain' rows). The
-- indexes are IF NOT EXISTS so a re-run is a no-op.
CREATE INDEX IF NOT EXISTS idx_workspaces_source ON workspaces(source);
CREATE INDEX IF NOT EXISTS idx_route_rules_source ON route_rules(source);
CREATE INDEX IF NOT EXISTS idx_downstream_servers_source ON downstream_servers(source);
