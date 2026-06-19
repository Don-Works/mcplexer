# Token-Preservation Delegation

Last researched: 2026-06-10

## Goal

Use frontier sessions, such as Claude Opus/Fable or GPT-5.5, only for planning,
architecture, risk calls, and integration review. Move token-heavy execution
into bounded mcplexer Workers running cheaper "code cutter" models.

This is not just a cost optimization. It is also a context hygiene rule: the
parent session should keep the decision record clean and should not fill itself
with codebase scans, build logs, repeated file reads, or mechanical edits.

## Current State

mcplexer already has most of the substrate:

- `docs/context-cost.md` documents the low-context MCP surface:
  `mcpx__search_tools`, `mcpx__execute_code`, bounded mesh previews, task
  preview/hydrate, and runtime context-cost counters.
- Workers are persisted in `internal/store/workers.go` with per-run caps:
  `MaxInputTokens`, `MaxOutputTokens`, `MaxToolCalls`,
  `MaxWallClockSeconds`, and monthly cost / failure autopause fields.
- Worker runs are driven by `internal/workers/runner`, with a two-tool worker
  surface locked by `internal/gateway/worker_surface_test.go`: workers see only
  `mcpx__search_tools` and `mcpx__execute_code`.
- The worker dispatcher applies the worker's allowlist to discovery and nested
  calls, so a worker can be given broad search/execute primitives while still
  being limited to specific inner tools.
- `claude_cli`, `opencode_cli`, and `grok_cli` worker providers exist in
  `internal/models/claude_cli.go`, `internal/models/opencode_cli.go`, and
  `internal/models/grok_cli.go`. They are opt-in through daemon env vars
  because they currently run with host network egress.
- `grok_cli` runs may currently record zero tokens/cost when the Grok CLI JSON
  response omits usage fields. Use run status/output for correctness and avoid
  treating zero cost as authoritative spend accounting.
- `mcplexer__spawn_subagent` already exists in
  `internal/control/tools_spawn_subagent.go` and creates a one-shot Worker with
  a default bounded allowlist.
- Mesh, tasks, memory, and worker output channels already give us a cross-client
  pickup layer. This matters because Claude Code and Codex do not share a
  native conversation context.
- Memory is explicitly part of the default worker contract: `defaultDelegationTools`
  (internal/workers/admin/delegation.go) includes `memory__save`, `memory__recall`,
  `memory__list` (plus core task tools) for execute workers; review workers get the
  read side only. The `WorkerPreamble` (embedded in gateway, locked by test) tells
  every worker "your surface is exactly two tools" and "persist across runs in the
  `memory` namespace". Inside `execute_code` the worker calls `memory.save` /
  `memory.recall` (verb form under the namespace object; search surfaces the
  `memory__*` names). Skill bodies can be attached for extra memory usage examples
  or domain facts ("skill/body pickup"). The same two-tool + search/execute path
  works for direct harnesses, server-prefixed (Grok CLI etc.), and all CLI-backed
  workers (grok_cli, opencode_cli for MiniMax/GLM, etc.). Harness naming differences
  are normalised by the gateway; the inner memory namespace contract is identical.

The important gap: `spawn_subagent` is an admin/control tool. Normal project
sessions are intentionally supposed to see only the universal surface, not
worker CRUD. In a live check from this repo, `mcpx__search_tools` did not find a
spawn/delegation tool. So an expensive parent agent cannot reliably discover a
safe project-level delegation path today.

## External Constraints

Official Claude Code guidance aligns with this direction:

- Claude subagents are for side tasks that would flood the main conversation
  with search results, logs, or file contents, and each runs in its own context
  window. See Anthropic's subagent docs:
  https://code.claude.com/docs/en/sub-agents
- Anthropic's cost guidance says to reserve Opus for complex architectural or
  multi-step reasoning, use Sonnet for most coding, and use Haiku for simple
  subagent tasks. It also recommends reducing MCP overhead and delegating
  verbose operations to subagents:
  https://code.claude.com/docs/en/costs
- Claude Code model aliases update over time. On the Anthropic API, `opus`
  currently resolves to Opus 4.8, `sonnet` to Sonnet 4.6, and `haiku` to
  Haiku 4.5. Pin full model IDs for reproducibility:
  https://code.claude.com/docs/en/model-config

Official Codex guidance also matches:

- Codex subagents are enabled by default in current releases but only spawn
  when explicitly asked. They run separately and return consolidated results,
  but they still consume tokens:
  https://developers.openai.com/codex/subagents
