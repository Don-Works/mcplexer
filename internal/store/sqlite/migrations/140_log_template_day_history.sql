-- Persist per-template observed days beyond raw-line retention. The baseline
-- first_seen day is kept separately and is not used to infer cadence: older
-- pruned days cannot be reconstructed honestly during upgrade.
CREATE TABLE IF NOT EXISTS log_template_days (
    template_id  TEXT NOT NULL REFERENCES log_templates(id) ON DELETE CASCADE,
    observed_day TEXT NOT NULL,
    basis        TEXT NOT NULL DEFAULT 'observed'
                 CHECK (basis IN ('observed', 'first_seen_baseline')),
    PRIMARY KEY (template_id, observed_day)
);

CREATE INDEX IF NOT EXISTS idx_log_template_days_template_day
    ON log_template_days(template_id, observed_day);

-- Backfill every day still proven by retained lines.
INSERT OR IGNORE INTO log_template_days (template_id, observed_day, basis)
SELECT template_id, substr(ts, 1, 10), 'observed'
FROM log_lines
WHERE length(ts) >= 10;

-- Preserve the lifetime boundary without pretending intervening pruned days
-- were observed. If first_seen is still retained, the observed row wins.
INSERT OR IGNORE INTO log_template_days (template_id, observed_day, basis)
SELECT id, substr(first_seen, 1, 10), 'first_seen_baseline'
FROM log_templates
WHERE length(first_seen) >= 10;

CREATE TRIGGER IF NOT EXISTS trg_log_lines_track_template_day
AFTER INSERT ON log_lines
WHEN length(NEW.ts) >= 10
BEGIN
    INSERT INTO log_template_days (template_id, observed_day, basis)
    VALUES (NEW.template_id, substr(NEW.ts, 1, 10), 'observed')
    ON CONFLICT(template_id, observed_day) DO UPDATE SET basis = 'observed';
END;
