-- M1.2 — paired libp2p peers (p2p milestone).
--
-- Each row represents a peer that has completed the pairing flow with this
-- node. The 6-digit pairing code is intentionally NOT stored — it lives
-- in-memory in the daemon for its short TTL and is consumed on success.
--
-- p2p_peers
--   peer_id      libp2p PeerID string (e.g. "12D3Koo..."), unique
--   display_name human-friendly label (defaults to short peer ID prefix)
--   paired_at    when the pairing handshake completed
--   last_seen    most recent successful contact (NULL until first contact)
--   trust_level  0=basic, 1=trusted, 2=admin (room to grow)
--   scopes       JSON array of granted scope IDs (forward-compat hook)
--   revoked_at   NULL means active; non-NULL means revoked + ignored
--
-- Pending pair codes are stored in p2p_pending_pairs while the second leg
-- of the pairing handshake is in flight; row is deleted on consume/expiry.

CREATE TABLE IF NOT EXISTS p2p_peers (
    peer_id      TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    paired_at    TEXT NOT NULL,
    last_seen    TEXT,
    trust_level  INTEGER NOT NULL DEFAULT 0,
    scopes       TEXT NOT NULL DEFAULT '[]',
    revoked_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_p2p_peers_active
    ON p2p_peers(revoked_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS p2p_pending_pairs (
    code       TEXT PRIMARY KEY,
    peer_id    TEXT NOT NULL,
    multiaddrs TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_p2p_pending_pairs_expires_at
    ON p2p_pending_pairs(expires_at);
