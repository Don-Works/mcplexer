CREATE TABLE IF NOT EXISTS usage_snapshot_cache (
    cache_key TEXT PRIMARY KEY,
    window_days INTEGER NOT NULL CHECK (window_days BETWEEN 1 AND 365),
    snapshot_json BLOB NOT NULL CHECK (json_valid(snapshot_json)),
    generated_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_usage_snapshot_cache_updated
    ON usage_snapshot_cache(updated_at);
