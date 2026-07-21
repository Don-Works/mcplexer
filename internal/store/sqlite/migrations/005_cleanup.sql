-- Fix hijacked global-deny rule: delete the mutated row and re-insert the correct one
DELETE FROM route_rules WHERE id = 'global-deny';
INSERT INTO route_rules (id, priority, workspace_id, path_glob, tool_match, downstream_server_id, auth_scope_id, policy, log_level, source, created_at, updated_at)
VALUES ('global-deny', 0, '', '**', '["*"]', '', '', 'deny', 'info', 'default', datetime('now'), datetime('now'));

-- Delete orphaned route rules (references non-existent auth scopes)
DELETE FROM route_rules WHERE auth_scope_id NOT IN (SELECT id FROM auth_scopes) AND auth_scope_id != '';

-- Fix SQLite downstream: update command from npx to uvx
UPDATE downstream_servers SET command = 'uvx', args = '["mcp-server-sqlite", "--db-path", "./data/mydb.db"]', updated_at = datetime('now') WHERE id = 'sqlite' AND command = 'npx';

-- Delete the unused seeded Linear provider (the auto-discovered one is correct)
DELETE FROM oauth_providers WHERE id = 'linear' AND client_id = '' AND source = 'default';

-- Fix Linear template provider: update the seeded Linear template PKCE flag
UPDATE oauth_providers SET use_pkce = 1, updated_at = datetime('now') WHERE template_id = 'linear' AND source = 'default';
