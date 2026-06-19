-- 060_mesh_terminal_locator.sql
--
-- Captures terminal location for each mesh agent so the dashboard can offer
-- a "Focus" action that jumps the user's local tmux client to the right pane
-- (local agents) or spawns a window that SSHes into the peer and attaches to
-- their tmux at the right pane (remote agents).
--
-- For local agents: tmux_session/window/pane are populated from $TMUX +
-- `tmux display -p` when the agent registers via mesh__receive (or
-- mesh__set_terminal_locator).
--
-- For remote (peer-origin) agents: the same three fields gossip across over
-- the agent-directory protocol; the LOCAL daemon uses p2p_peers.ssh_target
-- to know how to SSH there.
--
-- ssh_target lives on p2p_peers (per-peer) because it's a property of how to
-- reach the machine, not of any one agent.

ALTER TABLE mesh_agents ADD COLUMN tmux_session TEXT NOT NULL DEFAULT '';
ALTER TABLE mesh_agents ADD COLUMN tmux_window TEXT NOT NULL DEFAULT '';
ALTER TABLE mesh_agents ADD COLUMN tmux_pane TEXT NOT NULL DEFAULT '';

ALTER TABLE p2p_peers ADD COLUMN ssh_target TEXT NOT NULL DEFAULT '';
