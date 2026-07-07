# MCPlexer Agent Guidance

This repository uses `CLAUDE.md` as the detailed project guide. Read it for the
full architecture, commands, security posture, worker caveats, and the
**MCP harness compatibility** section (Grok/Cursor server-prefixed tool names
vs Claude direct `mcpx__*` names). Memory (`memory__*` / `memory.save` etc.),
tasks, mesh and skills are reached the same way in all harnesses and delegated
workers: via the (two-tool or 4-tool) search + execute surface; see the memory
and delegation docs for call forms and allowlist coverage.

## Delegation — use workers where they win

Workers run bounded agents on cheaper models in isolated git worktrees. They
win when the parent only needs the conclusion of the work, not the output —
and they lose when handoff + review costs more than doing the work directly
(see `skills/token-preserving-delegation.md` for the measured economics).
Delegation is a tool, not a mandate.

**Workers tend to win when the work is:**

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

## Memory — prefer the gateway store

mcplexer memory (`memory.save` / `memory.recall` inside `mcpx__execute_code`) is
cross-harness, cross-machine, and survives every session; harness-native memory
files are siloed per client. Recall when past sessions may have settled a
question; save decisions with rationale, preferences, and project facts not
derivable from the repo. Knowledge that should outlive this client belongs in
the gateway store, not in harness-local memory files.
