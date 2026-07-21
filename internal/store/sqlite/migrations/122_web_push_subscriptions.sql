-- 122 — Standards-based Web Push subscriptions for the PWA.
--
-- VAPID keys are protocol signing keys generated locally by the daemon.
-- They are not a vendor account or third-party service credential. Keep them
-- out of exported settings so config export cannot leak push-signing material.

CREATE TABLE IF NOT EXISTS web_push_vapid (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    public_key TEXT NOT NULL,
    private_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS web_push_subscriptions (
    endpoint TEXT PRIMARY KEY,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL DEFAULT '',
    device_label TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_success_at DATETIME NULL,
    last_error_at DATETIME NULL,
    last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS web_push_subscriptions_enabled_idx
    ON web_push_subscriptions(enabled, updated_at DESC);

