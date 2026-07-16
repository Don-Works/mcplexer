-- 139 — Local mappings for workspaces whose authority lives on another peer.
--
-- These rows are subscriptions, never grants. Cached capabilities help the
-- local UI and routing layer, but the home peer re-authorizes every operation.
-- Legacy workspace_peer_bindings are deliberately not copied.

CREATE TABLE p2p_workspace_memberships (
    share_id            TEXT PRIMARY KEY,
    home_peer_id        TEXT NOT NULL,
    remote_workspace_id TEXT NOT NULL,
    local_workspace_id  TEXT NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    workspace_name      TEXT NOT NULL,
    capabilities_json   TEXT NOT NULL DEFAULT '[]'
        CHECK (json_valid(capabilities_json) AND json_type(capabilities_json) = 'array'),
	access_epoch        INTEGER NOT NULL CHECK (access_epoch >= 1),
	cursor_hlc          TEXT NOT NULL DEFAULT '',
	status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    joined_at           INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    revoked_at          INTEGER,
    CHECK ((status = 'active' AND revoked_at IS NULL) OR
           (status = 'revoked' AND revoked_at IS NOT NULL))
);

CREATE INDEX idx_p2p_workspace_memberships_home
    ON p2p_workspace_memberships(home_peer_id, status);
