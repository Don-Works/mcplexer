-- 136 — Authenticated P2P principals and explicit workspace authorization.
--
-- Transport peers, durable people/machines, principal signing keys, devices,
-- invitations, workspace grants, and task publication policy are deliberately
-- separate objects. Legacy peer/user/link rows are labels for migration only:
-- no legacy scope or workspace link becomes a grant.

CREATE TABLE p2p_principals (
    id                       TEXT PRIMARY KEY,
    kind                     TEXT NOT NULL CHECK (kind IN ('person', 'machine')),
    display_name             TEXT NOT NULL,
    status                   TEXT NOT NULL CHECK (status IN ('pending', 'active', 'legacy_unverified', 'revoked')),
    controlling_principal_id TEXT REFERENCES p2p_principals(id),
    is_local_owner           INTEGER NOT NULL DEFAULT 0 CHECK (is_local_owner IN (0, 1)),
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL,
    activated_at             INTEGER,
    revoked_at               INTEGER,
    revocation_reason        TEXT NOT NULL DEFAULT '',
    CHECK (is_local_owner = 0 OR kind = 'person'),
    CHECK ((status = 'revoked' AND revoked_at IS NOT NULL) OR
           (status != 'revoked' AND revoked_at IS NULL))
);

CREATE UNIQUE INDEX idx_p2p_principals_local_owner
    ON p2p_principals(is_local_owner) WHERE is_local_owner = 1;
CREATE INDEX idx_p2p_principals_status
    ON p2p_principals(kind, status, display_name);

CREATE TABLE p2p_principal_keys (
    id                    TEXT PRIMARY KEY,
    principal_id          TEXT NOT NULL REFERENCES p2p_principals(id),
    canonical_public_key  TEXT NOT NULL,
    fingerprint           TEXT NOT NULL UNIQUE,
    algorithm             TEXT NOT NULL CHECK (algorithm = 'ssh-ed25519'),
    status                TEXT NOT NULL CHECK (status IN ('pending', 'active', 'revoked')),
    replaces_key_id       TEXT REFERENCES p2p_principal_keys(id),
    comment               TEXT NOT NULL DEFAULT '',
    added_by_principal_id TEXT REFERENCES p2p_principals(id),
    created_at            INTEGER NOT NULL,
    verified_at           INTEGER,
    revoked_at            INTEGER,
    CHECK ((status = 'revoked' AND revoked_at IS NOT NULL) OR
           (status != 'revoked' AND revoked_at IS NULL))
);

CREATE INDEX idx_p2p_principal_keys_principal
    ON p2p_principal_keys(principal_id, created_at DESC);

CREATE TABLE p2p_principal_devices (
    id                      TEXT PRIMARY KEY,
    peer_id                 TEXT NOT NULL UNIQUE,
    principal_id            TEXT NOT NULL REFERENCES p2p_principals(id),
    identity_key_id         TEXT REFERENCES p2p_principal_keys(id),
    display_name            TEXT NOT NULL DEFAULT '',
    kind                    TEXT NOT NULL DEFAULT 'unknown' CHECK (kind IN ('laptop', 'server', 'daemon', 'unknown')),
    status                  TEXT NOT NULL CHECK (status IN ('active', 'legacy_unverified', 'revoked')),
    binding_version         TEXT NOT NULL DEFAULT '',
    binding_transcript_hash TEXT NOT NULL DEFAULT '',
    binding_signature       BLOB NOT NULL DEFAULT X'',
    created_at              INTEGER NOT NULL,
    verified_at             INTEGER,
    revoked_at              INTEGER,
    revocation_reason       TEXT NOT NULL DEFAULT '',
    CHECK ((status = 'active' AND identity_key_id IS NOT NULL AND verified_at IS NOT NULL AND revoked_at IS NULL) OR
           (status = 'legacy_unverified' AND identity_key_id IS NULL AND verified_at IS NULL AND revoked_at IS NULL) OR
           (status = 'revoked' AND revoked_at IS NOT NULL))
);

CREATE INDEX idx_p2p_principal_devices_principal
    ON p2p_principal_devices(principal_id, created_at DESC);
CREATE INDEX idx_p2p_principal_devices_key
    ON p2p_principal_devices(identity_key_id) WHERE identity_key_id IS NOT NULL;

