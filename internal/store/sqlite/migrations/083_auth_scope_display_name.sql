-- 083_auth_scope_display_name.sql
--
-- The Credentials tab today renders scope names like
-- `clickup_oauth_agent_example_workspace` and `linear_oauth_gateway_linear`
-- straight from auth_scopes.name — these are computed from
-- {server_slug}_{provider_id} during quick-setup and were never meant
-- for human eyes.
--
-- Adds a nullable-style (NOT NULL DEFAULT '') display_name column. The
-- UI prefers display_name when present and falls back to a humanised
-- form of name otherwise, so:
--   - existing rows continue to scan without backfill (default '')
--   - the operator can later rename a credential to e.g. "ClickUp"
--     without touching the slug that downstream wiring references.
--
-- name itself stays UNIQUE and stable (it's the externally-referenced
-- handle); display_name is a presentation-only field with no uniqueness
-- requirement.

ALTER TABLE auth_scopes
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
