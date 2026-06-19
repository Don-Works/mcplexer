-- 078 — JSON1 expression indices on the hot tasks.meta paths.
--
-- Migration 072 made tasks.meta a JSON object stored as TEXT and gave
-- composed_by a virtual generated column + index. But the production
-- query shape (see metaMatchSQL in internal/store/sqlite/task.go) ORs
-- the scalar-equality branch with an array-containment EXISTS subquery
-- so the planner can match either shape uniformly. SQLite's planner
-- requires EACH branch of an OR to be independently indexable before
-- it'll split the lookup with the "OR-by-union" optimisation; without
-- expression indices on the scalar-equality branch, the EXISTS branch
-- forces a full SCAN of tasks even for hot keys.
--
-- This migration adds:
--   * Scalar partial indices on json_extract(meta, '$.<key>') for the
--     keys that the agent fleet hits dozens of times per session:
--     touches_files (multi-agent file-overlap coordination), branch,
--     worktree, pr, linear, mesh_thread, source_mesh_msg_id.
--   * composes also gets one — agents do occasionally meta_match on it
--     for "find children of this epic" even though the canonical shape
--     is an array.
--
-- Partial-index WHERE shape: `json_valid(meta) AND json_extract(...) IS
-- NOT NULL`. The json_valid guard is REQUIRED — pre-072 frontmatter
-- rows that haven't been touched by a service-level write yet still
-- carry the legacy `key: value` text. Without the guard, json_extract
-- raises "malformed JSON (1)" at index-build time and the whole CREATE
-- INDEX fails. With the guard, those rows simply fall out of the index
-- (they were never going to match a meta_match filter anyway — see
-- TestListTasksMetaFiltersAgainstLegacyFrontmatter).
--
-- The index EXPRESSION is the same json_extract — SQLite needs it for
-- the column-equality lookup. SQLite never evaluates the expression on
-- rows excluded by the partial-index WHERE, so the json_valid guard in
-- WHERE is enough to keep CREATE INDEX safe.
--
-- Why partial-on-json_extract is enough (and json_each is NOT in the
-- index): json_each is a TVF, not an expression — SQLite expression
-- indices want a single deterministic expression per row. For the
-- array branch we rely on the planner to short-circuit via the
-- json_type='array' guard: rows where json_type is 'array' fall out
-- of the partial index naturally (their scalar json_extract returns
-- the array itself, which doesn't equal the bound scalar), so the OR
-- branches stay logically correct.
--
-- ANALYZE is required at the end so the planner has stats to prefer
-- these indices over the broad workspace+updated_at index for the
-- meta_match path. Without ANALYZE the planner falls back to size
-- heuristics that don't see the partial-index selectivity.

CREATE INDEX IF NOT EXISTS idx_tasks_meta_branch
    ON tasks(json_extract(meta, '$.branch'))
    WHERE json_valid(meta) AND json_extract(meta, '$.branch') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_worktree
    ON tasks(json_extract(meta, '$.worktree'))
    WHERE json_valid(meta) AND json_extract(meta, '$.worktree') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_pr
    ON tasks(json_extract(meta, '$.pr'))
    WHERE json_valid(meta) AND json_extract(meta, '$.pr') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_linear
    ON tasks(json_extract(meta, '$.linear'))
    WHERE json_valid(meta) AND json_extract(meta, '$.linear') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_mesh_thread
    ON tasks(json_extract(meta, '$.mesh_thread'))
    WHERE json_valid(meta) AND json_extract(meta, '$.mesh_thread') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_source_mesh_msg_id
    ON tasks(json_extract(meta, '$.source_mesh_msg_id'))
    WHERE json_valid(meta) AND json_extract(meta, '$.source_mesh_msg_id') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_composes
    ON tasks(json_extract(meta, '$.composes'))
    WHERE json_valid(meta) AND json_extract(meta, '$.composes') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_meta_touches_files
    ON tasks(json_extract(meta, '$.touches_files'))
    WHERE json_valid(meta) AND json_extract(meta, '$.touches_files') IS NOT NULL;

-- Crucial: without ANALYZE the planner sees no stats for the new
-- indices and falls back to the broader workspace index. With it the
-- partial-index selectivity wins for the scalar-equality branch of
-- the meta_match OR.
ANALYZE;
