-- 117 — Memory conflict queue.
--
-- (Renumbered 116->117 during the workspace-cmd-center/audit consolidation:
-- the audit branch's 116_audit_search had already been applied to the shared
-- daemon DB as version 116, so this migration moved to 117. Made idempotent
-- with IF NOT EXISTS because the table can already exist on a DB that applied
-- the original 116 before the renumber.)
--
-- When a NOTE is saved, the post-write neighbour scan
-- (memory.Service.surfaceContradictions) may flag existing memories as
-- possible duplicates or conflicts. Those flags were previously advisory and
-- ephemeral (returned in the save response + a signal event, then lost). This
-- table persists them so the dashboard can offer a "conflicts to review"
-- queue with explicit resolution (supersede / keep-both / dismiss), instead of
-- the contradiction signal vanishing the moment the save call returns.
--
-- Denormalised (names + preview captured at write time) so the queue renders
-- without joins and degrades gracefully if a side is later deleted.

CREATE TABLE IF NOT EXISTS memory_conflicts (
    id                TEXT PRIMARY KEY,          -- ULID
    memory_id         TEXT NOT NULL,             -- the NEW note that triggered the scan
    memory_name       TEXT NOT NULL DEFAULT '',
    candidate_id      TEXT NOT NULL,             -- the existing memory flagged
    candidate_name    TEXT NOT NULL DEFAULT '',
    candidate_preview TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL,             -- 'duplicate' | 'related'
    reason            TEXT NOT NULL DEFAULT '',
    workspace_id      TEXT,                      -- NULL = global scope
    created_at        INTEGER NOT NULL,          -- unix seconds
    resolved_at       INTEGER,                   -- NULL = open
    resolution        TEXT                       -- 'superseded' | 'kept_both' | 'dismissed'
);

-- Open-queue read path: newest open conflicts first.
CREATE INDEX IF NOT EXISTS idx_memory_conflicts_open
    ON memory_conflicts(resolved_at, created_at DESC);

-- One open row per (new note, candidate) pair so a re-save doesn't duplicate
-- an already-pending conflict.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_memory_conflict_open_pair
    ON memory_conflicts(memory_id, candidate_id)
    WHERE resolved_at IS NULL;
