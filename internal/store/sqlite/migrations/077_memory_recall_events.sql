-- 077 — Memory recall-event log (AR4: learned recall weights).
--
-- WHY: today recall is stateless — the same query produces the same
-- ranking forever. Human associative memory does the opposite: memories
-- recalled together gain implicit weight ("things that fire together
-- wire together"). To replicate that we need a log of what surfaced
-- under what query, then a periodic aggregation step turns the log
-- into co-recall counts the recall path can use as a third axis next
-- to FTS5 + vec0.
--
-- POSTURE — anti-noise / opt-in:
--   - The TABLE always exists (no schema gymnastics for enable/disable).
--   - Recording is gated behind MCPLEXER_RECALL_TRACKING=1. Default OFF.
--   - Writes are async (buffered channel, dropped on overflow). Recall
--     never blocks on logging.
--   - Only the top-K rows of each recall result set are logged — the
--     long tail is noise, the head is signal.
--   - rank_position is recorded so the aggregator can weight by
--     position (top-1 has more meaning than top-10).
--   - Retention is a future concern (a background sweep dropping rows
--     older than N days), but the schema is wide enough today to
--     support it via the created_at index.

CREATE TABLE memory_recall_events (
    id              TEXT PRIMARY KEY,         -- ulid
    memory_id       TEXT NOT NULL,            -- the memory that surfaced
    session_id      TEXT,                     -- who triggered the recall
    workspace_id    TEXT,                     -- scope at the time of recall
    query           TEXT NOT NULL DEFAULT '', -- the FTS5/vec query (empty = list/browse)
    entity_filter   TEXT NOT NULL DEFAULT '', -- "kind:id" of the entity filter (empty = no entity scope)
    rank_position   INTEGER NOT NULL,         -- 1-indexed; 1 = top hit
    result_set_id   TEXT NOT NULL,            -- ulid grouping all rows surfaced in the same recall call
    source          TEXT NOT NULL DEFAULT 'rrf', -- fts | vec | rrf | list
    created_at      INTEGER NOT NULL,         -- unix seconds
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

-- Recall-by-memory: "every event that surfaced memory X". Powers the
-- co-recall aggregator AND the "this memory has been surfaced N times"
-- stat for the detail drawer.
CREATE INDEX idx_memory_recall_by_memory
    ON memory_recall_events(memory_id, created_at DESC);

-- Group-by result_set_id is the join the aggregator runs against itself
-- to compute co-recall pairs. Make that scan cheap.
CREATE INDEX idx_memory_recall_by_result_set
    ON memory_recall_events(result_set_id);

-- Workspace + time-range queries (dashboard analytics, future retention sweep).
CREATE INDEX idx_memory_recall_by_workspace_time
    ON memory_recall_events(workspace_id, created_at DESC);

-- Session-scoped lookups so forget_by_source can excise a poisoned
-- session's recall trail just like it excises memories.
CREATE INDEX idx_memory_recall_by_session
    ON memory_recall_events(session_id, created_at DESC)
    WHERE session_id IS NOT NULL;
