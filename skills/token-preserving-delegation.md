---
name: token-preserving-delegation
description: Preserve expensive parent-model context by delegating token-heavy coding, search, audit, or test work to cheaper MCPlexer workers. Use when Claude Opus, Codex/GPT, or another high-cost caller should keep strategy and review while cheaper models such as MiniMax, GLM/Z.ai, OpenCode, or CLI-backed workers execute bounded work; includes handoff sizing, model/profile selection, savings accounting, parallel fan-out, polling, and parent scoring of results.
---

# Token-Preserving Delegation (delegation-first)

Delegation is the default execution path, not an optional optimisation. Use MCPlexer delegation whenever the parent model should stay strategic and avoid burning context on broad code reading, repetitive edits, test loops, or parallel investigation. When in doubt, delegate, then review and score the result.

## Economics Guardrails (first-12h ledger audit, 2026-06-11)

The first 12 hours at full integration ran 100 delegations and lost money on the books: $59.81 worker spend vs $39.93 frontier cost avoided. Four rules fix that:

1. **Never use a frontier model as a worker for execute work — this is a hard default, not a preference.** Frontier-class means the top tier: Claude Opus, Claude Fable, Claude Mythos, GPT-5.5 (incl. its high/reasoning variants), o1-class, and any model priced at/above ~$5/M input or ~$25/M output. claude_cli/claude-opus-4-8 alone was 10 of 100 runs but $57.94 of the $59.81 spend (97%), averaging 6.6M input tokens per run. Its avg score (90) was only ~10 points above free-tier glm-5.1 (78) and openrouter minimax-m3 (86) — a gap that parent review closes for free. **Default to workhorse models** (glm, minimax, deepseek, sonnet) for execute work. The ONLY sanctioned exceptions: a frontier model as a read-only judge / final-reviewer where the verdict itself is the deliverable (and even then prefer one judge over N), or a genuinely exceptional task where no workhorse model can do the work and you can articulate why. The gateway now returns a non-blocking `warnings` array on `mcpx__delegate_worker` when you pick a frontier worker for execute mode — if you see it, you are almost certainly doing the wrong thing; switch models unless you're in one of the two exceptions above. (review-mode delegations are exempt from the warning — the frontier-as-judge case is the point.)
2. **Don't race your worker.** 19 of 100 delegations were reviewed "superseded" — the parent landed the same fix itself while the worker ran, paying twice (one worker burned 2.7M tokens and 25 minutes for a 74 "partial/superseded"). Once you delegate a slice: declare its paths in the linked task's `meta.touches_files`, then either `mcpx__wait_for_delegation` or work a DIFFERENT slice. If the worker is too slow or off-track, cancel it explicitly — never silently duplicate it.
3. **Kill the cold start: scout once, brief many.** Workers averaged ~1.7M input tokens each because every one re-explored the repo from scratch (166M total worker input tokens in 12h). Before a multi-worker wave, run ONE cheap scout (or a few parent-side greps) that produces a repo brief: exact files + line anchors per slice, conventions, test commands. Paste the relevant brief slice into each handoff. Pinned-file handoffs are the single highest-leverage token saver in the system.
4. **Distill after review.** Reviews feed model rank automatically, but the *why* evaporates unless written down. After scoring a delegation <50 or >=90, save a one-line `memory.save({kind:"fact"})` naming the model, the task shape, and the cause (e.g. "free-tier models fail ambiguous multi-surface refactors — all 5 failures in the 12h window were that shape"). Recall these before choosing models for the next wave.

## Calling Convention (read before writing any snippet)

Tool names in this skill use the registry form `namespace__tool` (e.g. `mcpx__delegate_worker`, `task__list`, `mesh__receive`). Inside an `mcpx__execute_code` snippet the sandbox exposes the DOT form only — `mcpx.delegate_worker({...})`, `task.list({...})`, `mesh.receive({})`. Copying the double-underscore form into a snippet throws `ReferenceError: task__list is not defined` and wastes a turn. When unsure of a signature, run `mcpx__search_tools` with `detail: "full"` first.

