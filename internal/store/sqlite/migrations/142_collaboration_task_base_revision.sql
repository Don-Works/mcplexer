-- 142 — Optimistic base revision for edits published from a workspace mirror.
-- The mirror keeps the last home HLC it actually observed; both offer phases
-- carry it so the home rejects a stale full-snapshot edit instead of silently
-- overwriting an intervening canonical change.

ALTER TABLE tasks ADD COLUMN remote_base_hlc TEXT NOT NULL DEFAULT '';
ALTER TABLE task_offers ADD COLUMN base_hlc TEXT NOT NULL DEFAULT '';
