-- Skill capability enforcement (M2.3).
--
-- 1) audit_records gains a nullable skill_id column so audit rows produced by
--    a tool call dispatched from inside a skill can be linked back to it.
--    Legacy rows leave skill_id NULL; sqlite stores text NULL transparently.
ALTER TABLE audit_records ADD COLUMN skill_id TEXT;
CREATE INDEX idx_audit_skill ON audit_records (skill_id) WHERE skill_id IS NOT NULL;

-- 2) skill_invocations records every tool call attempt made under a skill
--    context, with allow/deny outcome. Decoupled from audit_records so the
--    UI can render a per-skill timeline without joining on a sparse column.
CREATE TABLE skill_invocations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    skill_name  TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    namespace   TEXT NOT NULL,
    allowed     INTEGER NOT NULL,
    ts          INTEGER NOT NULL
);
CREATE INDEX idx_skill_invocations_skill ON skill_invocations (skill_name, ts);
CREATE INDEX idx_skill_invocations_allowed ON skill_invocations (allowed, ts);
