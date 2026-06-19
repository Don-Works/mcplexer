-- M2.4 — local trust store for skill signers (ADR 0002).
--
-- Each row binds a 64-bit minisign key id (rendered as 16-char uppercase hex)
-- to the canonical 56-char public-key string and a human-readable label.
-- A non-NULL revoked_at flips the signer to "refuse new installs and warn"
-- without auto-uninstalling already-installed skills.

CREATE TABLE trusted_signers (
    pubkey_id     TEXT PRIMARY KEY,
    pubkey_string TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    added_at      TEXT NOT NULL,
    revoked_at    TEXT
);

CREATE INDEX idx_trusted_signers_active
    ON trusted_signers(revoked_at) WHERE revoked_at IS NULL;