CREATE TABLE p2p_workspace_shares (
    share_id           TEXT PRIMARY KEY,
    local_workspace_id TEXT NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    home_peer_id       TEXT NOT NULL,
    owner_principal_id TEXT NOT NULL REFERENCES p2p_principals(id),
    status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    access_epoch       INTEGER NOT NULL DEFAULT 1 CHECK (access_epoch >= 1),
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    revoked_at         INTEGER,
    CHECK ((status = 'revoked' AND revoked_at IS NOT NULL) OR
           (status = 'active' AND revoked_at IS NULL))
);

CREATE INDEX idx_p2p_workspace_shares_owner
    ON p2p_workspace_shares(owner_principal_id, status);

CREATE TABLE p2p_principal_invitations (
    id                      TEXT PRIMARY KEY,
    token_hash              BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    purpose                 TEXT NOT NULL CHECK (purpose IN ('new_principal', 'add_device', 'rotate_key')),
    principal_id            TEXT NOT NULL REFERENCES p2p_principals(id),
    identity_key_id         TEXT NOT NULL REFERENCES p2p_principal_keys(id),
    created_by_principal_id TEXT NOT NULL REFERENCES p2p_principals(id),
    created_at              INTEGER NOT NULL,
    expires_at              INTEGER NOT NULL,
    consumed_at             INTEGER,
    consumed_by_peer_id     TEXT NOT NULL DEFAULT '',
    revoked_at              INTEGER,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL),
    CHECK (consumed_at IS NULL OR consumed_by_peer_id != '')
);

CREATE INDEX idx_p2p_principal_invitations_pending
    ON p2p_principal_invitations(expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE p2p_identity_challenges (
    id                TEXT PRIMARY KEY,
    invitation_id     TEXT NOT NULL REFERENCES p2p_principal_invitations(id) ON DELETE CASCADE,
    initiator_peer_id TEXT NOT NULL,
    responder_peer_id TEXT NOT NULL,
    nonce_hash         BLOB NOT NULL CHECK (length(nonce_hash) = 32),
    transcript_hash    TEXT NOT NULL CHECK (length(transcript_hash) = 64),
    issued_at          INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    consumed_at        INTEGER,
    CHECK (expires_at > issued_at)
);

CREATE INDEX idx_p2p_identity_challenges_pending
    ON p2p_identity_challenges(invitation_id, expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE p2p_invitation_grants (
    invitation_id  TEXT NOT NULL REFERENCES p2p_principal_invitations(id) ON DELETE CASCADE,
    share_id       TEXT NOT NULL REFERENCES p2p_workspace_shares(share_id) ON DELETE CASCADE,
    capability     TEXT NOT NULL CHECK (capability IN (
        'workspace.view', 'tasks.read', 'tasks.create', 'tasks.publish',
        'tasks.comment', 'tasks.edit', 'tasks.assign', 'tasks.share',
        'tasks.delete', 'evidence.read', 'mesh.read', 'mesh.send',
        'worker.trigger', 'workspace.admin'
    )),
    constraints_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(constraints_json) AND json_type(constraints_json) = 'object'),
    expires_at      INTEGER,
    PRIMARY KEY (invitation_id, share_id, capability)
);

CREATE TABLE p2p_workspace_grants (
    id                      TEXT PRIMARY KEY,
    share_id                TEXT NOT NULL REFERENCES p2p_workspace_shares(share_id) ON DELETE CASCADE,
    principal_id            TEXT NOT NULL REFERENCES p2p_principals(id),
    capability              TEXT NOT NULL CHECK (capability IN (
        'workspace.view', 'tasks.read', 'tasks.create', 'tasks.publish',
        'tasks.comment', 'tasks.edit', 'tasks.assign', 'tasks.share',
        'tasks.delete', 'evidence.read', 'mesh.read', 'mesh.send',
        'worker.trigger', 'workspace.admin'
    )),
    constraints_json        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(constraints_json) AND json_type(constraints_json) = 'object'),
    created_by_principal_id TEXT NOT NULL REFERENCES p2p_principals(id),
    granted_epoch           INTEGER NOT NULL CHECK (granted_epoch >= 1),
    created_at              INTEGER NOT NULL,
    expires_at              INTEGER,
    revoked_at              INTEGER
);