## Failure Guardrails (observed 2026-06-11)

Most bad rows in the first live delegation waves were operational, not model-quality failures: immediate OpenCode `Session not found`, adapter `signal: killed` / `context canceled`, shared OpenCode state collisions during fan-out, empty-output launch failures, and stale branches reviewed with misleading diffs. Treat these as process signals and tighten the next wave instead of abandoning delegation.

- **Classify before retrying.** A run with zero/near-zero tool calls, empty output, no branch/commit, `Session not found`, adapter killed, or provider reset is an operational launch/runtime failure. Review it as rejected with explicit operational notes, append the task note, and retry once with a corrected profile/endpoint/provider or narrower handoff. Do not score it as evidence that the model cannot code.
- **Do not repeat the same failed shape.** If OpenCode failed at launch, switch to the managed local endpoint/profile or a different provider. If many workers died while sharing one endpoint, reduce the wave, stagger launches, or use one worker per slice. If a worker was killed mid-run, keep the next handoff smaller and require earlier task notes.
- **Use clear-head handoffs.** Workers should receive the smallest context and tool surface that can finish the job: exact files, exact commands, no broad chat history, no raw logs unless needed, no irrelevant tools. Broad handoffs create expensive re-reading and increase failure probability.
- **Budget generously but checkpoint.** Harsh caps lose work and money. For deep implementation/review, prefer 200-250 tool calls and a 3600s wall clock, with a required task note in the first ~20 tool calls and at each milestone. Budgets bound runaway work; they are not a substitute for scope.
- **Review stale branches with merge-base, not direct main diffs.** Direct `git diff main..old-branch` can look like massive deletions after main has moved. First inspect `git log $(git merge-base main <branch>)..<branch>` and `git diff --stat $(git merge-base main <branch>)..<branch`. If useful work is based on old main, launch a current-head fix-forward worker or cherry-pick the isolated commit after reviewing conflicts.
- **Close the ledger loop.** Every failed/superseded delegation needs a review score and a task note naming the replacement delegation or reason blocked. Every accepted result needs branch, SHA, verification, and task closure. Unreviewed `needs_review` rows and stale `doing` tasks are coordination debt.

## Workflow

1. Decide whether delegation is worth it. Default answer: yes.
   - Delegate token-heavy execution: codebase scans, implementation passes, test writing, fixture generation, log triage, migration drafting, mechanical edits, and parallel investigation. If the architecture and acceptance criteria are clear, the work belongs in a worker.
   - Keep parent-owned work in the caller: architecture calls, high-stakes judgment, final merge decisions, secret handling, worker decomposition, and result scoring.
   - Workers must use isolated git worktrees and must not touch `~/.mcplexer/` (DB, logs, secrets, p2p, backups). All config/state operations go through MCP tools, never raw SQL or direct file access.

2. Create a compact handoff packet.
   - Include the objective, exact files or modules if known, constraints, acceptance criteria, tests to run, and what to report back.
   - Prefer links, task IDs, grep terms, failing command output, and relevant snippets over dumping broad context.
   - State file ownership for parallel workers so they do not duplicate edits.
   - Include machine-local constraints the worker cannot discover cheaply: shell guards (e.g. a PreToolUse hook that hard-blocks `;`, `|`, `&`, backticks, and newlines in Bash — one command per call), sandbox/network limits, and repo-specific hooks. Harness subagents inherit CLAUDE.md; CLI-backed mcplexer workers do NOT — any constraint missing from the handoff is invisible to them and costs blocked turns.

3. Estimate the baseline before spawning.
   - `baseline_tokens_estimate`: incremental expensive parent-model tokens avoided for the delegated slice from this point forward.
   - `baseline_cost_usd`: estimated expensive parent-model cost avoided for that delegated slice; include it whenever model pricing is known.
   - `parent_input_tokens`, `parent_output_tokens`, and `parent_cost_usd`: what the parent has already spent getting to the decision point. Treat these as sunk ledger values, not as costs to subtract from delegation ROI.

