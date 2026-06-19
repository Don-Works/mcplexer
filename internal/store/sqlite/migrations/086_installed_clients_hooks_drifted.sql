-- 086 — Shell Guard drift detection column.
--
-- installed_clients.hooks_installed is set true when InstallClaudeCodeHooks
-- writes the PreToolUse curl shim into ~/.claude/settings.json. But that
-- file is not write-locked: a later `mcplexer rules sync`, a manual edit,
-- or another tool replacing the file can strip the entry — and today the
-- dashboard still reports green while the underlying hook chain doesn't
-- fire. audit_records ends up silently empty for Bash, and "permissive"
-- is indistinguishable from "broken".
--
-- This column lets the read-side surface (GET /api/v1/guards/shell) write
-- back a drift flag when it re-reads settings.json and the endpoint
-- substring is no longer present. The UI renders this as a red
-- "Hook drifted — re-install to repair" badge alongside the existing
-- install/uninstall controls.
--
-- Default 0 — fresh DBs start clean; the read path is the authority that
-- flips this on for existing rows whose underlying file has drifted.

ALTER TABLE installed_clients
    ADD COLUMN hooks_drifted INTEGER NOT NULL DEFAULT 0;
