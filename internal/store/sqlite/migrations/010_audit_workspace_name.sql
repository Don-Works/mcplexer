ALTER TABLE audit_records ADD COLUMN workspace_name TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_approvals ADD COLUMN workspace_name TEXT NOT NULL DEFAULT '';
