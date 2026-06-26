-- 120 — A libp2p peer is one device, and a device has exactly one human owner.
--
-- Migration 028 documented that invariant, but the primary key was
-- (peer_id, user_id), which allowed stale synthetic users and later real
-- users to both claim one peer. Keep the newest owner row per peer (preferring
-- self if present), then enforce uniqueness on peer_id.

DELETE FROM peer_users
 WHERE rowid NOT IN (
   SELECT rowid FROM (
     SELECT pu.rowid,
            ROW_NUMBER() OVER (
              PARTITION BY pu.peer_id
              ORDER BY u.is_self DESC, u.created_at DESC, u.user_id DESC
            ) AS rn
       FROM peer_users pu
       JOIN users u ON u.user_id = pu.user_id
   )
   WHERE rn = 1
 );

CREATE UNIQUE INDEX IF NOT EXISTS idx_peer_users_one_owner
    ON peer_users(peer_id);
