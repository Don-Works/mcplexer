-- 080 — chat_turn_signals.
--
-- Per-turn feedback log for the concierge self-improvement loop
-- (epic 01KSGKFZMVFZRWVDSZMK8W9JN1). After the concierge emits a turn,
-- we classify the user's NEXT reply into {confirmation, correction,
-- frustration, redirect, escalation, neutral} and append one row here.
--
-- Downstream consumers:
--   - Friction extractor worker (B2) — polls for label IN ('correction',
--     'frustration'), reads the last 3 conversation turns, proposes a
--     refinement via skill__propose_refinement (W3).
--   - A/B telemetry (B4) — aggregates by (worker_id, prompt_version) to
--     pick the winning prompt candidate.
--   - Dashboard friction inbox — surfaces recent negative-signal turns
--     so an operator can spot patterns.
--
-- Why a dedicated table over a generic event log:
--   - The shape is stable + small (label + free-form notes, no JSON
--     payload soup) which makes the friction-extractor SQL simple.
--   - Per-user/channel/worker indices give the A/B aggregator cheap
--     "group by arm" queries.
--   - Forensic redaction: ForgetMemoryBySource has an analogue here for
--     session-poisoning recovery without touching memories or audit.
--
-- Scoping axes:
--   - worker_id   → which concierge produced the turn
--   - workspace_id → routing scope (e.g. the Telegram concierge ws)
--   - user_id_external → an opaque per-user identifier supplied by the
--     adapter ("telegram:12345" or "gchat:user@org.com"). NOT a foreign
--     key — these come from third-party platforms and we don't model
--     them as their own entity.
--   - channel     → "telegram", "gchat", "web", etc.
--   - prompt_version → the worker's prompt_template version active when
--     this turn was emitted. Lets the A/B telemetry layer slice signals
--     by arm without parsing prompt diffs.

CREATE TABLE chat_turn_signals (
    id                   TEXT PRIMARY KEY,            -- ulid
    worker_id            TEXT NOT NULL,
    workspace_id         TEXT NOT NULL,               -- denormalized for cheap scope filtering
    user_id_external     TEXT NOT NULL DEFAULT '',    -- opaque per-channel user id (e.g. "telegram:12345")
    channel              TEXT NOT NULL,               -- "telegram" | "gchat" | "web" | ...
    prompt_version       INTEGER NOT NULL DEFAULT 0,  -- 0 = unknown / pre-versioning
    turn_id              TEXT NOT NULL DEFAULT '',    -- worker_run id or mesh msg id of the turn being judged
    label                TEXT NOT NULL,               -- confirmation|correction|frustration|redirect|escalation|neutral
    user_message         TEXT NOT NULL DEFAULT '',    -- the reply text we classified (truncated to 2KB by writers)
    assistant_message    TEXT NOT NULL DEFAULT '',    -- the prior turn's text (truncated to 2KB by writers)
    confidence           REAL NOT NULL DEFAULT 0,     -- 0..1, classifier self-rated
    classifier_kind      TEXT NOT NULL DEFAULT 'rule', -- "rule" (heuristic) | "model" (small LLM)
    source_session_id    TEXT NOT NULL DEFAULT '',    -- writer session — enables forensic redaction
    created_at           INTEGER NOT NULL,            -- unix seconds
    promoted_to_refinement_id TEXT                    -- set by B2 when this signal feeds a skill_refinement
);

-- Primary access pattern: friction extractor "new negative signals since
-- last run". Partial index keeps the hot path tight (most signals are
-- neutral or confirmation; only correction/frustration drive refinement).
CREATE INDEX idx_chat_turn_signals_negative_new
    ON chat_turn_signals(worker_id, created_at DESC)
    WHERE label IN ('correction','frustration') AND promoted_to_refinement_id IS NULL;

-- A/B telemetry — "all signals on this arm" lookups.
CREATE INDEX idx_chat_turn_signals_arm
    ON chat_turn_signals(worker_id, prompt_version, label, created_at DESC);

-- Per-user calibration view (B5 memory pinning uses this to scope
-- lessons "concierge:<channel>:<user>").
CREATE INDEX idx_chat_turn_signals_user
    ON chat_turn_signals(channel, user_id_external, created_at DESC);

-- Forensic redaction symmetry with memory.
CREATE INDEX idx_chat_turn_signals_source_session
    ON chat_turn_signals(source_session_id)
    WHERE source_session_id <> '';
