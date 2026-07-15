-- 135 — log_lines FK/cascade hardening + rate-spike hysteresis state.
--
-- Migration 128 gave log_templates a FOREIGN KEY on source_id
-- (ON DELETE CASCADE) but left log_lines with plain TEXT source_id /
-- template_id columns and no FK at all. Deleting a log_source cascades
-- log_templates but stranded log_lines for that source, orphaned until
-- the next PruneLogLines age/byte sweep happened to catch them. SQLite
-- has no ALTER TABLE ... ADD CONSTRAINT, so the fix is the standard
-- rebuild: recreate log_lines with FKs on both source_id and
-- template_id (ON DELETE CASCADE), copying over only rows whose source
-- AND template still exist today — any pre-existing orphans (from
-- hosts/sources deleted before this migration shipped) are dropped in
-- the same pass instead of tripping the new constraint. log_lines has
-- no other table referencing it, so this rebuild is safe to run inside
-- the single migration transaction (PRAGMA foreign_keys stays ON the
-- whole time; the connection-level pragma can't be toggled mid-tx
-- anyway — see sqlite.go).
--
-- error_spike_active is the distiller's rate-spike hysteresis latch
-- (internal/logwatch/distill): 0 until a sustained error/critical rate
-- fires a spike notification, then 1 until a later evaluation finds
-- the rate back under threshold — so a chronic spike notifies once
-- and a recovered-then-reoffending source re-arms cleanly.

DROP INDEX IF EXISTS idx_log_lines_source_ts;

CREATE UNIQUE INDEX IF NOT EXISTS idx_log_templates_source_id
    ON log_templates(source_id, id);

CREATE TABLE log_lines_new (
    source_id   TEXT NOT NULL REFERENCES log_sources(id) ON DELETE CASCADE,
    template_id TEXT NOT NULL,
    ts          DATETIME NOT NULL,
    line        TEXT NOT NULL,
    FOREIGN KEY (source_id, template_id)
        REFERENCES log_templates(source_id, id) ON DELETE CASCADE
);

INSERT INTO log_lines_new (source_id, template_id, ts, line)
SELECT l.source_id, l.template_id, l.ts, l.line
FROM log_lines l
WHERE EXISTS (SELECT 1 FROM log_sources s WHERE s.id = l.source_id)
  AND EXISTS (
      SELECT 1 FROM log_templates t
      WHERE t.id = l.template_id AND t.source_id = l.source_id
  );

DROP TABLE log_lines;
ALTER TABLE log_lines_new RENAME TO log_lines;

CREATE INDEX IF NOT EXISTS idx_log_lines_source_ts ON log_lines(source_id, ts);
CREATE INDEX IF NOT EXISTS idx_log_lines_template ON log_lines(template_id);

ALTER TABLE log_sources ADD COLUMN error_spike_active INTEGER NOT NULL DEFAULT 0;
