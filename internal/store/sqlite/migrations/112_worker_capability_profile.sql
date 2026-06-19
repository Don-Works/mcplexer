-- Delegation capability scoping: a first-class enforcement column on the
-- workers table carrying the marshalled toolgate.CapabilityProfile.
--
-- Empty string = no profile = today's allow-all behavior (only
-- tool_allowlist_json gates). Enforcement-bearing config MUST live in a
-- column, not the display-only _mcplexer_delegation parameters blob, so a
-- corrupt or absent blob can never silently widen a delegate's surface.
--
-- A matching schema invariant (ensureWorkerCapabilityProfile) adds this
-- column idempotently on every boot, covering branch swaps / partially
-- restored backups where schema_version raced past this migration.

ALTER TABLE workers ADD COLUMN capability_profile_json TEXT NOT NULL DEFAULT '';
