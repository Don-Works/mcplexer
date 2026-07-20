-- 145 — Expected-signal (absence) detection for Monitoring.
--
-- Every detector before this one is driven by log lines ARRIVING: lines →
-- templates → triage → incident → notify. Zero lines therefore produces zero
-- alerts, so "the orders integration ingested nothing all night" is
-- structurally unobservable. An expected-signal rule inverts the polarity:
-- source S is expected to produce at least min_count lines matching
-- match_substring inside window_seconds; failing that is an incident.
--
-- Evaluation is entirely deterministic (Go + SQL in the daemon). No model is
-- consulted — the AI worker is handed a pre-computed fact, never asked to
-- notice an absence, so this feature adds zero token load.
--
-- Absence and "we cannot see" are DIFFERENT incidents with different fixes.
-- The evaluator emits a collection-health outcome (distinct class_key) rather
-- than a confident-but-false "no orders!" whenever the source is disabled,
-- pulls are failing, or the source has gone entirely silent. Note that
-- log_sources.cursor_ts is a log WATERMARK, not pull health, and is
-- deliberately NOT used as a liveness proxy anywhere in this feature.

CREATE TABLE IF NOT EXISTS monitoring_expected_signals (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source_id                TEXT NOT NULL REFERENCES log_sources(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,

    -- Matching runs over retained log_lines, not templates: a template only
    -- exists once its shape has been seen, which is precisely the state an
    -- absence rule cannot assume. match_substring is a case-insensitive
    -- SUBSTRING (never a regex — no REGEXP function is registered, and a
    -- regex would put operator-authored backtracking on the ingest path).
    -- Empty match_substring means "any line from this source".
    match_substring          TEXT NOT NULL DEFAULT '',
    min_severity             TEXT NOT NULL DEFAULT '',
    min_count                INTEGER NOT NULL DEFAULT 1,
    window_seconds           INTEGER NOT NULL,
    severity                 TEXT NOT NULL DEFAULT 'error',

    -- Active schedule. A naive "0 in the last hour" fires at 3am on a source
    -- with genuinely low overnight volume, gets muted, and is then worse than
    -- no rule at all. active_days_mask is a bitmask over time.Weekday
    -- (bit 0 = Sunday … bit 6 = Saturday); active_start_minute /
    -- active_end_minute are minutes since local midnight in `timezone`
    -- (start > end wraps midnight, for nightly batch windows). The evaluator
    -- additionally refuses to fire until a FULL window has elapsed inside the
    -- current contiguous active period, so a 09:00-17:00 rule with a 6h window
    -- cannot fire at 09:05 on the strength of overnight quiet.
    timezone                 TEXT NOT NULL DEFAULT 'UTC',
    active_days_mask         INTEGER NOT NULL DEFAULT 127,
    active_start_minute      INTEGER NOT NULL DEFAULT 0,
    active_end_minute        INTEGER NOT NULL DEFAULT 1440,

    -- Collection-health guards. require_source_liveness suppresses an absence
    -- claim when the source produced no lines AT ALL in the window (we cannot
    -- distinguish "no orders" from "we lost the stream"); operators whose
    -- source logs nothing but the expected signal turn it off and rely on the
    -- failure counter alone. max_consecutive_failures is the RAISE threshold
    -- for a collection incident; any non-zero failure count below it merely
    -- makes the evaluation inconclusive and suppresses the absence claim.
    require_source_liveness  INTEGER NOT NULL DEFAULT 1,
    max_consecutive_failures INTEGER NOT NULL DEFAULT 3,
    enabled                  INTEGER NOT NULL DEFAULT 1,

    -- Evaluation state. last_signal_at is the bootstrap guard: a rule that has
    -- never observed its signal never fires an absence incident, so a fresh
    -- install cannot alert on a signal it has not yet learned to see.
    -- active_incident_id is the recovery latch, cleared the moment the signal
    -- returns; repeat evaluations converge on ONE incident per class rather
    -- than creating a new one every tick.
    last_evaluated_at        DATETIME NULL,
    last_signal_at           DATETIME NULL,
    last_outcome             TEXT NOT NULL DEFAULT '',
    last_raised_at           DATETIME NULL,
    last_recovered_at        DATETIME NULL,
    active_incident_id       TEXT NULL REFERENCES monitoring_incidents(id) ON DELETE SET NULL,

    created_at               DATETIME NOT NULL,
    updated_at               DATETIME NOT NULL,
    UNIQUE(workspace_id, source_id, name)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_expected_signals_workspace
    ON monitoring_expected_signals(workspace_id, name);

-- The evaluator's scheduling view: every enabled rule across all workspaces,
-- ordered so a tick is stable.
CREATE INDEX IF NOT EXISTS idx_monitoring_expected_signals_enabled
    ON monitoring_expected_signals(workspace_id, source_id)
    WHERE enabled = 1;

CREATE INDEX IF NOT EXISTS idx_monitoring_expected_signals_incident
    ON monitoring_expected_signals(active_incident_id);
