-- 111 — mesh__push_skill v1: p2p skill PUSH (outbox/inbox + accept/reject).
--
-- Mirrors secret_offers (068) but for registry skills. Unlike secrets,
-- the skill body + tar.gz bundle are NOT carried inline (a single mesh
-- envelope is capped at 1 MiB — see p2p.MaxEnvelopeBytes). Instead the
-- push ships a tiny metadata-only OFFER; on accept the receiver pulls the
-- full body+bundle from the sender over the existing /mcplexer/
-- skill-registry/1.0.0 stream (no size cap) and publishes it locally.
--
-- direction:   'inbound'  (we received an offer, awaiting accept/reject)
--            | 'outbound' (we pushed an offer, awaiting peer decision)
-- status:      'pending' | 'accepted' | 'rejected' | 'expired'
--
-- Rows are kept after decision for audit + replay-detection (offer_id is
-- the dedup key). published_version records the local registry version
-- the accept produced (0 = not yet / not accepted).

CREATE TABLE IF NOT EXISTS skill_offers (
    offer_id          TEXT PRIMARY KEY,
    direction         TEXT NOT NULL,
    peer_id           TEXT NOT NULL,
    name              TEXT NOT NULL,
    version           INTEGER NOT NULL DEFAULT 0,
    content_hash      TEXT NOT NULL DEFAULT '',
    bundle_sha256     TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'pending',
    created_at        TEXT NOT NULL,
    decided_at        TEXT,
    expires_at        TEXT NOT NULL,
    published_version INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_skill_offers_pending
    ON skill_offers(direction, status, expires_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_skill_offers_peer
    ON skill_offers(peer_id, created_at);
