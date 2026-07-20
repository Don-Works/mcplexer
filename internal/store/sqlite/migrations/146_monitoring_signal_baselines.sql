-- 146 — Learned baselines for expected-signal (absence) detection.
--
-- Migration 145 gave the daemon a correct absence evaluator and no way to get a
-- rule into it. The operator's position is that a rule will never be authored:
-- "no user is gonna describe those alerts - you should just infer them from the
-- logs + operations of the system, what does normal look like." This table is
-- where that inference is recorded.
--
-- The learner mines retained log_lines for templates that arrive at a stable
-- cadence — the fingerprint of a cron/scheduled job — and promotes only the
-- confident ones into monitoring_expected_signals. It is plain statistics
-- (median, MAD, p95) over rows the daemon already holds: no model, no
-- embeddings, no prompt, no extra worker wake-up.
--
-- REJECTIONS ARE STORED, not just promotions. A learned rule nobody can
-- interrogate is a rule nobody trusts, and "why is there no alert for this job"
-- is the question operators actually ask. Every candidate the learner looked at
-- keeps its decision code, its human-readable reason, and the exact statistics
-- the decision was made from, so any promotion can be re-derived by hand.
--
-- rule_id is also the OWNERSHIP marker. A monitoring_expected_signals row with
-- a baseline pointing at it is learner-owned and may be adapted; anything else
-- is the operator's and is never rewritten. ON DELETE SET NULL means deleting a
-- learned rule leaves its evidence behind for the next pass to reconsider,
-- rather than erasing the record of what the system had concluded.

CREATE TABLE IF NOT EXISTS monitoring_signal_baselines (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source_id            TEXT NOT NULL REFERENCES log_sources(id) ON DELETE CASCADE,
    -- No FK to log_templates: a template pruned out from under a live baseline
    -- must not silently delete the record of what we learned from it.
    template_id          TEXT NOT NULL,
    rule_id              TEXT NULL REFERENCES monitoring_expected_signals(id) ON DELETE SET NULL,

    masked               TEXT NOT NULL DEFAULT '',
    match_substring      TEXT NOT NULL DEFAULT '',

    decision             TEXT NOT NULL,
    reason               TEXT NOT NULL DEFAULT '',

    -- Evidence. Stored as the raw statistics rather than a single blended
    -- score so the threshold ladder stays auditable: a candidate can be strong
    -- on one axis and disqualified on another, and both facts survive here.
    period_seconds       REAL NOT NULL DEFAULT 0,
    p95_seconds          REAL NOT NULL DEFAULT 0,
    mad_seconds          REAL NOT NULL DEFAULT 0,
    relative_mad         REAL NOT NULL DEFAULT 0,
    p95_ratio            REAL NOT NULL DEFAULT 0,
    sample_count         INTEGER NOT NULL DEFAULT 0,
    cycles_observed      REAL NOT NULL DEFAULT 0,
    hour_occupancy       REAL NOT NULL DEFAULT 0,
    span_seconds         REAL NOT NULL DEFAULT 0,
    confidence           REAL NOT NULL DEFAULT 0,

    -- The rule shape this candidate implies, kept even on rejection so an
    -- operator can see what WOULD have been created.
    window_seconds       INTEGER NOT NULL DEFAULT 0,
    active_start_minute  INTEGER NOT NULL DEFAULT 0,
    active_end_minute    INTEGER NOT NULL DEFAULT 1440,

    -- scan_truncated records that the per-source line budget clipped the
    -- history examined, so span_seconds is a floor and not the whole truth.
    scan_truncated       INTEGER NOT NULL DEFAULT 0,

    first_seen           DATETIME NULL,
    last_seen            DATETIME NULL,
    observed_at          DATETIME NOT NULL,
    learned_runs         INTEGER NOT NULL DEFAULT 0,
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL,

    UNIQUE(template_id)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_signal_baselines_workspace
    ON monitoring_signal_baselines(workspace_id, decision, confidence DESC);

CREATE INDEX IF NOT EXISTS idx_monitoring_signal_baselines_source
    ON monitoring_signal_baselines(source_id, decision);

CREATE INDEX IF NOT EXISTS idx_monitoring_signal_baselines_rule
    ON monitoring_signal_baselines(rule_id);
