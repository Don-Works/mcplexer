-- 061 — Per-workspace tasks.
--
-- Tasks are mcplexer's operational primitive for work-to-be-done. They
-- borrow patterns from migration 058 (memory): workspace scoping,
-- peer-offer/request via libp2p, FTS5 mirror, origin_peer_id
-- provenance. But unlike memory they are NOT bi-temporal — operational
-- state moves forward, not retroactively. Row-local history lives in
-- status_history_json so it survives independent of mesh-message
-- retention policy.
--
-- Surfaces:
--   - Agents over MCP (task__create / __list / __get / __update /
--     __assign / __delete / __append_note / __query_audit)
--   - Workers (registered in BuiltinToolCaller so workers can call
--     task__* via mcpx__execute_code)
--   - The dashboard ("Tasks" pages — phase 4)
--   - Peers via the new libp2p protocol /mcplexer/task/1.0.0 (phase 3)
--   - Mesh lifecycle events with kind='task_event' (phase 2)
--
-- Composition over inheritance: epics are tasks whose `meta` contains
-- `composes: [task:abc, task:def]`. No parent_id. The task__create tool
-- accepts a `compose_into?` arg that atomically appends the new task's
-- id to the parent's meta composition list.
--
-- Cross-peer pre-grants:
--   task_offer:<workspace> — can suggest tasks (pending until accepted)
--   task_assign:<workspace> — can land tasks directly (skips accept)
-- See internal/peerscope/registry.go.

CREATE TABLE tasks (
    id                       TEXT PRIMARY KEY,                 -- ulid
    workspace_id             TEXT NOT NULL,                    -- FK enforced by app; cascade on delete via Go (soft-delete)

    title                    TEXT NOT NULL,
    description              TEXT NOT NULL DEFAULT '',
    status                   TEXT NOT NULL DEFAULT 'open',     -- freeform; task_status_vocabulary names the terminal ones
    closed_at                INTEGER,                          -- set when status moves to a terminal (per workspace vocab)

    priority                 TEXT NOT NULL DEFAULT 'normal',   -- freeform: low|normal|high|critical suggested
    due_at                   INTEGER,                          -- unix seconds; null = no due date

    tags_json                TEXT NOT NULL DEFAULT '[]',
    meta                     TEXT NOT NULL DEFAULT '',         -- frontmatter; opaque, never parsed server-side

    -- Assignment (split origin so queries are cheap)
    assignee_session_id      TEXT,                              -- null = unassigned
    assignee_origin_kind     TEXT NOT NULL DEFAULT 'local',     -- 'local' | 'peer'
    assignee_peer_id         TEXT NOT NULL DEFAULT '',          -- non-empty when origin_kind='peer'
    assigned_by_session_id   TEXT,
    assigned_by_peer_id      TEXT NOT NULL DEFAULT '',
    assigned_at              INTEGER,

    -- Provenance (mirrors memory 058)
    source_kind              TEXT NOT NULL DEFAULT 'agent',     -- agent|worker|user|peer-import|system
    source_session_id        TEXT,
    source_tool_call_id      TEXT,
    created_by_session_id    TEXT,
    updated_by_session_id    TEXT,
    origin_peer_id           TEXT NOT NULL DEFAULT '',          -- non-empty if accepted from a peer offer

    -- Row-local audit: durable independent of mesh retention.
    -- Append-only JSON: [{at,by_session,by_peer,evt,from,to,note?}].
    status_history_json      TEXT NOT NULL DEFAULT '[]',

    pinned                   INTEGER NOT NULL DEFAULT 0,
    deleted_at               INTEGER,                           -- soft delete
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL
);

CREATE INDEX idx_tasks_workspace_open
    ON tasks(workspace_id, updated_at DESC)
    WHERE deleted_at IS NULL AND closed_at IS NULL;

