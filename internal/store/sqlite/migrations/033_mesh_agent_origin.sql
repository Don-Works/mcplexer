-- M-mesh-origin: tag every mesh_agents row with where it was discovered.
--
-- Values:
--   "local"          — agent connected to this daemon's stdio MCP socket
--   "peer:<peer_id>" — agent observed via an inbound libp2p envelope
--
-- The UI uses this to distinguish locally-attached Claude Code/Codex
-- sessions from agents reached over libp2p. Defaults to "local" so existing
-- rows match the historical (single-host) interpretation.

ALTER TABLE mesh_agents ADD COLUMN origin TEXT NOT NULL DEFAULT 'local';
CREATE INDEX IF NOT EXISTS idx_mesh_agents_origin ON mesh_agents(origin);
