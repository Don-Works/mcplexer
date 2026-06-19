-- 074 — Skill telemetry runs (W2).
--
-- One row per agent invocation of a registry skill. Captures the
-- lifecycle (start → phases → complete) for downstream consumption by:
--   * the dashboard (recent-runs panel, success rate, duration histogram)
--   * the refinement loop (W3) which uses outcome + tools_used as the
--     A/B signal for promote-or-discard decisions on candidate skill
--     versions
--   * the composition graph (W6) which traces which skills call which
--     downstream tools and which other skills they spawn as sub-runs
--
-- Append-only by design: phase events accumulate in phases_json rather
-- than mutating in place, so refinement (W3) can spot "phase X
-- restarted N times before completing" as a friction signal — that's
-- lost if we update-by-key.
--
-- task_epic_id is nullable. When the agent passes `phases` to
-- skill__run_start AND no epic id is provided AND a TasksService is
-- wired, the handler auto-creates a task epic + per-phase child rows
-- so the run is visible + resumable in the task dashboard mid-flight.
-- When phases aren't declared upfront (freeform), we still record
-- everything in this table; the task tree is purely a UI affordance.
--
-- Indexes target the two dominant query shapes:
--   * skill detail page: "last N runs of skill X" → (skill_name, started_at DESC)
--   * dashboard "what's been running": "last N runs in workspace W"
--     → (workspace_id, started_at DESC)

CREATE TABLE IF NOT EXISTS skill_runs (
    id                TEXT PRIMARY KEY,
    skill_name        TEXT NOT NULL,
    skill_version     INTEGER NOT NULL,
    workspace_id      TEXT NOT NULL,
    started_at        TEXT NOT NULL,
    completed_at      TEXT,
    outcome           TEXT NOT NULL DEFAULT 'running',
    phases_json       TEXT NOT NULL DEFAULT '[]',
    tools_used_json   TEXT NOT NULL DEFAULT '[]',
    task_epic_id      TEXT,
    agent_session_id  TEXT,
    metadata_json     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_skill_runs_skill_started
    ON skill_runs(skill_name, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_skill_runs_workspace_started
    ON skill_runs(workspace_id, started_at DESC);
