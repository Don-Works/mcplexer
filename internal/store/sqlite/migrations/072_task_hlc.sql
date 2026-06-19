-- 072 — Per-row Hybrid Logical Clock for task gossip.
--
-- The cross-peer gossip protocol (/mcplexer/task-sync/1.0.0) replays
-- task mutations using `hlc_at` as the watermark — each peer asks
-- "send me every task in workspace X with hlc_at > <my_max>". A
-- monotonic per-row HLC stamp gives that watermark without dragging
-- the inherently-fuzzy wall clock into the consistency story.
--
-- Wire format: fixed-width 32-char lowercase hex string (see
-- internal/clock/hlc.go) — first 16 chars = wall_ms, last 16 = counter.
-- The string sorts lexicographically by HLC order so SQLite's ORDER BY
-- + watermark comparison are pure string ops, no decode required.
--
-- Backfill: every existing task gets a derived stamp computed from its
-- updated_at so newly-added gossip subscribers don't see every old row
-- as "newer than the watermark" on first sync. Counter is 0 across the
-- backfill — order between two rows with identical updated_at is
-- arbitrary but stable (they sort by hex string).
--
-- This migration is paired with a Go-side fixup in clock.go that owns
-- forward stamp generation. Reading hlc_at on rows that pre-date this
-- migration is safe — string compare doesn't care that the value came
-- from a deterministic backfill rather than a live HLC tick.

ALTER TABLE tasks ADD COLUMN hlc_at TEXT NOT NULL DEFAULT '';

UPDATE tasks
SET hlc_at = printf('%016x%016x', updated_at * 1000, 0)
WHERE hlc_at = '';

CREATE INDEX idx_tasks_workspace_hlc
    ON tasks(workspace_id, hlc_at)
    WHERE deleted_at IS NULL;

-- Convert tasks.meta from legacy frontmatter text to JSON-shaped text.
-- The Go post-migration hook rewrites existing rows; this SQL adds the
-- generated column/index used by meta_match queries on composed_by.
ALTER TABLE tasks ADD COLUMN meta_composed_by TEXT
    GENERATED ALWAYS AS (
        CASE
            WHEN NOT json_valid(meta) THEN NULL
            WHEN json_type(meta, '$.composed_by') = 'array'
                THEN json_extract(meta, '$.composed_by[0]')
            ELSE json_extract(meta, '$.composed_by')
        END
    ) VIRTUAL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_composed_by
    ON tasks(meta_composed_by)
    WHERE meta_composed_by IS NOT NULL;
