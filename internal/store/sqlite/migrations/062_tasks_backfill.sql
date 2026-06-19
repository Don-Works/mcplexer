-- 062 — Self-healing backfill for migration 061.
--
-- Why: during Phase 1 iteration the agent restarted the daemon against an
-- early WIP version of 061_tasks.sql that recorded schema_version=61 in
-- schema_version but produced no tables (either the file was empty or
-- the transaction silently committed without DDL effect). Daemons that
-- ran that WIP cannot recover by re-applying 061 — the version row
-- locks them out.
--
-- This migration re-runs 061's intent with `IF NOT EXISTS` on every
-- DDL, so:
--   - fresh installs (where 061 created everything correctly) → no-op
--   - stuck installs (where 061 left the schema empty) → tables get
--     created here and task__* tools start working.
--
-- Everything below is a literal copy of 061's schema with `IF NOT
-- EXISTS` added. If you're touching the task schema, edit 061; this
-- file exists only to repair daemons that got caught mid-iteration.

CREATE TABLE IF NOT EXISTS tasks (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL,

    title                    TEXT NOT NULL,
    description              TEXT NOT NULL DEFAULT '',
    status                   TEXT NOT NULL DEFAULT 'open',
    closed_at                INTEGER,

    priority                 TEXT NOT NULL DEFAULT 'normal',
    due_at                   INTEGER,

    tags_json                TEXT NOT NULL DEFAULT '[]',
    meta                     TEXT NOT NULL DEFAULT '',

    assignee_session_id      TEXT,
    assignee_origin_kind     TEXT NOT NULL DEFAULT 'local',
    assignee_peer_id         TEXT NOT NULL DEFAULT '',
    assigned_by_session_id   TEXT,
    assigned_by_peer_id      TEXT NOT NULL DEFAULT '',
    assigned_at              INTEGER,

    source_kind              TEXT NOT NULL DEFAULT 'agent',
    source_session_id        TEXT,
    source_tool_call_id      TEXT,
    created_by_session_id    TEXT,
    updated_by_session_id    TEXT,
    origin_peer_id           TEXT NOT NULL DEFAULT '',

    status_history_json      TEXT NOT NULL DEFAULT '[]',

    pinned                   INTEGER NOT NULL DEFAULT 0,
    deleted_at               INTEGER,
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_workspace_open
    ON tasks(workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL AND closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_workspace_status
    ON tasks(workspace_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_assignee
    ON tasks(assignee_origin_kind, assignee_peer_id, assignee_session_id, closed_at)
    WHERE deleted_at IS NULL AND assignee_session_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_origin_peer
    ON tasks(origin_peer_id, updated_at DESC)
    WHERE deleted_at IS NULL AND origin_peer_id != '';

CREATE INDEX IF NOT EXISTS idx_tasks_due_at
    ON tasks(workspace_id, due_at)
    WHERE deleted_at IS NULL AND due_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_source_session
    ON tasks(source_session_id)
    WHERE deleted_at IS NULL AND source_session_id IS NOT NULL;

CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(
    title,
    description,
    meta,
    tags,
    status,
    workspace_id UNINDEXED,
    id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS tasks_ai AFTER INSERT ON tasks BEGIN
    INSERT INTO tasks_fts(rowid, title, description, meta, tags, status, workspace_id, id)
    VALUES (
        new.rowid,
        new.title,
        new.description,
        new.meta,
        (SELECT IFNULL(group_concat(value, ' '), '') FROM json_each(new.tags_json)),
        new.status,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER IF NOT EXISTS tasks_au AFTER UPDATE ON tasks BEGIN
    DELETE FROM tasks_fts WHERE rowid = old.rowid;
    INSERT INTO tasks_fts(rowid, title, description, meta, tags, status, workspace_id, id)
    VALUES (
        new.rowid,
        new.title,
        new.description,
        new.meta,
        (SELECT IFNULL(group_concat(value, ' '), '') FROM json_each(new.tags_json)),
        new.status,
        new.workspace_id,
        new.id
    );
END;

CREATE TRIGGER IF NOT EXISTS tasks_ad AFTER DELETE ON tasks BEGIN
    DELETE FROM tasks_fts WHERE rowid = old.rowid;
END;

CREATE TABLE IF NOT EXISTS task_status_vocabulary (
    workspace_id   TEXT NOT NULL,
    status_text    TEXT NOT NULL,
    is_terminal    INTEGER NOT NULL DEFAULT 0,
    display_color  TEXT,
    display_order  INTEGER NOT NULL DEFAULT 0,
    managed_by     TEXT NOT NULL DEFAULT 'user',
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, status_text)
);

CREATE TABLE IF NOT EXISTS task_notes (
    id                  TEXT PRIMARY KEY,
    task_id             TEXT NOT NULL,
    author_session_id   TEXT,
    author_kind         TEXT NOT NULL DEFAULT 'agent',
    body                TEXT NOT NULL,
    created_at          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_notes_task
    ON task_notes(task_id, created_at DESC);

CREATE TABLE IF NOT EXISTS task_offers (
    id                    TEXT PRIMARY KEY,
    task_id               TEXT,
    remote_task_id        TEXT NOT NULL,
    from_peer_id          TEXT NOT NULL,
    to_peer_id            TEXT NOT NULL,
    remote_workspace_id   TEXT NOT NULL,
    remote_workspace_name TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT,

    title                 TEXT NOT NULL,
    description_preview   TEXT NOT NULL DEFAULT '',
    meta_preview          TEXT NOT NULL DEFAULT '',
    status_preview        TEXT NOT NULL DEFAULT '',
    priority_preview      TEXT NOT NULL DEFAULT '',
    tags_json             TEXT NOT NULL DEFAULT '[]',

    is_direct_assign      INTEGER NOT NULL DEFAULT 0,
    envelope_nonce        TEXT NOT NULL,
    envelope_created_at   INTEGER NOT NULL,
    direction             TEXT NOT NULL,
    state                 TEXT NOT NULL,
    accepted_at           INTEGER,
    declined_at           INTEGER,
    declined_reason       TEXT,
    created_at            INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_task_offers
    ON task_offers(direction, from_peer_id, to_peer_id, remote_task_id, envelope_nonce);

CREATE INDEX IF NOT EXISTS idx_task_offers_pending
    ON task_offers(direction, state, created_at DESC);

CREATE TABLE IF NOT EXISTS workspace_peer_bindings (
    peer_id              TEXT NOT NULL,
    remote_workspace_id  TEXT NOT NULL,
    local_workspace_id   TEXT NOT NULL,
    remote_workspace_name TEXT NOT NULL DEFAULT '',
    established_at       INTEGER NOT NULL,
    PRIMARY KEY (peer_id, remote_workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_workspace_peer_bindings_local
    ON workspace_peer_bindings(local_workspace_id);

CREATE TABLE IF NOT EXISTS task_assign_throttles (
    peer_id           TEXT NOT NULL,
    workspace_id      TEXT NOT NULL,
    last_assign_at    INTEGER NOT NULL,
    count_in_window   INTEGER NOT NULL DEFAULT 0,
    window_started_at INTEGER NOT NULL,
    PRIMARY KEY (peer_id, workspace_id)
);
