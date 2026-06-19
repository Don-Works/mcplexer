-- M7.1 — Per-human user identity.
--
-- The p2p_peers table tracks per-MACHINE identity (one row per device). This
-- migration introduces a per-HUMAN identity layer that sits ABOVE that: a
-- single user (Max, Alice, Bob) may operate multiple machines, each with its
-- own libp2p peer ID, but only one logical user row.
--
-- users
--   user_id      UUID v4 primary key (stable across machines + restarts)
--   display_name human-friendly label (mirrored from settings for self;
--                supplied by the remote during pairing for non-self users)
--   created_at   when the row was created
--   is_self      exactly one row has is_self=1: the local user. Enforced via
--                a partial unique index below (SQLite supports those since
--                3.8).
--
-- peer_users
--   join table linking p2p_peers (per-machine) to users (per-human). One peer
--   row maps to exactly one user; one user may map to many peers (multi-
--   machine support — Max's laptop + workstation become one user).

CREATE TABLE IF NOT EXISTS users (
    user_id      TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    is_self      INTEGER NOT NULL DEFAULT 0
);

-- Enforce: at most one is_self=1 row. Partial-unique-index is the SQLite
-- idiom for "unique among rows where condition".
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_is_self
    ON users(is_self) WHERE is_self = 1;

CREATE TABLE IF NOT EXISTS peer_users (
    peer_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    PRIMARY KEY (peer_id, user_id),
    FOREIGN KEY (peer_id) REFERENCES p2p_peers(peer_id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(user_id)     ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_peer_users_user_id ON peer_users(user_id);
CREATE INDEX IF NOT EXISTS idx_peer_users_peer_id ON peer_users(peer_id);
