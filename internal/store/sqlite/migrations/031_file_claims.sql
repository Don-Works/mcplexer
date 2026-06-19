-- M7.4: file_claims — beam-crossing prevention via advisory path-glob claims.
-- Each row is a single claim made by an agent (local or peer) over a set of
-- path globs in a given repo+branch. Claims are advisory (no enforcement),
-- expire automatically, and can be released early.
--
-- claimer_user_id and claimer_peer_id are mutually informative: when M7.1
-- (users) is wired, claimer_user_id holds the cross-machine user identity.
-- claimer_peer_id holds the libp2p peer ID that announced the claim and is
-- always populated for cross-machine claims.
CREATE TABLE file_claims (
    claim_id TEXT PRIMARY KEY,
    claimer_user_id TEXT NOT NULL DEFAULT '',
    claimer_peer_id TEXT NOT NULL DEFAULT '',
    claimer_display_name TEXT NOT NULL DEFAULT '',
    repo TEXT NOT NULL,
    branch TEXT NOT NULL,
    paths_json TEXT NOT NULL,
    intent TEXT NOT NULL DEFAULT '',
    claimed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    released_at TEXT NULL
);

CREATE INDEX idx_file_claims_active
    ON file_claims(repo, branch, expires_at)
    WHERE released_at IS NULL;

CREATE INDEX idx_file_claims_claimer
    ON file_claims(claimer_user_id, claimer_peer_id);
