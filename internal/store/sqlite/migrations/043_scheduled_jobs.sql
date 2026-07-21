-- M0-A — Schedule Guard catalog.
--
-- One row per scheduled job mcplexer is responsible for. The scheduler tick
-- selects jobs whose enabled=1 AND next_run_at <= now and runs them through
-- the same decision_chain as any other guarded surface.
--
-- `survive_daemon_down=1` rows get a second life as a native systemd timer
-- or launchd label in M3; until then the in-process scheduler is the only
-- driver and native_driver/native_id stay empty.
--
-- args_json/env_json are JSON-encoded by the caller (the Go layer
-- Marshal/Unmarshals at the boundary). Counters + last_* columns are
-- updated by the runner after each attempt.

CREATE TABLE IF NOT EXISTS scheduled_jobs (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    spec                 TEXT NOT NULL,
    command              TEXT NOT NULL,
    args_json            TEXT NOT NULL DEFAULT '[]',
    env_json             TEXT NOT NULL DEFAULT '{}',
    cwd                  TEXT NOT NULL DEFAULT '',
    surface              TEXT NOT NULL DEFAULT 'schedule',
    enabled              INTEGER NOT NULL DEFAULT 1,
    survive_daemon_down  INTEGER NOT NULL DEFAULT 0,
    native_driver        TEXT NOT NULL DEFAULT '',
    native_id            TEXT NOT NULL DEFAULT '',
    last_run_at          DATETIME NULL,
    next_run_at          DATETIME NULL,
    last_status          TEXT NOT NULL DEFAULT '',
    last_error           TEXT NOT NULL DEFAULT '',
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Hot-path index for the scheduler tick: "which enabled jobs are due now?".
-- Partial predicate keeps the index small once disabled jobs accumulate.
CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_enabled_next
    ON scheduled_jobs(enabled, next_run_at)
    WHERE enabled = 1;

-- Secondary index for listing/filtering by kind (cron, interval, file_watch,
-- git_hook). Used by the admin UI + audit queries.
CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_kind
    ON scheduled_jobs(kind);
