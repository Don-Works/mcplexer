# Bounding MCP Context Cost

MCPlexer keeps the static tool surface slim, then lets agents discover tools with
`mcpx__search_tools` and batch calls with `mcpx__execute_code`. This document
records the payload bounds added for scoped retrieval and the measurement harness
used to catch regressions.

## Defaults and Hard Caps

| Surface | Default | Hard cap | Hydrate path |
| --- | ---: | ---: | --- |
| `tools/list` advertised surface | `slim_surface=true` | off via setting/env | `mcpx__search_tools` |
| Code-mode captured output | 24 KiB | 256 KiB | raise `code_mode_max_output_bytes` |
| `mesh__send` body | 64 KiB | 64 KiB | use task attachments or another blob channel |
| `mesh__receive` results | 20 | 50 | request fewer/more up to configured cap |
| `mesh__receive` message preview | 512 bytes | 2048 bytes | `mesh__hydrate` / `mesh__thread` |
| `mesh__hydrate` content | 16 KiB | 64 KiB | raise `max_content_bytes` on hydrate |
| `task__list` rows | preview rows | `full:true` | `task__get({id})` |
| `task__recent_activity` | full rows | 500 rows | `dedupe:true` for clusters |

Environment overrides:

- `MCPLEXER_SLIM_SURFACE=false`
- `MCPLEXER_CODE_MODE_MAX_OUTPUT_BYTES`
- `MCPLEXER_MESH_RECEIVE_MAX_RESULTS`
- `MCPLEXER_MESH_RECEIVE_PREVIEW_BYTES`
- `MCPLEXER_MESH_SEND_MAX_CONTENT_BYTES`

The dashboard Settings page exposes the same controls.

Runtime counters are available through `mcpx__context_cost_stats`. The tool
returns process-local result-byte counters by tool plus the active slim-surface,
compaction, code-mode, and mesh cap settings. Counters reset on daemon restart.

Ranking is applied only after authorization and caps:

- `memory__recall` fuses scoped FTS and vector hits when an embedder is configured.
- `mcpx__skill_search` builds a scoped TF-IDF index over visible skill heads.
- `task__list({q, semantic:true})` TF-IDF-ranks up to 500 already scoped and
  filter-matched task candidates, then returns preview rows by default.
- Mesh retrieval remains preview/hydrate-first; semantic ranking is not a
  substitute for mesh send/receive caps.

## Measurement Harness

Run the opt-in baseline:

```sh
MCPLEXER_CONTEXT_COST_BASELINE=1 go test ./internal/gateway -run TestContextCostBaseline -count=1 -v
```

Enforce thresholds:

```sh
MCPLEXER_CONTEXT_COST_BASELINE=1 MCPLEXER_CONTEXT_COST_ENFORCE=1 go test ./internal/gateway -run TestContextCostBaseline -count=1 -v
```

Current measured guardrails:

| Surface | Bytes | Approx tokens | Threshold |
| --- | ---: | ---: | ---: |
| tools/list default slim | 7,219 | 1,805 | 20,000 |
| tools/list full surface, slim schemas | 66,818 | 16,705 | 120,000 |
| tools/list full surface, full schemas | 88,932 | 22,233 | 180,000 |
| search_tools summary multi-query | 12,722 | 3,181 | 16,000 |
| search_tools full multi-query | 54,383 | 13,596 | 60,000 |
| execute_code large print truncated | 24,793 | 6,199 | 32,000 |
| mesh receive 6 KiB default preview | 1,037 | 260 | 2,000 |
| mesh receive 6 KiB max preview | 2,574 | 644 | 4,000 |
| mesh hydrate 6 KiB body | 6,293 | 1,574 | 9,000 |
| task list five | 5,164 | 1,291 | 18,000 |
| task get single with notes | 3,625 | 907 | 12,000 |
| memory recall five | 3,188 | 797 | 5,000 |
| memory get single full body | 2,781 | 696 | 4,000 |
| skill search three | 817 | 205 | 3,000 |
| skill get single body | 3,040 | 760 | 8,000 |

## Rollout

1. Keep `slim_surface=true` in production.
2. Roll out mesh preview/hydrate and code output caps together; they are hard
   caps first, retrieval ranking second.
3. Watch for agent prompts that assume `mesh__receive` returns full bodies or
   `task__list` returns full descriptions. Update those prompts to hydrate by ID.
4. Run the enforced baseline in CI for gateway changes that touch tools/list,
   search, code mode, mesh, tasks, memory, or skill registry.
5. Check `mcpx__context_cost_stats` during rollout for tools whose max or last
   result bytes climb toward the measured thresholds.

## Rollback

- Set `MCPLEXER_SLIM_SURFACE=false` if a client cannot use deferred discovery.
- Use `task__list({full:true})` temporarily for callers that still need full task
  rows while migrating to `task__get`.
- Raise `code_mode_max_output_bytes` or mesh receive preview settings within the
  hard caps for a controlled workload.
- Do not remove mesh send/hydrate hard caps; route large payloads through a
  content-addressed channel instead.

## Remaining Risks

- Mesh candidates still rely on preview/hydrate and lexical clustering before a
  future vector index. Do not relax mesh hard caps while adding ranking.
- `search_tools detail:"full"` is the largest hot read surface. It remains under
  the current guardrail but is the next candidate for tighter ranking and paging.
