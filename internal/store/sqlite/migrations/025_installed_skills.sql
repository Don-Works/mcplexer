-- M2.2 — registry of installed .mcskill bundles.
--
-- Each row tracks a skill that has been unpacked under {data_dir}/skills/<name>/.
-- The full manifest is preserved verbatim in manifest_json so that the UI / CLI
-- can render capability summaries without re-reading the on-disk manifest.toml.
--
-- name is the primary key — only one version of a given skill may be installed
-- at a time. Re-installing a skill with the same name is rejected at the
-- application layer with ErrSkillAlreadyInstalled.
--
-- signer_pubkey holds the canonical 56-char minisign public key string of the
-- signer (matches trusted_signers.pubkey_string). Empty when the bundle was
-- installed unsigned (only allowed via an explicit operator override).
--
-- source records where the bundle came from: "file:<abs-path>" for a local
-- install or the raw "https://..." URL for a network install.

CREATE TABLE installed_skills (
    name           TEXT PRIMARY KEY,
    version        TEXT NOT NULL,
    manifest_json  TEXT NOT NULL,
    signer_pubkey  TEXT NOT NULL DEFAULT '',
    source         TEXT NOT NULL DEFAULT '',
    installed_at   INTEGER NOT NULL
);

CREATE INDEX idx_installed_skills_signer
    ON installed_skills(signer_pubkey)
    WHERE signer_pubkey != '';
