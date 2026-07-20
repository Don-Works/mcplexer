-- 149 — Channel health measured by DEMAND, not by attempts.
--
-- Migration 148 persisted the dispatcher's consecutive-failure run. That closes
-- the case where a dead route is actually tried, and it is genuinely load
-- bearing — a route driven to five failures now reports broken over the API.
-- But it cannot close the 2026-07-14 case on its own, and the reason is an
-- ordering property of the dispatcher that no amount of failure-counting can
-- work around.
--
-- The throttle runs BEFORE channels are consulted (prepareNotification, then
-- deliverChannels). So when the workspace hourly cap is spent, no channel is
-- attempted, and a counter that only advances on attempts cannot advance at
-- all. It does not reset — the run is consecutive, not windowed — it simply
-- freezes at whatever it reached.
--
-- The subtlety worth writing down, because it is easy to get backwards: a dead
-- route does NOT suppress itself. recordNotify writes a throttle mark only when
-- a send actually succeeded, so a route that never succeeds never spends the
-- budget. What spent the budget on 2026-07-14 was OTHER traffic in the same
-- workspace — other templates, other healthy channels. The cap is per
-- workspace, so healthy noise starved the broken route of the very attempts
-- that would have proven it broken. It froze at one failure and stayed there
-- for six days.
--
-- So the observable has to be inverted. Instead of counting failures (which
-- requires attempts, which suppression prevents), count the notifications this
-- channel was ELIGIBLE for — recorded before the throttle, so suppression is
-- irrelevant to it — and reset that count only when a delivery actually
-- succeeds. The question stops being "did sending fail?" and becomes "was this
-- route owed messages that it never delivered?", which is the question the
-- operator was really asking all along.

-- Notifications generated since this channel last successfully delivered, for
-- which this channel was an eligible route (enabled, and the severity at or
-- above its min_severity floor). Incremented BEFORE the throttle decision, so
-- it advances whether the notification was delivered, failed, or suppressed.
-- Zeroed by a successful delivery, and only by that.
--
-- This is the DENOMINATOR that makes staleness safe to act on. A quiet
-- workspace never targets its channels, so the count never grows and a healthy
-- but idle route can never drift into "broken". Without it, deriving health
-- from the age of last_success_at alone would flag every correctly-configured
-- channel in a workspace that simply had nothing to report — a false positive
-- that gets the whole feature switched off, which lands us back at silence.
ALTER TABLE monitoring_channels
    ADD COLUMN targeted_since_success INTEGER NOT NULL DEFAULT 0;

-- When this channel was last owed a notification. With last_success_at it
-- brackets the outage in wall-clock terms — "owed messages as recently as
-- 09:14, last actually delivered on the 14th" — which is the sentence that
-- makes a stale route legible without the reader doing arithmetic on counters.
ALTER TABLE monitoring_channels
    ADD COLUMN last_targeted_at DATETIME NULL;

-- The operator read path: routes that are owed messages they are not
-- delivering, worst first. Partial, so the index is the size of the problem.
CREATE INDEX IF NOT EXISTS idx_monitoring_channels_owed
    ON monitoring_channels(workspace_id, targeted_since_success DESC)
    WHERE targeted_since_success > 0;