4. Choose the cheap worker model/profile.
   - Prefer `model_profile_id` when a MCPlexer model profile exists.
   - Otherwise set `model_provider`, `model_id`, `model_endpoint_url`, and `secret_scope_id`.
   - For OpenCode-backed cheap models, use `model_provider: "opencode_cli"` and a concrete configured `model_id` such as `minimax/MiniMax-M3` or `zai-coding-plan/glm-5.1`.
   - For xAI-backed Grok CLI workers, use `model_provider: "grok_cli"` and a concrete `grok -m` model id such as `grok-build`.
   - Known grok_cli failure mode (2026-06-11, bug filed): workers die with `adapter send: grok_cli: run: signal: killed` when one of their `mcplexer__execute_code` calls returns `tool_error: tool_output_error` — concurrency is NOT the trigger (3-way and 7-way fan-outs died identically while earlier same-day runs succeeded). Until fixed, prefer `opencode_cli` for workers that do heavy MCP tool I/O, and score infra kills as operational-rejected with a note so the model rank isn't polluted.
   - `max_wall_clock_seconds` is honoured literally — there is NO hidden runner cap. If omitted, `mcpx__delegate_worker` and `mcpx__invoke_model` default to 3600 seconds (60 minutes). An earlier note here wrongly claimed a hard 15-minute ceiling; the worker that died at "wall-clock 15m0s exceeded" had simply been given `max_wall_clock_seconds: 900` (= 15m). Always pair a long cap with the incremental-evidence rule: first task note within ~20 tool calls, updates per section — so a reap or crash still leaves findings on the ledger.
   - CLI-backed providers (grok_cli, opencode_cli, claude_cli) may omit usage/cost/tool-call fields from their JSON, so mcplexer can record zero tokens, zero cost, and zero tool calls for a successful run. Treat any zero as missing CLI accounting, not authoritative spend — never cite zero-cost runs as evidence a model is free, and rank models by review score; use cost/duration only from runs with non-zero accounting.
   - Prefer an OpenCode server endpoint such as `http://127.0.0.1:4096` for `model_endpoint_url`; raw parallel `opencode run` processes can collide on OpenCode's local SQLite state, while `opencode run --attach` shares one long-lived server.

5. Spawn workers with `mcpx__delegate_worker`.
   - Dedup first: `task__list({q: "<objective keywords>", state: "any"})` and a scan of `mcpx__list_delegations` — an equivalent task may already be done or in flight (parallel agents here have independently re-implemented the same feature). Link the delegation to the existing task id instead of creating a twin.
   - Use `parallelism: 1` for one implementation pass.
   - Use higher parallelism for independent slices only; include a split plan in `handoff`.
   - Set `worker_mode: "review"` for read-only critique/audit runs and `worker_mode: "execute"` for implementation runs.
   - Leave `review_required` omitted/false for routine delegations; set `review_required: true` only when the parent review must gate completion or when model-ranking telemetry is explicitly worth the review cost. Required reviews keep the delegation in `needs_review` until scored.
   - Set caps (`max_tool_calls`, `max_wall_clock_seconds`, token caps, monthly cost cap) so failures are bounded — but size them to the work (see "Work the Delegates Hard"); the default wall clock is 3600 seconds for coding workflows, while the default 80 tool calls still only fits small bounded tasks.
   - Memory is available by default for execute workers (via the two-tool surface): the worker preamble directs persistence to the `memory` namespace; use `mcpx__search_tools` (or harness alias) for "memory" then `execute_code` with `memory.save({...})` / `memory.recall({...})` / `memory.list` (see docs/memory.md and CLAUDE.md for exact call forms and harness naming). Attach a skill (e.g. this one or a memory-usage example skill) for body pickup if the worker needs extra patterns or project facts. Review workers get read-only memory tools. No secret leakage — memory ops are server-side under the delegation's workspace scope.

6. Poll with `mcpx__list_delegations`.
   - Watch the context tree: parent context, worker contexts, run status, token use, cost, tool calls, and duration.
   - Prefer reading concise worker handoffs over re-reading everything they inspected.
   - Check `model_stats` and the Delegations UI model rank to see which cheap models are actually performing.

