-- Store redacted response content in audit records.
ALTER TABLE audit_records ADD COLUMN response_redacted TEXT NOT NULL DEFAULT '';
