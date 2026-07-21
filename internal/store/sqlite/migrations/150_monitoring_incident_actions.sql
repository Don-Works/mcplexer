-- 150 — Operator actions on a live incident: acknowledge and silence.
--
-- The daemon already had exactly one lever an operator could pull on an
-- incident: disposition=benign (a permanent mute, reached only through triage or
-- a benign task resolution). That is too blunt. An operator watching a real
-- incident recur wants to say one of three things, and only one of them is
-- "this was never a problem":
--
--   * "I've seen it — stop nagging me, but keep it open."      -> acknowledge
--   * "Be quiet until 18:00, then tell me again if it's live." -> silence
--   * "It's over / not worth tracking."                        -> dismiss
--
-- dismiss reuses the EXISTING benign/resolution vocabulary (disposition=benign +
-- a reversible monitoring_resolutions receipt, broken by any recurrence), so it
-- needs no new column. acknowledge and silence do: they are a bounded, visible,
-- reversible pause of the re-notification nag that must NOT hide an escalation.
--
-- The columns below are that state, and they are read by exactly one place —
-- monitoringNotificationDue in Go — so the suppression stays deterministic and
-- co-located with the persistence policy it modifies. No model is consulted.
--
-- The escalation-piercing contract is encoded in acked_severity / silenced_severity:
-- each records the EFFECTIVE severity (classifier severity raised by sustained
-- age) at the instant the action was taken. The pause holds only while the
-- effective severity has not risen above that floor. "Worse than when I acked"
-- therefore always reaches the operator — ack and silence never survive an
-- escalation past their floor — and because severity and age are both monotonic,
-- a pierced pause stays pierced until the operator re-acks at the new level.

-- Acknowledge: who saw it and when, plus the effective-severity floor the
-- acknowledgement was taken at. NULL acked_at means "not acknowledged".
ALTER TABLE monitoring_incidents ADD COLUMN acked_at TEXT NULL;
ALTER TABLE monitoring_incidents ADD COLUMN acked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE monitoring_incidents ADD COLUMN acked_severity TEXT NOT NULL DEFAULT '';

-- Silence: a BOUNDED quiet period. silenced_until is the hard expiry the daemon
-- reads on every tick; once now >= silenced_until the pause is gone and a still
-- active incident re-notifies. A row can never be silenced "forever" — the store
-- rejects an unbounded duration, and this column carries no sentinel for it.
-- silenced_at/by are the attribution; silenced_severity is the pierce floor.
ALTER TABLE monitoring_incidents ADD COLUMN silenced_at TEXT NULL;
ALTER TABLE monitoring_incidents ADD COLUMN silenced_until TEXT NULL;
ALTER TABLE monitoring_incidents ADD COLUMN silenced_by TEXT NOT NULL DEFAULT '';
ALTER TABLE monitoring_incidents ADD COLUMN silenced_severity TEXT NOT NULL DEFAULT '';

-- The operator read path — "what is currently silenced or acknowledged" — is a
-- workspace scan over the small set of paused incidents. Partial so the index is
-- the size of the suppression surface, not of the incident table.
CREATE INDEX IF NOT EXISTS idx_monitoring_incidents_suppressed
    ON monitoring_incidents(workspace_id)
    WHERE acked_at IS NOT NULL OR silenced_until IS NOT NULL;
