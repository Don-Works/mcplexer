-- 078 — Task attachments index.
--
-- Lets agents (and humans, via future REST + UI in C2.3/C2.4) attach
-- files to tasks. The row in this table is the *index*; the actual
-- bytes live on disk under
--
--   $MCPLEXER_DATA_DIR/attachments/<workspace_id>/<task_id>/<sha256>
--
-- (default $MCPLEXER_DATA_DIR is ~/.mcplexer). Content-addressed by
-- sha256 so the same file uploaded twice within a task dedupes to one
-- on-disk blob — multiple rows can share a storage_path. The DB layer
-- doesn't enforce uniqueness across rows because (a) two different
-- filenames pointing at the same content is a legitimate use case
-- (renames, copy-with-note) and (b) the cascade soft-delete shouldn't
-- ever orphan the file under a different row.
--
-- WHY filesystem over BLOB:
--   - keeps mcplexer.db lean (attachments are the largest entity)
--   - easy to back up separately (rsync the attachments dir; the db
--     index points at content-addressed names that are stable under
--     full-data-dir restore)
--   - content readable by external tools (search, virus scan, etc.)
--   - no extra read amplification on `SELECT *` of a task list
--
-- Soft-delete via deleted_at so the audit trail survives. The file
-- on disk is kept until a future GC sweep (out of scope for C2.1)
-- because two attachments may share the same sha256.
--
-- Cascade story (extends DeleteWorkspace, see workspace.go):
--   - workspace soft-delete -> soft-delete attachments belonging to
--     any non-deleted task in that workspace (mirrors the tasks
--     cascade added by migration 061).
--   - hard-delete of a workspace isn't supported by mcplexer's API
--     surface; soft-delete is the contract.

CREATE TABLE task_attachments (
    id                   TEXT PRIMARY KEY,            -- ulid
    task_id              TEXT NOT NULL,               -- FK -> tasks.id (soft-FK, enforced by service layer)
    workspace_id         TEXT NOT NULL,               -- denormalized for cheap workspace scoping
    filename             TEXT NOT NULL DEFAULT '',    -- caller-supplied; sanitized on write
    mime_type            TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes           INTEGER NOT NULL,            -- bytes on disk (post-decode for base64 uploads)
    sha256               TEXT NOT NULL,               -- hex(sha256(content))
    storage_path         TEXT NOT NULL,               -- path under data dir, e.g. "attachments/<ws>/<task>/<sha>"
    uploader_session_id  TEXT,                        -- nullable: a worker/peer may not have a session id
    uploader_kind        TEXT NOT NULL DEFAULT 'agent', -- agent|worker|user|peer-import|system (mirrors task.source_kind)
    created_at           INTEGER NOT NULL,            -- unix seconds
    deleted_at           INTEGER                      -- unix seconds (soft delete)
);

-- Primary access pattern: "all attachments for this task, newest first".
CREATE INDEX idx_task_attachments_task_created
    ON task_attachments(task_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Workspace-scoped sweep: workspace cascade soft-delete + dashboard
-- usage counters.
CREATE INDEX idx_task_attachments_workspace
    ON task_attachments(workspace_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Content-address lookup: powers within-task dedupe and future GC.
CREATE INDEX idx_task_attachments_sha256
    ON task_attachments(sha256)
    WHERE deleted_at IS NULL;
