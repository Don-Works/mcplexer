-- 036_mesh_agent_status.sql — adds a free-form persistent status field
-- to mesh_agents. Lets agents advertise what they're currently doing
-- ("building X, ETA 5m" / "idle" / "blocked on Y") so humans + peers
-- can triage at a glance without parsing message streams.
--
-- Free-form text up to ~120 chars. NOT an enum on purpose — agents
-- pick their own words. Defaulting empty so legacy rows + slim builds
-- keep the pre-status behaviour.

ALTER TABLE mesh_agents ADD COLUMN status TEXT NOT NULL DEFAULT '';
