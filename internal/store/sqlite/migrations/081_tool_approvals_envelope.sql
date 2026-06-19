-- BUG-ENV — approval queue envelope schema for cross-boundary shares.
--
-- The pre-existing tool_approvals table only carried MCP tool-call fields
-- (tool_name, arguments, justification). Cross-boundary shares
-- (skill_share, memory_share, task_offer, mesh_direct) and consent
-- bookkeeping (mesh_grant_consent) need three additional fields so the
-- dashboard can render a useful preview:
--
--   originating_workspace — which workspace produced the share request.
--                           Distinct from workspace_id (the *target* of
--                           the routed tool call) so the recipient can
--                           tell which of their workspaces emitted the
--                           share without scanning arguments.
--   kind                  — share type ∈ {skill_share, memory_share,
--                           task_offer, mesh_direct, mesh_grant_consent}.
--                           Empty for legacy tool-call rows; the UI falls
--                           back to surface/tool_name in that case.
--   summary               — human-readable preview (skill name / memory
--                           title / task title / message head / "Granted
--                           X to peer Y"), with secrets redacted upstream.
--                           Distinct from justification (which is the
--                           agent's "why").
--
-- All three are nullable + default '' so old rows still render and old
-- callers don't need to know about the new fields.

ALTER TABLE tool_approvals ADD COLUMN originating_workspace TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_approvals ADD COLUMN kind                  TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_approvals ADD COLUMN summary               TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_tool_approvals_kind
    ON tool_approvals(kind);
