-- 101 — Bundled dream-consolidator worker template (harvest recipes + memory).
--
-- Ships a scheduled "dream mode" consolidation worker. Runs off-peak
-- (recommended after the memory-consolidator, e.g. 04:00 UTC) and
-- performs two responsibilities in one autonomous pass:
--
--   1. Memory compaction (global + workspace passes, invalidate/supersede
--      pattern identical to the memory-consolidator).
--   2. Recipe harvesting: distills successful/reliable code-mode patterns
--      (execute_code snippets, common tool sequences, safe batching) into
--      dense, recallable "recipe" notes saved with tags=["recipe","harvested","dream"].
--      Recipes are written to global scope so they are available cross-workspace
--      for cheap models via memory__recall / future recipe search surfaces.
--
-- The template is the richer, LLM-driven, scheduled alternative to ad-hoc
-- harvesting. It uses mcpx__execute_code + mcpx__search_tools for discovery
-- during harvest, plus the memory__* surface for both compaction and
-- persisting the harvested recipes.
--
-- Pattern mirrors 064 (and 052) exactly: deterministic PK, content-hash
-- prefix, INSERT OR IGNORE, direct insert to worker_templates (post-057 split).
--
-- Operators can customise via the publish flow (higher versions).
-- The worker materialised from this template is named "dream-consolidator".

INSERT OR IGNORE INTO worker_templates (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    published_at, created_by_agent_id, workspace_id
) VALUES (
    'template-bundled-dream-consolidator',
    'dream-consolidator',
    1,
    'bundled-builtin-dream-consolidator-v1',
    'Dream-mode scheduled worker: compacts memory (global+workspace) and harvests reusable code-mode recipes/patterns into tagged global notes for cheap-model fluency. Built-in template.',
    '{"name":"dream-consolidator","description":"Sleep-time dream consolidation (recipes + memory). Runs memory compaction passes (global then workspace) using the invalidate/supersede pattern, then harvests reliable execute_code and tool-use patterns from context + recall into dense global-scope recipe notes tagged recipe/harvested/dream. Inspired by sleep-time compute + recipe distillation for cheap models that struggle with raw code-mode. Default schedule hint 0 4 * * * (after memory-consolidator).","model_provider_hint":"claude_cli","model_id_hint":"claude-sonnet-4-6","prompt_template":"You are mcplexer''s dream-mode consolidator. You run OFF-PEAK (nightly) to keep memory small/dense AND to harvest reusable recipes that make cheap models (GLM/Haiku/MiniMax) fluent on the code-mode (mcpx__execute_code) surface.\n\n# Pass 1 — GLOBAL memory (same rules as memory-consolidator)\n1. memory__list({kind:\"note\", scope:\"global_only\", limit:40})\n2. For clusters of near-duplicates, memory__save one richer note (scope:\"global\" explicitly), then memory__invalidate the sources (superseded_by the new id). Never forget.\n3. Skip pinned; never touch facts.\n\n# Pass 2 — WORKSPACE memory\nSame as Pass 1 but scope:\"workspace_only\" then save with scope:\"workspace\".\n\n# Pass 3 — Recipe harvest (for cheap model fluency)\n1. Use mcpx__search_tools({queries:[\"common successful execute_code patterns\",\"batch tool use\",\"safe memory consolidate example\"]}) and memory__recall({q:\"successful code-mode OR execute_code snippet\", tags:[\"recipe\"], limit:30}) to surface candidates and prior recipes.\n2. Distil 2-4 high-value, short, reliable recipes: each is a tiny self-contained JS snippet + \"When to use\" + pitfalls. Focus on frequent operations (task batching, memory save+recall patterns, worker control, safe error handling in execute_code).\n3. For each, memory__save({kind:\"note\", scope:\"global\", name:\"recipe:<kebab-short-name>\", tags:[\"recipe\",\"harvested\",\"dream\"], body: \"When: ...\\n```js\\n// the exact snippet using task.xxx / memory.xxx / mcpx__... \\n```\\nSources: harvested during dream run\" }).\n4. If near-dupe recipe notes exist, invalidate the old ones pointing at the new canonical.\n\n# Output contract\nNo prose to user. End after the last tool call. The scheduler will re-run you on your schedule_spec.","schedule_spec_hint":"0 4 * * *","tool_allowlist":["memory__list","memory__get","memory__save","memory__recall","memory__invalidate","mcpx__execute_code","mcpx__search_tools"],"output_channels_hint":[{"type":"mesh","priority":"low"}],"exec_mode_hint":"autonomous"}',
    '{}',
    '["builtin","dream","consolidator","recipes","memory","cleanup"]',
    'mcplexer-builtin',
    NULL,
    CAST(strftime('%s', '2026-06-11') AS INTEGER),
    NULL,
    NULL
);
