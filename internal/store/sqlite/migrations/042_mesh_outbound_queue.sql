-- M7.5 — offline-delivery queue for cross-machine mesh.
--
-- When Manager.Send fans a message to a paired peer and the libp2p dial
-- fails (peer offline, NAT-flap, daemon restart on the remote side), the
-- mesh layer parks the envelope here instead of returning an error to the
-- caller. The Reconnector's online-transition signal + a 30s background
-- sweeper drain the queue back through the same dispatch path.
--
-- Scope is per-target-peer, not broadcast. A `to_peer: "alice"` send that
-- fails queues for alice only. Audience=* broadcast sends never queue —
-- those are fire-and-forget by contract.
--
-- TTL defaults to 7 days; expired-undelivered rows get logged and pruned.

CREATE TABLE IF NOT EXISTS mesh_outbound_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL UNIQUE,
    target_peer_id TEXT NOT NULL,
    target_agent_session_id TEXT NOT NULL DEFAULT '',
    envelope BLOB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    enqueued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at DATETIME NULL,
    expires_at DATETIME NOT NULL
);

-- Hot-path index for "what's due for this peer right now". The partial
-- predicate keeps the index small once a peer has accumulated delivered
-- rows we haven't pruned yet.
CREATE INDEX IF NOT EXISTS mesh_outbound_queue_due_idx
    ON mesh_outbound_queue(target_peer_id, next_attempt_at)
    WHERE delivered_at IS NULL;

-- Secondary index used by the prune sweep (delivered rows older than 1d
-- + expired rows). Keeps the prune scan cheap as the table grows.
CREATE INDEX IF NOT EXISTS mesh_outbound_queue_prune_idx
    ON mesh_outbound_queue(delivered_at, expires_at);
