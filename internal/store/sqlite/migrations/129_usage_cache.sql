-- migration 129: usage cache tables for the AI subscription dashboard.
-- usage_provider_cache holds one row per provider (claude, codex, etc.)
-- with a JSON-encoded ProviderSnapshot. usage_openrouter_cache holds
-- at most one row (id=1) with the OpenRouter-specific snapshot.

CREATE TABLE IF NOT EXISTS usage_provider_cache (
    provider    TEXT PRIMARY KEY,
    snapshot    TEXT NOT NULL DEFAULT '{}',
    updated_at  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS usage_openrouter_cache (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    snapshot    TEXT NOT NULL DEFAULT '{}',
    updated_at  TEXT NOT NULL DEFAULT ''
);
