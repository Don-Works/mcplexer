# Near-Autonomous Coding With MCPlexer

This guide describes the safe operating loop for coding work that is mostly
handled by workers, but still reviewed and steered by a frontier parent session.
It is deliberately an operator workflow, not a new autonomous mode.

The useful mental model:

- Tasks are the durable work ledger.
- Delegations are bounded worker executions against a task or handoff packet.
- Mesh is the peer-to-peer coordination channel for colleagues and other agents.
- Memory stores durable project facts and decisions that are not obvious from
  the repository.
- The parent agent plans, decomposes, reviews, and records decisions.
- Workers search, edit, test, summarize, and stop at the return contract.

## Current Capability

MCPlexer already has the pieces needed for an almost-autonomous coding loop:

- `mcpx__delegate_worker` launches bounded one-shot Workers with caps, model
  selection, task kind, allowlists, and optional parent review.
- `mcpx__list_delegations` shows worker runs, status, model, token/cost
  accounting, output, and review state.
- `mcpx__review_delegation` records the parent review score and feeds model
  quality ranking.
- `mcpx__list_delegation_model_capacity` shows the registered model candidates
  capacity mode can select.
- `task__create`, `task__claim`, `task__update`, and `task__append_note` provide
  a durable task ledger across harnesses.
- `mesh__receive` and `mesh__send` support agent and peer messaging on the local
  daemon and paired daemons.
- P2P transport supports direct, hole-punched, and relay-backed peer
  connections. See [p2p-network-modes.md](p2p-network-modes.md).
- Worker surfaces stay narrow: workers get the search/execute gateway and their
  configured inner allowlist, not unrestricted admin access.

What this does not do by itself:

- It does not merge code without a review gate unless an operator builds that
  policy outside the core delegation path.
- It does not invent missing model capacity. Capacity mode needs registered
  model profiles or enabled worker-backed model candidates.
- It does not replace human judgement for destructive actions, secrets,
  production data, releases, or cross-repository coordination.

## Readiness Checklist

Before asking a parent agent to run the loop, check these items.

1. Workspace routing is correct.
   The harness CWD must resolve to the intended MCPlexer workspace. If the wrong
   workspace is selected, tools, secrets, task visibility, and policies will be
   wrong.

2. Model capacity exists.
   Run `mcpx__list_delegation_model_capacity` before using
   `model_selection_mode:"capacity"`. If it returns no rows, create a model
   profile in `Workers > Model Profiles`, create an enabled worker with a
   provider/model, or pass `model_provider` and `model_id` explicitly.

3. CLI-backed providers are intentionally enabled.
   Providers such as `opencode_cli`, `grok_cli`, and `claude_cli` depend on
   daemon configuration and local CLI state. Prefer a local OpenCode server
   endpoint for OpenCode-backed parallel workers so they attach to one server.

4. Tasks have enough specification.
   A task should include the objective, allowed paths, constraints, acceptance
   criteria, verification commands, and return contract in the description or
   work context. Do not rely on chat history as the only specification.

5. Worker handoffs are bounded.
   Good handoffs name allowed paths, no-go areas, budget caps, expected tests,
   and what the worker must return. Keep the packet compact and store larger
   context on the task.

6. Review remains explicit.
   Use `review_required:true` for implementation work unless the task is a
   known-safe automation. Parent sessions should inspect diffs, test output, and
   worker claims before recording a successful review.

7. Peer messaging has an owner.
   Mesh messages read by an agent must be resolved as ignore, reply,
   promote-to-task, or do-it-and-reply. Decisions that matter later belong in
   tasks or memory, not only in a mesh message.

8. Secrets and local state stay behind MCP tools.
   Workers and agents should not read or write `~/.mcplexer` directly. Use MCP
   tools for config, tasks, memory, secrets, workers, and mesh state.

## Operating Loop

Use this loop for large coding work:

1. Orient.
   Recall relevant memory, read the task, check pending mesh messages, and
   inspect the repository only enough to frame the work.

2. Decompose.
   Split the work into small, reviewable slices. Each slice should have a clear
   pass/fail condition and a test command or reason tests are unavailable.

3. Delegate.
   Create one bounded delegation per slice. Prefer `worker_mode:"execute"` for
   implementation and `worker_mode:"review"` for a read-only audit pass. Set
   caps for wall clock, tool calls, and tokens.

4. Poll.
   Use `mcpx__list_delegations` to watch state. Do not treat worker output as
   truth until the parent has inspected the resulting files, branch, commits, or
   test output.

5. Review.
   Record the result with `mcpx__review_delegation`. Scores above 80 mean the
   work is accepted, 50-79 means partial, and below 50 means rejected or needs
   another pass.

6. Coordinate.
   If a colleague or peer agent needs to know, send a mesh message with the
   decision and task/delegation IDs. If the message creates new work, create or
   update a task.

7. Continue or stop.
   Continue only when the next slice is still bounded. Stop and ask for human
   input when requirements, credentials, production effects, or merge policy are
   unclear.

## Handoff Template

```markdown
## Objective
One sentence describing the desired outcome.

## Scope
Allowed files/directories. Explicitly name out-of-scope files and behaviours.

## Known Facts
Facts the parent verified, with file paths, task IDs, or command outputs.

## Constraints
Security rules, compatibility requirements, style constraints, and no-go areas.

## Acceptance Criteria
Concrete checks the parent can verify.

## Verification
Commands to run, or a clear reason tests should not be run.

## Return Contract
Return files changed, tests run, result status, risks, and unresolved questions.
Do not paste raw logs unless requested.
```

## Safe Defaults

- Use `review_required:true` for code changes.
- Use capacity mode only after checking capacity rows.
- Use `worker_mode:"review"` for unfamiliar areas before delegating edits.
- Use task notes for progress updates and final decisions.
- Use memory for durable facts that future sessions should recall.
- Prefer explicit model/profile selection when diagnosing provider failures.
- Keep destructive operations, releases, billing, and secrets behind human
  approval.

## Common Failure Modes

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `delegation model required` | No model profile, provider/model, or candidate was supplied | Pass `model_profile_id`, pass `model_provider` and `model_id`, or register capacity candidates |
| `no registered delegation model candidates` | Capacity mode has nothing to pick from | Create a model profile, create an enabled worker-backed model candidate, or pass an explicit model |
| Worker appears to ignore task context | Spec lives only in chat | Put objective, constraints, and acceptance criteria in the task description or work context |
| Peer messages vanish into chat | Decisions stayed in mesh only | Promote durable decisions to task notes or memory |
| Capacity keeps picking a poor model | Missing reviews or failure accounting | Review delegations and check model capacity rows for quarantines, low scores, or missing telemetry |

## Related Docs

- [token-preservation-delegation.md](token-preservation-delegation.md)
- [cheap-provider-profiles.md](cheap-provider-profiles.md)
- [p2p-network-modes.md](p2p-network-modes.md)
- [memory.md](memory.md)
