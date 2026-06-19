-- M1.3 — LAN auto-discovery via mDNS.
--
-- This migration is defensive about M1.2 (the pairing flow) potentially not
-- having merged yet: if `p2p_peers` does not yet exist, we create a minimal
-- shape compatible with M1.2's expected schema. If it already exists, the
-- CREATE is a no-op and the post-migration Go hook (ensureP2PConnectionMode)
-- adds the connection_mode column on its own.
--
-- connection_mode tracks how this node currently reaches the peer:
--   'direct'        — same LAN (mDNS-learned addrs reachable)
--   'hole-punched'  — DCUtR succeeded over Internet
--   'relay'         — circuit-v2 relay leg in use
-- NULL means unknown / never connected since process start.

CREATE TABLE IF NOT EXISTS p2p_peers (
    id              TEXT PRIMARY KEY,
    peer_id         TEXT NOT NULL UNIQUE,
    label           TEXT NOT NULL DEFAULT '',
    paired_at       TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at    TEXT,
    connection_mode TEXT
);

CREATE INDEX IF NOT EXISTS idx_p2p_peers_peer_id ON p2p_peers(peer_id);

-- Note: the connection_mode index is created by the Go post-hook so it can
-- only run AFTER the column has been ensured to exist (the table may have
-- been created by M1.2 without that column).
