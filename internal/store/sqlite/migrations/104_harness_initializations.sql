-- 104 — Harness initialization tracking + bootstrap content-hash receipts.
--
-- Small table to record last_initialize_at + clientInfo from MCP
-- initialize.clientInfo (mapped to stable harness keys: claude, codex,
-- opencode, gemini, grok).
--
-- Also holds bootstrap receipts (installed flag, version, content_hash of
-- the rendered using-mcplexer artifact written for that harness, registry
-- version, drifted flag). Drift is (re)computed by harness-sync recheck
-- by hashing the live on-disk artifact(s) and comparing to stored hash.
--
-- Enables the GET /api/v1/setup/status contract and 'mcplexer harness sync'.

CREATE TABLE IF NOT EXISTS harness_initializations (
    key                 TEXT PRIMARY KEY,
    last_initialize_at  DATETIME NULL,
    client_info         TEXT NULL,
    bootstrap_installed INTEGER NOT NULL DEFAULT 0,
    bootstrap_version   INTEGER NULL,
    bootstrap_hash      TEXT NOT NULL DEFAULT '',
    registry_version    INTEGER NOT NULL DEFAULT 0,
    drifted             INTEGER NOT NULL DEFAULT 0,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_harness_init_updated
    ON harness_initializations(updated_at);