- OpenAI recommends GPT-5.5 for demanding coding/research, GPT-5.4-mini for
  lower-cost lighter subagent work, and GPT-5.3-Codex-Spark for near-instant
  text-only iteration where available:
  https://developers.openai.com/codex/concepts/subagents
- Codex reads `AGENTS.md` before work, with project guidance layered over
  global guidance:
  https://developers.openai.com/codex/guides/agents-md
- `codex exec` is useful for automation and can run with explicit sandbox and
  output settings:
  https://developers.openai.com/codex/noninteractive

Model economics as of the same research pass:

- Anthropic lists Opus 4.8 at $5 / input MTok and $25 / output MTok, Sonnet 4.6
  at $3 / $15, and Haiku 4.5 at $1 / $5:
  https://platform.claude.com/docs/en/about-claude/models/overview
- Codex docs list GPT-5.4-mini as the fast, efficient model for responsive
  coding tasks and subagents. GPT-5.3-Codex-Spark is Pro-only research preview:
  https://developers.openai.com/codex/models

## Delegation Rule

The parent agent must delegate when any of these are true:

- The work requires reading many files, broad search, or repeated inspection.
- The work needs mechanical edits across more than one or two files.
- The work requires running builds/tests and interpreting logs.
- The prompt asks for implementation after the architecture is already clear.
- The expected intermediate output would be useful only as a summary.
- The parent model is Opus/Fable/GPT-5.5 and a cheaper worker can do the
  execution with a bounded spec.

The parent should keep:

- problem framing and acceptance criteria
- architecture decisions and tradeoffs
- worker task decomposition
- result review and integration
- final user communication

The worker should do:

- codebase mapping
- targeted implementation
- test/log digestion
- repetitive edits
- first-pass review against an explicit checklist
- result summarization

## Handoff Packet

Every delegated task should be a compact packet, not a transcript dump.

Recommended shape:

```markdown
## Objective
One sentence describing the outcome.

## Scope
Allowed files/directories and explicit out-of-scope items.

## Known Facts
Only facts the parent verified, with file paths or command outputs where useful.

## Constraints
Coding conventions, security constraints, no-go areas, and allowed side effects.

## Acceptance Criteria
Concrete pass/fail checks.

## Verification
Commands to run, or "do not run tests; explain why" if unavailable.

## Return Contract
Return: files changed, tests run, summary, risks, and any unresolved questions.
Do not paste raw logs unless the parent explicitly asks.
```

Size target: keep the packet under roughly 2,000-4,000 tokens. If more context
is needed, put it in a `task__create` work context or an attachment and pass the
task ID, not the whole blob.

## Implemented Product Path

As of this implementation pass, MCPlexer has a first-class delegation path:

- `mcpx__delegate_worker` creates one or more bounded one-shot Workers from a
  normal project session, defaulting to the caller workspace and recording
  parent context, parent spend, baseline estimates, model/profile choice, and
  parallel index. It supports `worker_mode` (`execute` or `review`) and
  `review_required`, which defaults to true so completed work stays
  `needs_review` until the parent model records a score.
- `mcpx__list_delegations` returns the context tree and ledger: parent context,
  worker contexts, latest runs, status, model, tokens, cost, tool calls,
  frontier-model tokens/cost avoided, worker token delta, estimated cost saved,
  review state/score, and per-model stats used by the dashboard model rank.
  Parent context/cost is shown as a sunk ledger value, not subtracted from
  delegation ROI.
- `mcpx__review_delegation` records the expensive parent model's score, outcome,
  notes, reviewer context, and reviewer model. The score is attributed to every
  participating model in that delegation so quality can be ranked by model.
- The dashboard route `/delegations` exposes the same launch, audit, savings,
  scoring, and model-rank workflow.

For OpenCode-backed workers, prefer a running local OpenCode server endpoint
(`http://127.0.0.1:4096`) in the model profile. The worker adapter uses
`opencode run --attach <endpoint>` for HTTP(S) endpoints, which avoids parallel
raw CLI processes contending on OpenCode's local SQLite database.

`mcplexer__spawn_subagent` remains an admin/control escape hatch. Do not expose
it directly to normal project sessions; use the narrower delegation tools unless
the operator is deliberately working from an admin-trusted context.

### 1. Add a Universal Delegation Tool

