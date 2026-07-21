-- 085 — Per-server tools/call dispatch timeout.
--
-- Today's incident (2026-05-27): the Linear MCP HTTP downstream
-- got into a state where every tools/call hung mid-stream. The
-- background-refresh aggregator (internal/cache/aggregator.go,
-- PerServerListToolsTimeout = 15s) skipped the wedged server
-- cleanly, but client-side tools/call dispatch in Manager.Call
-- has no equivalent deadline — a Claude session that called
-- linear__list_teams sat indefinitely waiting for a response
-- that would never come.
--
-- Adds a per-server call_timeout_sec column so each downstream
-- can carry its own bound. Zero / unset means "use the gateway
-- default" (120s) — code-side, not column-side, so a future
-- bump of the default doesn't require a backfill.
--
-- 120s default sits comfortably above the 30s upstream MCP
-- client ceiling for slow downstreams (e.g. Playwright cold-
-- start, an LLM-backed downstream doing a long-running search)
-- while still being short enough to recover from a wedge inside
-- one typical agent turn.

ALTER TABLE downstream_servers
    ADD COLUMN call_timeout_sec INTEGER NOT NULL DEFAULT 0;
