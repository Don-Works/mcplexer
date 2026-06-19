-- 093_brain_candidate_suppress.sql — sticky per-record proactive-memory
-- suppression.
--
-- When the operator dismisses a proactive memory candidate with "never",
-- the gateway writes a sticky suppression keyed on (record_id, content_hash)
-- so the same candidate never re-fires for that record. The browser's
-- POST /api/v1/brain/record/{id}/suppress-candidate endpoint lands here.
-- A blank content_hash suppresses ALL candidates for the record (the
-- coarse "stop suggesting on this record" choice).
--
-- This table is NOT index-rebuildable (it records a human decision, not a
-- derived fact), so it is the one brain-adjacent table that carries
-- authoritative state. Timestamps are Unix epoch INTEGER seconds.

CREATE TABLE IF NOT EXISTS brain_candidate_suppressions (
    record_id     TEXT NOT NULL,        -- the task/memory id the candidate fired on
    content_hash  TEXT NOT NULL,        -- sha of the candidate text; "" = suppress all
    created_at    INTEGER NOT NULL,     -- unix seconds
    PRIMARY KEY (record_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_brain_candidate_suppress_record
    ON brain_candidate_suppressions(record_id);
