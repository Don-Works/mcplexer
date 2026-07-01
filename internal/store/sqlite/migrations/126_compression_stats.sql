-- Token-compression savings ledger. One rolled-up row per
-- (workspace_id, transform, UTC day), upserted from the gateway's
-- compression pipeline on every measured tool result. Lets the dashboard
-- show per-transform OBSERVED savings (would-be in shadow/dry-run mode,
-- applied in on mode) that survive daemon restarts. The gateway handler is
-- per-connection and ephemeral, so this table is also the cross-connection
-- aggregation point (the in-memory ContextCostStats only sees one socket).
--
-- Matching schema invariant (ensureCompressionStats) creates this idempotently
-- on every boot, covering branch swaps / partially-restored backups.

CREATE TABLE IF NOT EXISTS compression_stats (
    workspace_id        TEXT    NOT NULL DEFAULT '',
    transform           TEXT    NOT NULL,
    day                 TEXT    NOT NULL,
    lossless            INTEGER NOT NULL DEFAULT 0,
    samples             INTEGER NOT NULL DEFAULT 0,
    changed             INTEGER NOT NULL DEFAULT 0,
    orig_bytes          INTEGER NOT NULL DEFAULT 0,
    would_save_bytes    INTEGER NOT NULL DEFAULT 0,
    would_save_tokens   INTEGER NOT NULL DEFAULT 0,
    applied             INTEGER NOT NULL DEFAULT 0,
    applied_save_bytes  INTEGER NOT NULL DEFAULT 0,
    applied_save_tokens INTEGER NOT NULL DEFAULT 0,
    updated_at          TEXT    NOT NULL,
    PRIMARY KEY (workspace_id, transform, day)
);

CREATE INDEX IF NOT EXISTS idx_compression_stats_day ON compression_stats(day);
