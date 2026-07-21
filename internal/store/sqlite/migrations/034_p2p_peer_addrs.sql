-- M1.5 — persist last-known libp2p multiaddrs per paired peer.
--
-- The DHT-backed Reconnector (be7a652) recovers paired peers across IP
-- changes by walking the Kademlia DHT once every 30s. On a fresh daemon
-- restart that means the first 30 seconds after boot have no addrs in the
-- peerstore, so a paired peer that's already reachable on a known LAN/VPN
-- address has to wait for the DHT to converge.
--
-- This migration adds a per-peer cache of the most recent direct (non-
-- relay) multiaddrs we observed via the DiscoveryService. On boot we hydrate
-- the libp2p peerstore from this column so the Reconnector's first
-- iteration has something to dial immediately.
--
-- Stored as a JSON array of multiaddr strings. Empty when we've never seen
-- the peer reach us via a non-relay path.
--
-- Defensive about migration 024 ordering (mirrors the connection_mode hook
-- in migrate.go): if the column already exists (e.g. test fixtures), the
-- ALTER is skipped by the post-migration Go hook.

CREATE TABLE IF NOT EXISTS p2p_peers (
    peer_id      TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    paired_at    TEXT NOT NULL,
    last_seen    TEXT,
    trust_level  INTEGER NOT NULL DEFAULT 0,
    scopes       TEXT NOT NULL DEFAULT '[]',
    revoked_at   TEXT
);

-- Note: the last_known_addrs column is added (idempotently) by the Go post-
-- migration hook ensureP2PLastKnownAddrs in migrate.go. Adding it directly
-- here would error out on databases where the table already exists from
-- M1.2 / M1.3 without the column.
