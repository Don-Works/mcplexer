-- 141 — A share_id is globally stable and may refer either to a workspace
-- hosted here or to an explicit p2p_workspace_membership hosted elsewhere.
-- Migration 138's local-share foreign key therefore rejected legitimate
-- outgoing publisher offers. Rebuild the table without that incorrect FK;
-- authorization continues to validate the reference against one of the two
-- typed stores before every wire operation.

CREATE TABLE task_offers_v140 (
    id                    TEXT PRIMARY KEY,
    task_id               TEXT,
    remote_task_id        TEXT NOT NULL,
    from_peer_id          TEXT NOT NULL,
    to_peer_id            TEXT NOT NULL,
    remote_workspace_id   TEXT NOT NULL,
    remote_workspace_name TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT,
    title                 TEXT NOT NULL,
    description_preview   TEXT NOT NULL DEFAULT '',
    meta_preview          TEXT NOT NULL DEFAULT '',
    status_preview        TEXT NOT NULL DEFAULT '',
    priority_preview      TEXT NOT NULL DEFAULT '',
    tags_json             TEXT NOT NULL DEFAULT '[]',
    is_direct_assign      INTEGER NOT NULL DEFAULT 0,
    envelope_nonce        TEXT NOT NULL,
    envelope_created_at   INTEGER NOT NULL,
    direction             TEXT NOT NULL,
    state                 TEXT NOT NULL,
    accepted_at           INTEGER,
    declined_at           INTEGER,
    declined_reason       TEXT,
    created_at            INTEGER NOT NULL,
    share_id              TEXT,
    sender_principal_id   TEXT REFERENCES p2p_principals(id),
    access_epoch          INTEGER NOT NULL DEFAULT 0,
    visibility_epoch      INTEGER NOT NULL DEFAULT 0
);

INSERT INTO task_offers_v140 (
    id, task_id, remote_task_id, from_peer_id, to_peer_id,
    remote_workspace_id, remote_workspace_name, workspace_id,
    title, description_preview, meta_preview, status_preview,
    priority_preview, tags_json, is_direct_assign, envelope_nonce,
    envelope_created_at, direction, state, accepted_at, declined_at,
    declined_reason, created_at, share_id, sender_principal_id,
    access_epoch, visibility_epoch
)
SELECT
    id, task_id, remote_task_id, from_peer_id, to_peer_id,
    remote_workspace_id, remote_workspace_name, workspace_id,
    title, description_preview, meta_preview, status_preview,
    priority_preview, tags_json, is_direct_assign, envelope_nonce,
    envelope_created_at, direction, state, accepted_at, declined_at,
    declined_reason, created_at, share_id, sender_principal_id,
    access_epoch, visibility_epoch
FROM task_offers;

DROP TABLE task_offers;
ALTER TABLE task_offers_v140 RENAME TO task_offers;

CREATE UNIQUE INDEX uniq_task_offers
    ON task_offers(direction, from_peer_id, to_peer_id, remote_task_id, envelope_nonce);
CREATE INDEX idx_task_offers_pending
    ON task_offers(direction, state, created_at DESC);
CREATE INDEX idx_task_offers_authorized_request
    ON task_offers(direction, to_peer_id, remote_task_id, envelope_nonce, share_id);
