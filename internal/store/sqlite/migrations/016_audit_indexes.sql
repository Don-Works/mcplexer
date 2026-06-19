-- Performance indexes for dashboard queries.

-- Time-range scans (used by nearly every dashboard query).
CREATE INDEX IF NOT EXISTS idx_audit_records_timestamp
    ON audit_records (timestamp DESC);

-- Error/blocked filtering with time range.
CREATE INDEX IF NOT EXISTS idx_audit_records_status_timestamp
    ON audit_records (status, timestamp DESC);

-- Workspace filtering with time range.
CREATE INDEX IF NOT EXISTS idx_audit_records_workspace_timestamp
    ON audit_records (workspace_id, timestamp DESC);

-- Session lookup (stale session cleanup, session audit log).
CREATE INDEX IF NOT EXISTS idx_audit_records_session_id
    ON audit_records (session_id);

-- Tool leaderboard P95 queries.
CREATE INDEX IF NOT EXISTS idx_audit_records_tool_timestamp
    ON audit_records (tool_name, timestamp);

-- Server health P95 queries.
CREATE INDEX IF NOT EXISTS idx_audit_records_server_timestamp
    ON audit_records (downstream_server_id, timestamp);

-- Cache hit stats.
CREATE INDEX IF NOT EXISTS idx_audit_records_cache_hit_timestamp
    ON audit_records (cache_hit, timestamp)
    WHERE tool_name NOT LIKE 'mcplexer__%';