CREATE INDEX idx_tasks_workspace_status
    ON tasks(workspace_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_tasks_assignee
    ON tasks(assignee_origin_kind, assignee_peer_id, assignee_session_id, closed_at)
    WHERE deleted_at IS NULL AND assignee_session_id IS NOT NULL;

CREATE INDEX idx_tasks_origin_peer
    ON tasks(origin_peer_id, updated_at DESC)
    WHERE deleted_at IS NULL AND origin_peer_id != '';

CREATE INDEX idx_tasks_due_at
    ON tasks(workspace_id, due_at)
    WHERE deleted_at IS NULL AND due_at IS NOT NULL;

CREATE INDEX idx_tasks_source_session
    ON tasks(source_session_id)
    WHERE deleted_at IS NULL AND source_session_id IS NOT NULL;

-- FTS5 mirror with json_each tag extraction (cleaner tokens than
-- memory's stringified-array trick; lets us filter by tag inside FTS).
CREATE VIRTUAL TABLE tasks_fts USING fts5(
    title,
    description,
    meta,
    tags,
    status,
    workspace_id UNINDEXED,
    id UNINDEXED,
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER tasks_ai AFTER INSERT ON tasks BEGIN
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

CREATE TRIGGER tasks_au AFTER UPDATE ON tasks BEGIN
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

CREATE TRIGGER tasks_ad AFTER DELETE ON tasks BEGIN
    DELETE FROM tasks_fts WHERE rowid = old.rowid;
END;

-- Per-workspace status vocabulary. Replaces a `status_terminal` column
-- on tasks themselves (which would have desynced). The cleanup skill
-- (task-status-consolidator, ships in phase 5) populates this table by
-- proposing merges; the user can also edit it directly.
CREATE TABLE task_status_vocabulary (
    workspace_id   TEXT NOT NULL,
    status_text    TEXT NOT NULL,
    is_terminal    INTEGER NOT NULL DEFAULT 0,
    display_color  TEXT,
    display_order  INTEGER NOT NULL DEFAULT 0,
    managed_by     TEXT NOT NULL DEFAULT 'user',         -- user|skill|system
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, status_text)
);

-- Notes — append-only so two agents touching the same task don't race
-- on the description blob.
CREATE TABLE task_notes (
    id                  TEXT PRIMARY KEY,                  -- ulid
    task_id             TEXT NOT NULL,
    author_session_id   TEXT,
    author_kind         TEXT NOT NULL DEFAULT 'agent',     -- mirrors source_kind
    body                TEXT NOT NULL,
    created_at          INTEGER NOT NULL
);
CREATE INDEX idx_task_notes_task
    ON task_notes(task_id, created_at DESC);

-- Cross-peer offers — mirrors memory_offers shape but adds direction
-- (incoming vs outgoing), envelope nonce (replay protection), is_direct_assign
-- and a uniqueness index that prevents a peer from re-offering the same
-- task under the same envelope.
CREATE TABLE task_offers (
    id                    TEXT PRIMARY KEY,                -- ulid
    task_id               TEXT,                            -- local task id once accepted; null while pending
    remote_task_id        TEXT NOT NULL,                   -- the sender's task id
    from_peer_id          TEXT NOT NULL,
    to_peer_id            TEXT NOT NULL,                   -- self when direction='incoming', remote when 'outgoing'
    remote_workspace_id   TEXT NOT NULL,
    remote_workspace_name TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT,                            -- chosen local workspace once accepted

    -- Preview only — full payload fetched via /mcplexer/task/1.0.0
    -- Request round-trip on accept. Caps protect against descriptor leak.
    title                 TEXT NOT NULL,
    description_preview   TEXT NOT NULL DEFAULT '',        -- ≤ 256 chars; service-level cap
    meta_preview          TEXT NOT NULL DEFAULT '',        -- ≤ 256 chars
    status_preview        TEXT NOT NULL DEFAULT '',
    priority_preview      TEXT NOT NULL DEFAULT '',
    tags_json             TEXT NOT NULL DEFAULT '[]',

    is_direct_assign      INTEGER NOT NULL DEFAULT 0,      -- true if sender invoked task__assign_remote with assign_task scope
    envelope_nonce        TEXT NOT NULL,                   -- replay protection
    envelope_created_at   INTEGER NOT NULL,                -- max-staleness check on accept
    direction             TEXT NOT NULL,                   -- 'incoming' | 'outgoing'
    state                 TEXT NOT NULL,                   -- pending|accepted|declined|expired|auto_accepted|rejected_throttle|rejected_unscoped
    accepted_at           INTEGER,
    declined_at           INTEGER,
    declined_reason       TEXT,
    created_at            INTEGER NOT NULL
);

CREATE UNIQUE INDEX uniq_task_offers
    ON task_offers(direction, from_peer_id, to_peer_id, remote_task_id, envelope_nonce);

CREATE INDEX idx_task_offers_pending
    ON task_offers(direction, state, created_at DESC);

-- One-time-per-pair workspace identity binding. The first time a peer
-- offers a task from one of their workspaces, the receiving agent picks
-- a local workspace; that choice is memoized here so all future offers
-- from the same peer→remote-workspace land in the bound local workspace.
CREATE TABLE workspace_peer_bindings (
    peer_id              TEXT NOT NULL,
    remote_workspace_id  TEXT NOT NULL,
    local_workspace_id   TEXT NOT NULL,
    remote_workspace_name TEXT NOT NULL DEFAULT '',
    established_at       INTEGER NOT NULL,
    PRIMARY KEY (peer_id, remote_workspace_id)
);

CREATE INDEX idx_workspace_peer_bindings_local
    ON workspace_peer_bindings(local_workspace_id);

-- Per-peer-per-workspace throttle window for direct-assign. Mirrors the
-- throttle pattern in worker_mesh_triggers; the dispatcher enforces by
-- checking last_assign_at vs the configured throttle on each incoming
-- envelope with is_direct_assign=true.
CREATE TABLE task_assign_throttles (
    peer_id           TEXT NOT NULL,
    workspace_id      TEXT NOT NULL,
    last_assign_at    INTEGER NOT NULL,
    count_in_window   INTEGER NOT NULL DEFAULT 0,
    window_started_at INTEGER NOT NULL,
    PRIMARY KEY (peer_id, workspace_id)
);
