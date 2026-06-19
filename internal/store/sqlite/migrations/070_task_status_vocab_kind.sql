-- 070 — Categorize freeform task status vocabulary.
--
-- Status text is freeform per workspace, but the UI and the auto-claim
-- service-layer logic need a small canonical bucket vocabulary —
-- "is this an `open`-ish status?", "an in-progress one?", "blocked?",
-- "done?" — without hardcoding the six suggested defaults
-- (open/doing/blocked/review/done/cancelled). Each agent / human is
-- free to coin `triaging`, `coding`, `paused`, `awaiting_review`, …
-- and declare which bucket the new word belongs to so the rest of the
-- system keeps working without a code change.
--
-- New column:
--   kind TEXT NOT NULL DEFAULT 'open' — enum: open | working | blocked
--                                       | done | cancelled
--
-- The column is added unconditionally with `ADD COLUMN ... DEFAULT 'open'`.
-- That's safe — every existing row will get `'open'`, and the seed
-- UPDATEs below promote the six suggested defaults to their true bucket
-- everywhere they exist. New rows that never declare a kind continue to
-- default to `'open'` which is the conservative answer (won't trigger
-- auto-claim, won't render as a working/closed chip).
--
-- Filed by task 01KSG65XAE0S2JG9GKW9SN8QKQ (dogfood finding #7).
-- See internal/store/sqlite/migrate.go schemaInvariants for the
-- self-heal that re-adds this column if schema_version got bumped past
-- 070 without the ALTER applying (mirrors the workspace_id pattern from
-- migration 063 / ensureWorkerRunsWorkspaceID).

ALTER TABLE task_status_vocabulary ADD COLUMN kind TEXT NOT NULL DEFAULT 'open';

-- Seed the six suggested defaults across every workspace that has
-- already declared them (terminal-flag inference path, claim path,
-- consolidator path). Workspaces that never declared these defaults
-- get nothing here — and the service layer + cleanup skill continue to
-- populate the vocab lazily.
UPDATE task_status_vocabulary SET kind = 'open'      WHERE status_text = 'open';
UPDATE task_status_vocabulary SET kind = 'working'   WHERE status_text = 'doing';
UPDATE task_status_vocabulary SET kind = 'blocked'   WHERE status_text = 'blocked';
UPDATE task_status_vocabulary SET kind = 'blocked'   WHERE status_text = 'review';
UPDATE task_status_vocabulary SET kind = 'done'      WHERE status_text = 'done';
UPDATE task_status_vocabulary SET kind = 'cancelled' WHERE status_text = 'cancelled';

CREATE INDEX IF NOT EXISTS idx_task_status_vocab_kind
    ON task_status_vocabulary(workspace_id, kind);
