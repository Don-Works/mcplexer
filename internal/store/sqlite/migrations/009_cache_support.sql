-- Add cache configuration column to downstream servers.
ALTER TABLE downstream_servers ADD COLUMN cache_config TEXT NOT NULL DEFAULT '{}';

-- Add cache_hit flag to audit records for observability.
ALTER TABLE audit_records ADD COLUMN cache_hit INTEGER NOT NULL DEFAULT 0;