7. Review the result as the parent when useful.
   - Inspect diffs, tests, and worker final reports.
   - Verify the worker's reported branch/commit/worktree before trusting the handoff. A worker can report success while leaving only dirty files, pointing at a stale SHA, or editing the parent worktree directly.
   - Penalize broken isolation, missing commits, stale-base branches, and reports that do not match git state. Salvage useful code when safe, but score the delegation as partial/rejected so model ranking learns the operational failure.
   - Call `mcpx__review_delegation` with a `score` from 0 to 100 and notes when review was required or when the judgement should feed model ranking, safety, merge readiness, or user-visible quality.
   - Use outcomes: `accepted` for >=80, `partial` for 50-79, `rejected` for <50 unless there is a reason to override.
   - Score exploration delegations when the comparison matters, including failures. A database/config failure can get a low score with notes so the model rank shows operational reliability, not just happy-path quality.
   - Launch failures are NOT model failures. When the worker never ran (e.g. `opencode_cli` dies before the first turn, adapter `signal: killed`), score it rejected with an explicit "launch failure / operational" note and do NOT treat the number as evidence about the model's coding quality — two such runs in the 12h audit put score-20 rows against an innocent model. Until the gateway separates operational outcomes from model_stats (task 01KTTVV1G4), the note is what keeps capacity ranking honest.
   - When `review_required: true`, review in the SAME session that spawned the delegation, immediately after reading the result. Optional delegations may still be scored later when the parent has useful judgement to record.
   - Before ending any session, sweep: `mcpx__list_delegations` and handle anything still `needs_review` that you own. Optional completed delegations do not need a parent score just to close.

## Work the Delegates Hard

Parent thrift is NOT worker thrift. The entire point of delegation is that worker tokens are cheap — so scale the WORK up, not down. A cheap worker given a timid, sampled, "spot-check a few pages" objective wastes the dispatch overhead and returns a report too thin to act on; the parent then re-does the work expensively, which is the worst outcome.

- Write maximal objectives: "cover EVERYTHING listed, do not sample or skim, bring evidence (numbers, exact URLs, header values, file:line) for every claim." Enumerate the full coverage surface explicitly — page types, endpoints, files — so "done" is checkable.
- Raise the caps to match: deep audits and implementation passes usually want `max_tool_calls` 200–250 and the default `max_wall_clock_seconds` 3600, plus generous output-token caps. Keep caps finite — they bound failures, not effort.
- Demand artifacts, not vibes: Lighthouse JSON, axe violation lists, timing matrices, test output, committed branches. A worker that ran tools 200 times produces evidence; one that ran 20 produces opinions.
- The counterweight is review cost, not worker cost: require a structured, severity-ranked summary at the end of every report so the parent can score it cheaply. Hard work with an unstructured dump still loses.
- Hard work ≠ unbounded scope: the objective still names exact targets and a "do not touch" list. Effort scales inside the fence, never across it.

## Exploration Pattern

When choosing cheap code-cutter models, run a bounded comparison instead of guessing:

- Use the same handoff packet for 2-4 single-worker delegations across `minimax/MiniMax-M3`, `zai-coding-plan/glm-5.1`, and one OpenRouter candidate.
- Keep each run small enough that parent review is cheap.
- Score all results, then prefer models with high review score, high success rate, low cost, and low average duration in the Delegations UI model rank.
- Repeat exploration when provider catalogues change; do not keep using an older model solely because it was once the default.

## Savings Rubric

Treat delegation as successful when quality stays high and the ledger shows real savings:

- `frontier_tokens_avoided = baseline_tokens_estimate`
- `estimated_parent_tokens_saved = frontier_tokens_avoided`
- `combined_tokens = parent_tokens + worker_tokens`
- `worker_token_delta = baseline_tokens_estimate - worker_tokens`
- `net_tokens_delta = worker_token_delta`
- `estimated_cost_saved_usd = baseline_cost_usd - worker_cost_usd`