CREATE UNIQUE INDEX idx_p2p_workspace_grants_active
    ON p2p_workspace_grants(share_id, principal_id, capability)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_p2p_workspace_grants_principal
    ON p2p_workspace_grants(principal_id, share_id, revoked_at, expires_at);

CREATE TABLE p2p_workspace_policies (
    share_id                  TEXT PRIMARY KEY REFERENCES p2p_workspace_shares(share_id) ON DELETE CASCADE,
    default_visibility        TEXT NOT NULL DEFAULT 'private' CHECK (default_visibility IN ('private', 'workspace')),
    agent_visibility_ceiling  TEXT NOT NULL DEFAULT 'private' CHECK (agent_visibility_ceiling IN ('private', 'restricted', 'workspace')),
    widening_requires_approval INTEGER NOT NULL DEFAULT 1 CHECK (widening_requires_approval IN (0, 1)),
    egress_profile            TEXT NOT NULL DEFAULT 'task-safe-v1',
    allow_remote_evidence     INTEGER NOT NULL DEFAULT 0 CHECK (allow_remote_evidence IN (0, 1)),
    updated_by_principal_id   TEXT NOT NULL REFERENCES p2p_principals(id),
    created_at                INTEGER NOT NULL,
    updated_at                INTEGER NOT NULL
);

CREATE TABLE p2p_collaboration_audit (
    id                 TEXT PRIMARY KEY,
    share_id           TEXT REFERENCES p2p_workspace_shares(share_id) ON DELETE SET NULL,
    event              TEXT NOT NULL,
    actor_principal_id TEXT REFERENCES p2p_principals(id),
    actor_peer_id      TEXT NOT NULL DEFAULT '',
    subject_kind       TEXT NOT NULL,
    subject_id         TEXT NOT NULL,
    details_json       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json) AND json_type(details_json) = 'object'),
    created_at         INTEGER NOT NULL
);

CREATE INDEX idx_p2p_collaboration_audit_share
    ON p2p_collaboration_audit(share_id, created_at DESC);
CREATE INDEX idx_p2p_collaboration_audit_subject
    ON p2p_collaboration_audit(subject_kind, subject_id, created_at DESC);

-- Existing per-human rows are only locally authoritative when is_self=1.
-- Remote IDs were self-reported during the old pairing flow, so they remain
-- migration labels and cannot authorize anything.
INSERT INTO p2p_principals (
    id, kind, display_name, status, is_local_owner,
    created_at, updated_at, activated_at
)
SELECT
    user_id,
    'person',
    display_name,
    CASE WHEN is_self = 1 THEN 'active' ELSE 'legacy_unverified' END,
    is_self,
    COALESCE(CAST(strftime('%s', created_at) AS INTEGER), unixepoch()),
    COALESCE(CAST(strftime('%s', created_at) AS INTEGER), unixepoch()),
    CASE WHEN is_self = 1 THEN COALESCE(CAST(strftime('%s', created_at) AS INTEGER), unixepoch()) ELSE NULL END
FROM users;

-- Keep the old peer-to-user association as an unverified device label. The
-- proof flow may later upgrade the same row atomically; no grant is copied.
INSERT INTO p2p_principal_devices (
    id, peer_id, principal_id, identity_key_id, display_name, kind, status,
    binding_version, binding_transcript_hash, binding_signature,
    created_at, verified_at, revoked_at, revocation_reason
)
SELECT
    'legacy:' || p.peer_id,
    p.peer_id,
    pu.user_id,
    NULL,
    p.display_name,
    'unknown',
    CASE WHEN p.revoked_at IS NULL THEN 'legacy_unverified' ELSE 'revoked' END,
    '', '', X'',
    COALESCE(CAST(strftime('%s', p.paired_at) AS INTEGER), unixepoch()),
    NULL,
    CASE WHEN p.revoked_at IS NULL THEN NULL ELSE COALESCE(CAST(strftime('%s', p.revoked_at) AS INTEGER), unixepoch()) END,
    CASE WHEN p.revoked_at IS NULL THEN '' ELSE 'legacy peer was revoked before principal migration' END
FROM peer_users pu
JOIN p2p_peers p ON p.peer_id = pu.peer_id;
