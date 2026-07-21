-- 068 — mesh__send_secret v1 (v0.13.0).
--
-- Two changes:
--
-- (1) Add `secret_transfer_recipient` to p2p_peers. Stores the age X25519
--     recipient string (`age1...`) the local daemon has learned for each
--     paired peer via the `peer_identity` mesh broadcast event. Empty
--     string until first announce arrives — mesh__send_secret refuses to
--     send to a peer with an empty recipient.
--
-- (2) Create `secret_offers` table. Tracks inbound secret transfer
--     requests staged for agent approval. Outbound rows track delivery
--     status for the sender's view.
--
-- direction:   'inbound' (we received it, awaiting accept/reject)
--            | 'outbound' (we sent it, awaiting peer decision)
-- status:      'pending' | 'accepted' | 'rejected' | 'expired'
--            | 'delivered' (outbound only — peer confirmed receipt
--                          before deciding)
--
-- Ciphertext rows are kept after decision for audit + replay-detection;
-- the plaintext is NEVER stored here. On accept, the plaintext is moved
-- into the existing auth_scopes secrets store under a name the receiver
-- chooses.

ALTER TABLE p2p_peers ADD COLUMN secret_transfer_recipient TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS secret_offers (
    offer_id      TEXT PRIMARY KEY,
    direction     TEXT NOT NULL,
    peer_id       TEXT NOT NULL,
    name          TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    ciphertext    BLOB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    TEXT NOT NULL,
    decided_at    TEXT,
    expires_at    TEXT NOT NULL,
    saved_as      TEXT
);

CREATE INDEX IF NOT EXISTS idx_secret_offers_pending
    ON secret_offers(direction, status, expires_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_secret_offers_peer
    ON secret_offers(peer_id, created_at);
