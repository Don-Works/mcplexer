-- M0-B (Guards) — record which Guard surface raised the approval.
--
-- The Guards initiative routes approval requests through `approval.Manager`
-- from five distinct surfaces: shell, schedule, mcp, network, sanitizer.
-- The Surface field on store.ToolApproval lets the dashboard, audit, and
-- the approval-rules engine distinguish "shell command needs ok" from
-- "MCP tool call needs ok" without re-deriving from ToolName.
--
-- Default '' preserves backwards compatibility with existing MCP-only
-- callers — they're treated as Surface="mcp" at the read boundary in Go.

ALTER TABLE tool_approvals ADD COLUMN surface TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_tool_approvals_surface
    ON tool_approvals(surface);
