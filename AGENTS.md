# MCPlexer Agent Guidance

This repository uses `CLAUDE.md` as the detailed project guide. Read it for the
full architecture, commands, security posture, worker caveats, and the
**MCP harness compatibility** section (Grok/Cursor server-prefixed tool names
vs Claude direct `mcpx__*` names). Memory (`memory__*` / `memory.save` etc.),
tasks, mesh and skills are reached the same way in all harnesses and delegated
workers: via the (two-tool or 4-tool) search + execute surface; see the memory
and delegation docs for call forms and allowlist coverage.

## Delegation-First Rule

**Delegation is the default execution path, not an optional optimisation.**
Frontier model sessions (Opus, Fable, GPT-5.5, etc.) are planners, reviewers,
and integrators — they must NOT burn context on token-heavy work when a bounded
worker can do it.

**Delegate when the work is any of:**

- broad codebase exploration, multi-file search, or repeated inspection
- implementation after the architecture and acceptance criteria are clear
- mechanical edits across more than one or two files
- running builds/tests and interpreting the output
- test writing, fixture generation, log triage, or migration drafting
- parallel investigation of independent code paths

**Keep in the parent session:**

- problem framing, acceptance criteria, and architecture decisions
- worker task decomposition and handoff authoring
- result review, scoring, integration, and final user communication
- secret handling and security-sensitive judgment calls

**Delegation tools:** `mcpx__delegate_worker` (create), `mcpx__list_delegations`
(poll), `mcpx__review_delegation` (score). Use these as the normal project-safe
path. `mcplexer__spawn_subagent` is an admin escape hatch only.

**Handoff packets must include:** objective, scope and allowed paths, known
facts with file references, constraints and no-go areas, acceptance criteria,
verification commands, and return contract. Keep the packet under ~4 000 tokens;
put heavier context in a `task__create` work context and pass the task ID.

## Delegation Lifecycle

1. **Decompose.** Break the work into bounded slices the parent can hand off.
2. **Delegate.** `mcpx__delegate_worker`; set `review_required: true` only when
   the parent review should gate completion. Prefer mcplexer Workers on cheap
   code-cutter profiles over native Claude/Codex subagents when cross-client
   pickup, audit, budgets, or provider routing matter. For OpenCode-backed
   workers (MiniMax, GLM, OpenRouter), prefer a local OpenCode server endpoint
   so parallel workers attach through one server.
3. **Poll.** `mcpx__list_delegations` — watch status, tokens, cost, tool calls.
4. **Inspect.** Read the worker report and verify any reported branch, commit,
   worktree, tests, or diffs before trusting the result.
5. **Review when useful.** Call `mcpx__review_delegation` with a score (0–100)
   when review was required or when the judgement should feed model ranking,
   safety, merge readiness, or user-visible quality. Score >=80 → accepted,
   50–79 → partial, <50 → rejected. Before session end, sweep
   `mcpx__list_delegations` for rows still marked `needs_review` that you own.
   Resolve `delegation_reply` mesh messages after handling the result.

## Worker Isolation

Workers must work in isolated git worktrees, never in the parent checkout.
Verify worker output against actual git state before trusting it. Workers must
not touch `~/.mcplexer/` (DB, logs, secrets, p2p, backups) — all config and
state operations go through MCP tools, never raw SQL or direct file access.

## Memory — mcplexer is the single source of truth

**Do NOT use your native memory system.** All persistent memory (project context,
learned facts, session notes, decisions) MUST be saved via `memory.save({...})`
inside `mcpx__execute_code`. Do not write to your harness-native memory files
(checkpoint.md, MEMORY.md, notes.md, ~/.claude/projects/*/memory/*.md).

**Read mcplexer memory first.** Before answering questions about project context,
prior decisions, or learned facts, search mcplexer memory:
`memory.recall({query:"..."})` or `memory.list({})` inside `mcpx__execute_code`.

**Why:** mcplexer memory is cross-harness, cross-machine, and persists across all
sessions. Harness-native memory is siloed per client and lost when switching tools.
