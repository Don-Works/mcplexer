-- M0.1 — Workers: scheduled in-process AI agents.
--
-- A Worker is a configuration row (model + skill + prompt + cron + tool
-- allowlist + output channels). The scheduler fires it on its schedule;
-- the runner (M0.3) executes one WorkerRun per dispatch and routes the
-- output to the mesh.
--
-- SecretScopeID is a hard FK into auth_scopes (model API key). The skill
-- columns are nullable — a Worker can run with a pure prompt. The memory
-- scope is reserved for the future memory-system initiative; M0 only
-- stores it.
--
-- worker_runs is the ledger of executions. Rows survive Worker deletes on
-- purpose: when a Worker is recreated under the same name, the audit
-- history is still queryable by ID. parameters_json / tool_allowlist_json
-- / output_channels_json / mesh_message_ids_json / audit_record_ids_json
-- are JSON blobs Marshal/Unmarshalled in the Go layer.

CREATE TABLE IF NOT EXISTS workers (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    model_provider        TEXT NOT NULL,
    model_id              TEXT NOT NULL,
    model_endpoint_url    TEXT NOT NULL DEFAULT '',
    secret_scope_id       TEXT NOT NULL REFERENCES auth_scopes(id),
    skill_name            TEXT NOT NULL DEFAULT '',
    skill_version         TEXT NOT NULL DEFAULT '',
    prompt_template       TEXT NOT NULL DEFAULT '',
    parameters_json       TEXT NOT NULL DEFAULT '{}',
    schedule_spec         TEXT NOT NULL,
    tool_allowlist_json   TEXT NOT NULL DEFAULT '[]',
    output_channels_json  TEXT NOT NULL DEFAULT '[]',
    exec_mode             TEXT NOT NULL DEFAULT 'propose',
    concurrency_policy    TEXT NOT NULL DEFAULT 'skip',
    memory_scope_id       TEXT NULL,
    enabled               INTEGER NOT NULL DEFAULT 1,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- One Worker name per workspace. The unique index doubles as the lookup
-- index for GetWorkerByName.
CREATE UNIQUE INDEX IF NOT EXISTS idx_workers_workspace_name
    ON workers(workspace_id, name);

CREATE TABLE IF NOT EXISTS worker_runs (
    id                     TEXT PRIMARY KEY,
    worker_id              TEXT NOT NULL,
    started_at             DATETIME NOT NULL,
    finished_at            DATETIME NULL,
    duration_ms            INTEGER NOT NULL DEFAULT 0,
    status                 TEXT NOT NULL,
    prompt_rendered        TEXT NOT NULL DEFAULT '',
    model_provider         TEXT NOT NULL DEFAULT '',
    model_id               TEXT NOT NULL DEFAULT '',
    input_tokens           INTEGER NOT NULL DEFAULT 0,
    output_tokens          INTEGER NOT NULL DEFAULT 0,
    cost_usd               REAL NOT NULL DEFAULT 0,
    tool_calls_count       INTEGER NOT NULL DEFAULT 0,
    output_text            TEXT NOT NULL DEFAULT '',
    error                  TEXT NOT NULL DEFAULT '',
    mesh_message_ids_json  TEXT NOT NULL DEFAULT '[]',
    audit_record_ids_json  TEXT NOT NULL DEFAULT '[]'
);

-- Hot-path index for ListWorkerRuns (worker timeline) + the running-count
-- query (scheduler concurrency check).
CREATE INDEX IF NOT EXISTS idx_worker_runs_worker_started
    ON worker_runs(worker_id, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_worker_runs_worker_status
    ON worker_runs(worker_id, status);

-- Extend scheduled_jobs with the optional worker FK. When Kind='worker',
-- the scheduler reads this column and dispatches to the worker runner
-- instead of execing job.Command. Kept nullable so legacy
-- cron/interval/file_watch/git_hook rows are unaffected.
ALTER TABLE scheduled_jobs ADD COLUMN worker_id TEXT NULL;

CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_worker_id
    ON scheduled_jobs(worker_id)
    WHERE worker_id IS NOT NULL;
