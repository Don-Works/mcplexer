-- 138 — Bind task offers to stable workspace/principal authorization state.
-- Legacy offers remain visible for audit but have no share_id and therefore
-- cannot be used to fetch task content through the authenticated protocol.

ALTER TABLE task_offers ADD COLUMN share_id TEXT REFERENCES p2p_workspace_shares(share_id);
ALTER TABLE task_offers ADD COLUMN sender_principal_id TEXT REFERENCES p2p_principals(id);
ALTER TABLE task_offers ADD COLUMN access_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_offers ADD COLUMN visibility_epoch INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_task_offers_authorized_request
    ON task_offers(direction, to_peer_id, remote_task_id, envelope_nonce, share_id);
