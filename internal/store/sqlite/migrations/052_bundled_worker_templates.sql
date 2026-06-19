-- M0.7 — Bundled default Worker templates so a fresh install's
-- /workers/templates gallery isn't empty.
--
-- Each template is a JSON-encoded WorkerTemplate stored in
-- skill_registry_entries with payload_type='worker'. The templates
-- target tools that ship with mcplexer (mcplexer__* admin surface), so
-- they work on a brand-new install with no downstream MCP servers
-- registered.
--
-- - daily-status-digest — hourly mcplexer__status summary
-- - audit-summary       — weekly mcplexer__query_audit roll-up
-- - cost-watcher        — hourly mcplexer__worker_cost_aggregate guard
-- - hello-world         — no-tools sanity check
--
-- These rows use deterministic IDs (template-bundled-NN) and a
-- recognisable content_hash prefix (bundled-builtin) so a re-run of
-- the migration on a partially-seeded DB is a no-op via INSERT OR
-- IGNORE. Operators publishing custom versions of any template name
-- get higher version numbers automatically (the Publish flow does
-- MAX(version)+1).

INSERT OR IGNORE INTO skill_registry_entries (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    published_at, created_by_agent_id,
    workspace_id, source_type, source_path, payload_type
) VALUES
(
    'template-bundled-01',
    'daily-status-digest',
    1,
    'bundled-builtin-daily-status-digest-v1',
    'Daily summary of mcplexer state — workspaces, servers, sessions. Built-in template.',
    '{"name":"daily-status-digest","description":"Daily summary of mcplexer state — workspaces, servers, sessions.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"Summarize the current state of mcplexer for the day: workspaces, servers, sessions. Keep it under 200 words. Sign off as ''Morning Digest''.","schedule_spec_hint":"0 9 * * *","tool_allowlist":["mcplexer__status"],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider"}]}',
    '{}', '["builtin","digest","mcplexer"]', 'mcplexer-builtin', NULL,
    CAST(strftime('%s', '2026-05-21') AS INTEGER), NULL,
    NULL, 'inline', NULL, 'worker'
),
(
    'template-bundled-02',
    'audit-summary',
    1,
    'bundled-builtin-audit-summary-v1',
    'Weekly summary of audit-log activity. Built-in template.',
    '{"name":"audit-summary","description":"Weekly summary of audit-log activity. Highlights unusual patterns.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"Summarize the last 7 days of audit activity. Highlight unusual patterns. Keep it under 300 words.","schedule_spec_hint":"0 17 * * 5","tool_allowlist":["mcplexer__query_audit"],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider"}]}',
    '{}', '["builtin","audit","mcplexer"]', 'mcplexer-builtin', NULL,
    CAST(strftime('%s', '2026-05-21') AS INTEGER), NULL,
    NULL, 'inline', NULL, 'worker'
),
(
    'template-bundled-03',
    'cost-watcher',
    1,
    'bundled-builtin-cost-watcher-v1',
    'Hourly worker-spend guard. Flags when MTD exceeds $50. Built-in template.',
    '{"name":"cost-watcher","description":"Hourly worker-spend guard. Flags when MTD exceeds $50.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"Check current MTD spend across all workers. Report if MTD exceeds $50 — flag the worker with the highest cost contribution. Otherwise return ''within budget''.","schedule_spec_hint":"0 * * * *","tool_allowlist":["mcplexer__worker_cost_aggregate"],"output_channels_hint":[{"type":"mesh","priority":"high"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider"}]}',
    '{}', '["builtin","cost","mcplexer"]', 'mcplexer-builtin', NULL,
    CAST(strftime('%s', '2026-05-21') AS INTEGER), NULL,
    NULL, 'inline', NULL, 'worker'
),
(
    'template-bundled-04',
    'hello-world',
    1,
    'bundled-builtin-hello-world-v1',
    'No-tools sanity check that confirms mcplexer Workers is alive. Built-in template.',
    '{"name":"hello-world","description":"No-tools sanity check that confirms mcplexer Workers is alive.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"Send a one-sentence greeting confirming that mcplexer Workers is alive.","schedule_spec_hint":"0 9 * * *","tool_allowlist":[],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider"}]}',
    '{}', '["builtin","starter","mcplexer"]', 'mcplexer-builtin', NULL,
    CAST(strftime('%s', '2026-05-21') AS INTEGER), NULL,
    NULL, 'inline', NULL, 'worker'
);
