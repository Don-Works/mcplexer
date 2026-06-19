-- 064 — Bundled task-status-consolidator worker template (Phase 5).
--
-- Ships one worker template that knows how to clean up the freeform
-- status vocabulary of a workspace's tasks:
--
--   1. Calls task__list({workspace, state: "any"}) via mcpx__execute_code.
--   2. Extracts distinct status values.
--   3. Asks the model to group semantically-similar strings into
--      clusters and pick a canonical name per cluster.
--   4. Emits a JSON plan operators can review + apply via
--      task__apply_status_consolidation (admin tool).
--
-- The matching admin tool — task__consolidate_statuses — can also
-- compute a fast, model-free heuristic proposal directly. The bundled
-- worker template is the richer, scheduled, semantic alternative.
--
-- Pattern mirrors 052_bundled_worker_templates.sql exactly: a
-- deterministic primary key (`template-bundled-task-status-consolidator`),
-- a recognisable content-hash prefix (`bundled-builtin-`), and
-- INSERT OR IGNORE so re-runs on a partially-seeded DB are a no-op.
-- Operators who customise the template via the Publish flow get higher
-- version numbers automatically (MAX(version)+1).
--
-- Schema: 057_worker_templates_split.sql moved bundled worker rows out
-- of skill_registry_entries and into the worker_templates table. This
-- bundled row therefore inserts directly into worker_templates.

INSERT OR IGNORE INTO worker_templates (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    published_at, created_by_agent_id, workspace_id
) VALUES (
    'template-bundled-task-status-consolidator',
    'task-status-consolidator',
    1,
    'bundled-builtin-task-status-consolidator-v1',
    'Cluster a workspace''s freeform task statuses into a canonical vocabulary. Built-in template.',
    '{"name":"task-status-consolidator","description":"Group semantically-similar task statuses in a workspace into canonical clusters and flag terminal ones. Returns a JSON plan an admin can review and apply via task__apply_status_consolidation.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"You are cleaning up the freeform task-status vocabulary for one workspace.\n\nStep 1 — fetch the current statuses using mcpx__execute_code:\n```js\nconst rows = task.list({ workspace: \"{{workspace}}\", state: \"any\", limit: 500 });\nconst counts = {};\nfor (const t of (rows.tasks || [])) { counts[t.status] = (counts[t.status] || 0) + 1; }\nprint(JSON.stringify({ counts, known_statuses: rows.known_statuses || [] }));\n```\n\nStep 2 — group near-duplicates and obvious synonyms into clusters (e.g. doing/in-progress/wip → \"doing\"; done/finished/complete → \"done\"; blocked/waiting/stuck → \"blocked\"). Pick the shortest agent-friendly canonical name per cluster. Mark a cluster `terminal: true` when reaching it means the task is closed (done, cancelled, wontfix, rejected). Leave singletons that look intentional alone.\n\nStep 3 — emit ONLY this JSON shape, nothing else:\n```json\n{\n  \"workspace\": \"{{workspace}}\",\n  \"merges\": [\n    {\"from\": \"in-progress\", \"to\": \"doing\", \"terminal\": false},\n    {\"from\": \"wip\",         \"to\": \"doing\", \"terminal\": false},\n    {\"from\": \"finished\",    \"to\": \"done\",  \"terminal\": true}\n  ]\n}\n```\nIf no merges are warranted, return `{\"workspace\": \"{{workspace}}\", \"merges\": []}`. The admin reviews this plan before applying.","schedule_spec_hint":"0 6 * * 0","tool_allowlist":["mcpx__execute_code","mcpx__search_tools"],"output_channels_hint":[{"type":"mesh","priority":"low"}],"exec_mode_hint":"autonomous","parameter_schema":[{"name":"workspace","type":"string","description":"Workspace name or ID to consolidate.","required":true}],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider"}]}',
    '{}',
    '["builtin","tasks","cleanup","status","mcplexer"]',
    'mcplexer-builtin',
    NULL,
    CAST(strftime('%s', '2026-05-22') AS INTEGER),
    NULL,
    NULL
);