Parent tokens and parent cost are still shown so the context tree is auditable, but they are sunk before delegation. Do not judge a cheap worker run by raw cross-model token count alone; MiniMax/GLM tokens can be much cheaper than Codex/Opus tokens. Use token deltas as context/noise telemetry and cost saved as the business metric.

Interpretation:

- Strong win: review score >=80 and estimated cost saved is positive.
- Partial win: review score 50-79, or cost was saved but parent cleanup remains.
- Failed delegation: review score <50, duplicate work, missing tests, or negative cost savings without a quality reason.

## Mesh Reply Hygiene

Workers publish their final output to the mesh as `delegation_reply` and `kind: finding` messages. These exist for the PARENT to consume:

- After polling, call `mesh__receive` and resolve each worker `delegation_reply`/`finding` — usually read-and-ignore once the delegation is reviewed, since the durable copy already lives on the delegation record.
- The message body lives in `messages[].preview` (there is no `.content` field in the receive envelope); use `mesh__hydrate({message_id})` for full content. Reading the wrong field looks like an "empty message" bug — it isn't.
- Do not leave worker messages unread at session end. A backlog of unread worker output means reviews were skipped (a 100-message `delegation_reply` backlog and a 37-message `finding` backlog have both been observed on this gateway).
- Never re-broadcast worker output to the mesh. Reply in-thread only when a peer explicitly asked for the result.

## Current-Head Integration Lessons

- For stale but useful branches, launch a current-head rebase/fix-forward worker instead of merging old work that would undo newer safety fixes.
- If the parent checkout is dirty with in-scope WIP, preserve it first (`git stash push -u -- <exact paths>` or equivalent) before merging a verified branch that supersedes it. Do not include unrelated dirty files.
- Small/free models need explicit "no new paid model IDs", "no unrelated files", and "commit or report blocked" constraints. They can be useful for tiny edits, but every result must be verified against actual git state.
- A report that says "tests pass" is not evidence unless the parent can find the commit or rerun the relevant command after integration.
- When a worker touches frontend source, plan a final `npm run build`/embedded-dist refresh after all frontend source changes land; avoid committing dist churn from every small worker.

## Codebase Scout Delegations

Use a scout when the expensive parent needs facts from a codebase but should not spend frontier context reading the tree. Scouts are not vague "explore this repo" workers; they answer one bounded question with evidence.

- Scope the question to exact paths, symbols, commands, or search terms, and require file:line references for every claim.
- Prefer `worker_mode: "review"` for pure read-only scouting. If the scout must persist reusable findings, keep the scope read-only but provide an explicit allowlist that includes `memory__save` and `task__append_note`.
- Ask for structured facts, not prose dumps: 5-15 findings, each with `file:line`, what it means, confidence, and the next parent action.
- Forbid full file contents, broad file listings, unrelated refactors, and `~/.mcplexer` access.
- Score low for hallucinated references, sampled coverage when exhaustive coverage was requested, or unstructured output that forces the parent to re-read the same files.

Scout handoff skeleton:

```text
Objective:
Find <specific fact/pattern> in <exact paths>.

Known context:
- task: <task id>
- search terms: <symbols/strings>
- known files: <file:line refs, if any>

Acceptance:
- Return 5-15 file:line findings, no full file contents.
- Say what each finding proves and what the parent should do next.
- Append concise task notes or save memory only if explicitly allowed.

Do not:
- Edit files.
- Touch ~/.mcplexer/.
- Return raw command dumps or broad file inventories.
```

## Worker Handoff Template

```text
Objective:
<one outcome, not a vague role>

Context:
<task id, files, commands, relevant snippets, constraints>

Environment:
<machine-local constraints: shell guards, sandbox, network, hooks>

Acceptance:
<tests, behavior, output format>

Parallel split:
<only if parallelism > 1>

Do not:
<boundaries, files to avoid, decisions reserved for parent>
```

## Parent Final Check

Before calling the task done, confirm:

- Worker output is scored and recorded.
- Savings fields are visible in `mcpx__list_delegations` or the Delegations UI.
- The final user response separates what the parent decided from what workers executed.
- No delegation you own is left in `needs_review`, and your mesh inbox holds no unread `delegation_reply`.
