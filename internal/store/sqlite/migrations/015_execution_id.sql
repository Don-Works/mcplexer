-- Add execution_id to group audit records from a single execute_code invocation.
ALTER TABLE audit_records ADD COLUMN execution_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_audit_execution ON audit_records (execution_id) WHERE execution_id != '';
