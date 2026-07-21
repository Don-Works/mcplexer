-- 107 — Move the built-in Slack MCP server from mcp-remote stdio to
-- MCPlexer's native Streamable HTTP OAuth path.
--
-- The old bridge stored OAuth state outside MCPlexer, so the gateway could not
-- reliably surface revocation/reauth state. Only rows that still match the
-- default seeded bridge are rewritten; custom Slack rows are left untouched.
UPDATE downstream_servers
SET
    transport = 'http',
    command = '',
    args = '[]',
    url = 'https://mcp.slack.com/mcp',
    discovery = 'dynamic',
    updated_at = datetime('now')
WHERE id = 'slack'
  AND tool_namespace = 'slack'
  AND transport = 'stdio'
  AND command = 'npx'
  AND args = '["-y", "mcp-remote", "https://mcp.slack.com/mcp"]';
