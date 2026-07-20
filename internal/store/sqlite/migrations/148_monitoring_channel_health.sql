-- 148 — Durable per-channel delivery health.
--
-- A broken alert channel was undetectable through any API. On 2026-07-14 a
-- gchat webhook began rejecting every message with HTTP 400 "Missing or
-- malformed token". It logged "send failed" exactly ONCE; the workspace hourly
-- notify cap then withheld the next 191 notifications before any channel was
-- consulted, so the dead route was never retried and never logged again. It
-- stayed dead for six days. That channel was the warn+ route — the one that
-- pages the operator — and nothing in the product could answer "is my alerting
-- actually working?".
--
-- The detection already existed: escalate's dispatcher tracks a consecutive-
-- failure run per route and escalates to ERROR at three. But it held that run
-- in a map on a process-lifetime struct and reported it to a log line. Restart
-- the daemon and the six days of evidence were gone; nobody reads a log line
-- that fires once an hour into a file. Detection was never the hole. Surfacing
-- was the whole hole.
--
-- These columns move that run out of memory and onto the row, so the health of
-- a route is a queryable property of the route rather than an event that
-- scrolled past. They are written by the dispatcher on the two outcomes it
-- already computes; no new detection logic, no model, no extra wake-up.
--
-- NOTE ON last_success_at: it is not a nicety alongside the failure counters,
-- it is the column an operator reads first. "When did this channel last
-- actually deliver?" is unanswerable from failure state alone — a channel that
-- has never been tried and a channel that is working look identical if you only
-- store failures (both have zero). Only last_success_at separates "healthy",
-- "broken", and "never proven".

-- The consecutive-failure run. Reset to 0 by a delivery that succeeds; the
-- dispatcher's broken threshold reads this. Mirrors log_sources.consecutive_
-- failures (migration 128) deliberately — same operational meaning, so the two
-- health surfaces stay legible together.
ALTER TABLE monitoring_channels
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;

-- Start of the CURRENT unbroken failure run, not the first failure ever. Set
-- when a run begins, cleared on success. "Broken since 06:12 on the 14th" is
-- the sentence an operator needs; a lifetime-first timestamp cannot produce it.
ALTER TABLE monitoring_channels
    ADD COLUMN first_failure_at DATETIME NULL;

-- Most recent failure. With first_failure_at this bounds the outage; alone it
-- distinguishes "failing right now" from "failed once last month".
ALTER TABLE monitoring_channels
    ADD COLUMN last_failure_at DATETIME NULL;

-- Short, REDACTED reason for the most recent failure. A webhook URL embeds
-- key+token in its query string and IS the credential, so an error echoing it
-- would persist a live secret into a table the REST API serves. The store layer
-- scrubs and truncates on the way in (see monitoring_channel_health.go) rather
-- than trusting each sender to have scrubbed its own error text — persistence
-- is the last point where that guarantee can still be made.
ALTER TABLE monitoring_channels
    ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

-- Last delivery this channel actually accepted. NULL means never — which is a
-- genuinely different state from healthy, and the API reports it as such rather
-- than flattering an unproven route.
ALTER TABLE monitoring_channels
    ADD COLUMN last_success_at DATETIME NULL;

-- The operator read path: "which of my alert routes are broken right now",
-- across every workspace. Partial so the index stays the size of the problem
-- rather than the size of the channel list.
CREATE INDEX IF NOT EXISTS idx_monitoring_channels_failing
    ON monitoring_channels(workspace_id, last_failure_at DESC)
    WHERE consecutive_failures > 0;
