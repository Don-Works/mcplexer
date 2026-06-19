-- Layer 2 — Worker ModelProfile extraction.
--
-- Today each Worker row carries (model_provider, model_id,
-- model_endpoint_url, secret_scope_id) inline, so duplicating those four
-- fields across every Worker that talks to the same upstream is the only
-- way to reuse credentials. ModelProfile lifts that tuple into its own
-- table — a named, reusable bundle of (provider + endpoint + secret +
-- known model list) — and adds a nullable `model_profile_id` FK to
-- workers so existing rows continue to read the inline fields untouched
-- while new rows can point at a shared profile.
--
-- Trade-off: we keep the inline Worker columns rather than dropping them
-- in this migration. A second migration once the runner consults the
-- profile by default (and the inline fields become advisory) can prune
-- the dead columns. Until then the FK is purely additive — a Worker with
-- model_profile_id IS NULL behaves exactly as before.
--
-- known_models_json is a JSON array of model IDs the profile vouches for
-- (e.g. ["claude-opus-4-7","claude-sonnet-4-7"]). It's hint metadata for
-- the dashboard's model picker, not an allowlist — empty array means
-- "anything the provider accepts". `builtin=1` marks profiles the daemon
-- auto-creates (e.g. opencode-local in Layer 3); the admin layer refuses
-- to mutate them so a user can't accidentally delete the local default.

CREATE TABLE IF NOT EXISTS worker_model_profiles (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    provider          TEXT NOT NULL,            -- anthropic|openai|openai_compat|claude_cli
    endpoint_url      TEXT NOT NULL DEFAULT '',
    secret_scope_id   TEXT NULL REFERENCES auth_scopes(id) ON DELETE SET NULL,
    known_models_json TEXT NOT NULL DEFAULT '[]',
    builtin           INTEGER NOT NULL DEFAULT 0,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_model_profiles_name
    ON worker_model_profiles(name);

CREATE INDEX IF NOT EXISTS idx_worker_model_profiles_provider
    ON worker_model_profiles(provider);

-- Nullable FK on workers — existing rows continue to use inline fields.
ALTER TABLE workers ADD COLUMN model_profile_id TEXT NULL
    REFERENCES worker_model_profiles(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_workers_model_profile_id
    ON workers(model_profile_id)
    WHERE model_profile_id IS NOT NULL;