The first shipped tool is `mcpx__delegate_worker`. It deliberately wraps Worker
creation instead of exposing broad worker CRUD.

The safe tool should accept a narrower input:

- `objective`
- `handoff`
- optional `task_id`
- optional `worker_mode`: `execute` for implementation, `review` for
  critique/audit
- optional `review_required`, default true
- optional `model_profile_id`, or explicit provider/model fields
- parent context/spend fields
- baseline token/cost estimates
- optional `parallelism`
- optional caps with hard upper bounds

It should derive workspace from the caller's session CWD / workspace, not from
an arbitrary caller-supplied `workspace_id`. Cross-workspace delegation should
require an explicit workspace grant or an admin context.

### 2. Ship Code-Cutter Profiles

Create named model profiles/templates that the delegation tool can select
without expensive parent agents reasoning over provider details:

- `code-cutter-haiku`: `anthropic`, `opencode_cli`, or `grok_cli` using a
  configured low-cost coding model.
- `code-cutter-mini`: OpenAI GPT-5.4-mini through direct OpenAI or opencode.
- `code-cutter-spark`: GPT-5.3-Codex-Spark for Pro users where available.
- `code-reviewer-mini`: read-only, higher reasoning than code-cutter.

Keep the profile set exploratory rather than frozen. Seed candidates across
MiniMax, GLM/Z.ai, OpenRouter, direct Anthropic/OpenAI, and local OpenCode
catalogue models, then let the Delegations UI model rank decide based on
review score, success rate, cost, and duration.

Default caps should be conservative:

- `research`: read-only, 80 tool calls, 15 min, output <= 4k tokens.
- `implement`: workspace write, 120 tool calls, 20 min, output <= 6k tokens.
- `test`: workspace write, 80 tool calls, 20 min, output <= 4k tokens.

### 3. Use Tasks as Context Pickup

Because Claude and Codex do not share native conversation state, the handoff
should live in mcplexer-owned state:

1. Parent creates or updates a `task__*` item with the handoff packet.
2. Delegation tool passes the task ID to the worker.
3. Worker writes result as task notes plus a mesh `result`.
4. Parent reviews the task result and integrates.

This makes pickup client-agnostic. Claude, Codex, OpenCode, and future clients
all read the same task/mesh/memory state.

### 4. Add Durable Client Guidance

Keep only a short trigger in always-loaded files:

- `CLAUDE.md` for Claude Code.
- `AGENTS.md` for Codex.

Put details in this doc or in a future skill so the common path does not inflate
the parent context.

### 5. Keep the Escape Hatches Explicit

Native Claude/Codex subagents are useful fallback tools, but they are not the
primary solution:

- Claude Code subagents still spend Claude Code tokens/plan allowance, though a
  Haiku subagent is much cheaper than an Opus parent.
- Codex subagents still spend Codex credits/tokens, though GPT-5.4-mini or Spark
  can be cheaper than GPT-5.5.
- mcplexer Workers are better for cross-client pickup, central audit, per-worker
  budgets, and provider routing through opencode/local/API-compatible models.

## Implementation Notes

Remaining implementation hardening:

1. Add explicit allowlisted model-profile families for cheap code-cutter
   workers.
2. Add path-scope and task-kind controls if callers need read-only or patch-only
   modes.
3. Return stable run IDs from the async dispatch path once the runner can expose
   them immediately.
4. Add worker templates for common MiniMax, GLM/Z.ai, Haiku, and mini profiles.
5. Add policy caps for global delegation budgets and maximum fan-out.

Suggested worker return schema:

```json
{
  "status": "success|blocked|failed",
  "summary": "short result",
  "files_changed": ["path"],
  "tests_run": [{"command": "...", "result": "pass|fail|not_run"}],
  "risks": ["..."],
  "questions": ["..."],
  "diff_ready": true
}
```

## Immediate Operating Procedure

Until the public delegation tool exists:

1. Parent agent should do only enough exploration to define a good packet.
2. If already in an admin-trusted mcplexer context, use
   `mcplexer__spawn_subagent` with a cheap model and a tight allowlist.
3. If not in admin context, create a `task__*` item and ask the user/operator to
   run the worker from the dashboard or admin context.
4. Avoid dumping large search output or logs into the parent session. Pre-filter
   with shell tools or ask a worker to summarize.

This is intentionally conservative. Exposing worker spawn broadly is the right
feature, but exposing the current admin spawn surface broadly would weaken the
CWD trust boundary that mcplexer is built around.
