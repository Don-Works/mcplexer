# Opencode-as-Harness Pilot Plan — mcplexer-heavy sessions

**Date:** 2026-06-02
**Decision under test:** Is `opencode` a better day-to-day harness than Claude Code for sessions whose center of gravity is the mcplexer gateway (`mcpx__*`, `mesh__*`, `task__*`, `memory__*`, `secret__*`, `skill__*`)?

The bet, stated plainly: opencode shells through its own provider config (Anthropic, OpenRouter, Minimax, LM Studio, MLX, ~30 backends), so a successful pilot decouples "which model" from "which harness" and lets mcplexer be the constant. mcplexer already runs as a local daemon exposing an MCP server; opencode is an MCP client; the two should connect with no gateway changes. This plan tests that bet without betting the farm.

---

## 1. Scope — what to migrate, what to leave on Claude Code

### Migrate to opencode first (high mcplexer-leverage, low harness-specific risk)
These session types lean on the gateway, not on Claude-Code-only features, so they isolate the variable we actually care about:

- **Code-mode / discovery-heavy work** — sessions dominated by `mcpx__search_tools` → `mcpx__execute_code` against downstream MCP servers (github, linear, clickup, postgres, vercel, customer, …). The slim 4-tool surface is gateway-side, not harness-side, so it should behave identically and is the cleanest A/B.
- **Mesh / multi-agent coordination** — discover mesh/task signatures with `mcpx__search_tools`, then call `mesh.receive(...)`, `mesh.send(...)`, `task.offer(...)`, and `task.assign_remote(...)` inside `mcpx__execute_code`. Provider-agnostic by design; good stress test of whether opencode keeps a stable MCP session for inbound mesh delivery.
- **Task-ledger-driven execution** — claim → heartbeat → review → done loops where the work is "drive the ledger and call downstream tools," not "deeply edit a large local repo." This directly probes lease/heartbeat behavior under a different harness.
- **Memory + skill registry ops** — `memory__recall/save`, `mcpx__skill_search/get/publish`. Read/write to the gateway, no harness dependence.
- **Cheap-model-friendly chores** — routine triage, status sweeps, label/issue hygiene, where opencode's ability to route to OpenRouter/Minimax/local (LM Studio/MLX) could cut cost substantially vs always paying for a frontier model.

### Leave on Claude Code (for now)
- **Heavy local-repo engineering on this very codebase (mcplexer itself)** — multi-file Go/TS refactors leaning on Claude Code's editing ergonomics, plus the **dev-mode hook escape hatch**: the `block-mcplexer-db.sh` PreToolUse hook only lifts the DB-lockdown when `CLAUDE_PROJECT_DIR` is the mcplexer repo/worktree. That hook is Claude-Code-specific; opencode has no equivalent wired, so legitimate gateway-internals debugging stays on Claude Code until we confirm an opencode-side guard exists.
- **Admin / CWD-gated gateway config** (`mcplexer__*`, `mcpx__provision_mcp/reload_server`) — only resolves under `~/.mcplexer`. Low volume; not worth re-validating the gating semantics on a new harness during the pilot. Verify once, late.
- **Sessions depending on Claude-Code-only harness skills** — the local `~/.claude/skills/*` slash commands and harness hooks. mcpx-registry skills are portable; local ones are not.
- **`secret__*` interactive flows** — `secret__prompt` must work outside the sandbox to stay top-level; confirm opencode surfaces the blocking prompt before trusting it with credentialed sessions.

**Principle:** migrate the sessions where mcplexer *is* the work; keep on Claude Code the sessions where the *harness* is the work.

---

## 2. Wiring — point opencode at the running mcplexer daemon

mcplexer's gateway speaks MCP. The robust transport is **stdio** (spawn `mcplexer serve` as the MCP server), which matches how Claude Code is wired today and avoids socket/HTTP-MCP ambiguity. The daemon (`com.mcplexer.daemon`, launchd) holds the durable state — DB, mesh, leases; the per-session `mcplexer serve` process is the MCP stdio bridge into that daemon.

> ⚠️ **Unknown to confirm at kickoff:** the exact opencode MCP-server config key/schema and whether opencode prefers stdio vs an HTTP/SSE MCP endpoint. Confirm against the installed opencode version's docs before pasting config — do **not** trust a schema from memory. The two shapes below are the canonical local stdio and remote/HTTP patterns; adapt field names to opencode's actual schema.

### 2a. Stdio wiring (preferred — mirrors the Claude Code setup)
In opencode's MCP config (project- or user-level), register mcplexer as a local stdio server. Reuse the **exact command + args** Claude Code already uses — read them from the working Claude Code MCP client config (`mcplexer setup` wrote them) rather than inventing:

```jsonc
// opencode MCP config (shape illustrative — verify keys against installed opencode)
{
  "mcp": {
    "mcplexer": {
      "type": "local",
      "command": ["<path>/mcplexer", "serve"],   // copy from working Claude Code config
      "environment": {
        // slim surface is the gateway DEFAULT (v0.20+): only 4 tools advertised.
        // Leave unset to inherit the default; set explicitly only to override.
        // "MCPLEXER_SLIM_SURFACE": "true"
      },
      "enabled": true
    }
  }
}
```

### 2b. HTTP/SSE wiring (fallback if opencode can't manage the stdio lifecycle cleanly)
If opencode's stdio supervision proves flaky, point it at the daemon's MCP-over-HTTP endpoint instead (if exposed) — local-loopback only, never bound off-box:

```jsonc
{
  "mcp": {
    "mcplexer": { "type": "remote", "url": "http://127.0.0.1:3333/<mcp-endpoint>", "enabled": true }
  }
}
```
*(The dashboard is `http://localhost:3333`; whether an MCP-protocol HTTP endpoint is mounted there must be confirmed — the documented, supported transport is stdio.)*

### 2c. The slim-surface code-mode workflow (must work identically)
This is the load-bearing acceptance check, because it's the whole value prop. After wiring, the opencode session must see **exactly 4 top-level tools** and drive everything else through code mode:

1. `tools/list` returns only: `mcpx__execute_code`, `mcpx__search_tools`, `secret__prompt`, `secret__list_refs`. **If opencode shows the wide surface, slim mode isn't being inherited — stop and fix before measuring.**
2. Discover: `mcpx__search_tools({ queries: ["task create","github list issues"], detail: "full" })` → get TS signatures.
3. Execute: one `mcpx__execute_code` snippet batching related downstream calls, e.g.
   ```js
   const issues = github.list_issues({ owner: "don-works", repo: "mcplexer", state: "open" });
   print(issues.length, issues.slice(0,5).map(i => i.title));
   ```
4. Confirm auto-unwrap works in opencode: snippet reads `result.id` directly (parsed object), **not** `JSON.parse(result.content[0].text)`. If opencode double-wraps or fails to unwrap, that's a blocker-class finding.
5. Confirm **mesh inbound delivery**: pending mesh messages are appended to tool results in opencode the same way (`[mesh: N pending message(s)]`), then `mcpx__search_tools` finds mesh receive/send signatures and `mcpx__execute_code` can call `mesh.receive(...)`.
6. Confirm **lease/heartbeat**: a `mcpx__execute_code` call can invoke `task.claim(...)` and set a lease; the session auto-heartbeats on tool calls; on opencode session end the gateway's `ReleaseSessionTasks` demotes working tasks to `open`. Verify the disconnect hook actually fires for an opencode session (it keys on session id, which is harness-agnostic, but must be observed, not assumed).

### 2d. Provider config (the reason to bother)
Set opencode's provider/model routing for the pilot cohort — e.g. Anthropic for parity-comparison runs, then a cheaper backend (OpenRouter / Minimax / local LM Studio or MLX) for the cost-sensitive chore cohort. Record which model each measured session used so cost/quality is attributable.
> Note: the `opencode_cli` *worker provider* inside mcplexer runs with **NetworkHost** egress and is gated behind `MCPLEXER_ALLOW_OPENCODE_CLI=1`. That gate is about mcplexer *spawning* opencode as a worker — **distinct** from this pilot, where opencode is the **outer harness** connecting in as an MCP client. The opt-in env is not required for the outer-harness path, but flag the distinction so no one conflates the two security postures.

---

## 3. Success & failure criteria (measurable)

Baseline first: run an identical workload basket on **Claude Code** and record the same metrics, so every number is a delta, not an absolute. Define the basket as ~20–30 representative mcplexer-heavy tasks (mix of code-mode, mesh, ledger, chore).

| Metric | How measured | SUCCESS (ship/expand) | FAILURE (rollback) |
|---|---|---|---|
| **Tool-call success rate** | gateway audit log (`mcplexer__query_audit`) — successful vs errored downstream + builtin calls per session | ≥ Claude Code baseline, and ≥ 95% absolute | < 90%, or any *systematic* class (unwrap, mesh delivery, secret subst) broken |
| **Code-mode integrity** | manual + audit: auto-unwrap correct, batching works, slim surface = 4 tools | 100% parity on the §2c checklist | any §2c item fails and isn't fixable in-pilot |
| **Latency (per tool call)** | timestamp deltas in audit log; p50 + p95 | p50 within +15% of baseline; p95 within +30% | p50 > +30% or p95 > +50% sustained |
| **Context-token savings** | harness-reported context size at session start + per-turn token usage; compare wide vs slim, and opencode vs Claude Code | slim surface yields the ~22k-token saving in opencode too; total ≤ Claude Code baseline | opencode bloats context such that net tokens > Claude Code despite slim mode |
| **Task-ledger discipline** | audit: ratio of work units that passed through dynamic `task.claim(...)`→`review`→`done`; count of zombie/abandoned `doing` left at session end; lease auto-release observed on disconnect | ≥ baseline discipline; **zero** un-released leases after opencode disconnect | lease auto-release doesn't fire for opencode sessions → zombie tasks (hard blocker) |
| **Cost per equivalent task** | provider billing / opencode usage × model; normalized to baseline task basket | ≥ 20% cheaper on the chore cohort via cheaper backends, with no quality regression | more expensive OR quality drops below "would re-run on Claude Code" bar |
| **Quality / rework rate** | human review: % of pilot tasks needing redo vs baseline | ≤ baseline rework rate | rework rate materially worse |

**Overall ship gate:** opencode wins or ties on tool-call success, code-mode integrity, ledger discipline, and quality, AND shows a real win on at least one of {cost, context tokens}. A tie everywhere = not worth the switching cost; keep Claude Code as default.

---

## 4. Time-boxed rollout (2 weeks)

**Pre-flight (Day 0, ~½ day):**
- Confirm installed opencode version + its MCP config schema (don't trust memory).
- Wire §2a, pass the entire §2c checklist on a throwaway session. **Gate:** if §2c fails, halt — fix or abandon before spending pilot days.
- Capture the Claude Code **baseline** on the task basket (metrics in §3).

**Phase 1 — Days 1–3: code-mode + discovery cohort (lowest risk).**
Run discovery-heavy and downstream-tool sessions on opencode. Daily: pull `mcplexer__query_audit`, log the §3 metrics, note any unwrap/slim anomalies. Keep Claude Code one keystroke away for fallback.

**Phase 2 — Days 4–6: mesh + task-ledger cohort.**
Add multi-agent coordination and claim/heartbeat/review loops. **Explicitly verify lease auto-release on opencode disconnect** (kill the session, confirm the task demotes to `open` with the "agent disconnected" history line). This is the highest-signal correctness check in the whole pilot.

**Phase 3 — Days 7–9: cost-routing cohort.**
Route the chore/triage cohort to cheaper backends (OpenRouter / Minimax / local). Measure cost delta + quality/rework. This is where the upside (if any) shows up.

**Phase 4 — Days 10–12: edge + parity.**
`secret__prompt` interactive flow; memory + skill-registry ops; one CWD-gated admin session under `~/.mcplexer` to confirm gating still resolves. Try one *light* local-repo edit session to gauge editing ergonomics vs Claude Code (informational, not a gate).

**Days 13–14: synthesis + decision.**
Compile metrics vs baseline, write the go/no-go against the §3 ship gate, record the decision (and rationale) to `memory__save` so it's mesh-visible and survives the session. Default-harness change for any cohort requires an explicit, recorded decision — not drift.

Throughout: every pilot work unit is a real `task__*` entry tagged for the pilot (e.g. `meta:{pilot:"opencode-harness"}`) so the audit trail *is* the dataset.

---

## 5. Rollback plan

Rollback is cheap by construction — **nothing about the gateway changes**, only which harness an operator opens.

- **Instant per-session:** opencode is additive. At any sign of trouble, the operator reopens Claude Code (still installed, still wired, untouched) and continues. No data migration — the ledger, mesh, memory, and secrets all live in the daemon, harness-independent.
- **Disable opencede's mcplexer wiring:** set `"enabled": false` on the mcplexer MCP entry in opencode config (or remove the block). Gateway unaffected.
- **No gateway mutations to undo:** the pilot does not run `mcplexer setup`, does not touch the launchd plist, does not alter slim-surface defaults, does not enable the `opencode_cli` *worker* opt-in. So there is no gateway state to revert.
- **Lease/zombie cleanup if Phase 2 reveals a gap:** if opencode disconnects don't release leases, the 1-minute passive sweep is the backstop (demotes leases >5 min). As immediate mitigation, end pilot sessions with an explicit `task__update({status:"open", assignee:null})` or `blocked` before closing, until/unless the disconnect hook is confirmed working.
- **Trigger conditions for full rollback:** any §3 FAILURE-column threshold breached on a *correctness* metric (tool-call success, code-mode integrity, ledger/lease discipline) → stop the pilot immediately, revert affected cohorts to Claude Code, file findings. Cost/latency misses → narrow scope (keep only the cohorts that won), don't necessarily abort the whole pilot.

---

## 6. Risks & unknowns (honest)

**Confirmed-unknown (must resolve at kickoff):**
- **opencode MCP config schema** — exact keys, stdio vs HTTP support, and whether it cleanly supervises a long-lived stdio MCP child. Plan assumes stdio parity with Claude Code; unverified until we read the installed version's docs.
- **Auto-unwrap behavior** — does opencode pass `mcpx__execute_code` results through such that the documented auto-unwrap (`result.id`, not `JSON.parse(...)`) holds? If opencode reformats tool results, code-mode snippets break subtly. **High-impact, must test Day 0.**
- **Slim-surface inheritance** — slim mode is gateway-side, but if opencode re-requests or caches `tools/list` differently, the 4-tool surface could leak wide. Test Day 0.
- **Mesh inbound delivery** — pending-message appending to tool results is a gateway convention; needs confirmation it survives opencode's result handling, else agents go deaf to mesh.
- **Lease auto-release on opencode disconnect** — keyed on session id (harness-agnostic in theory), but the disconnect hook firing for an opencode-initiated MCP session is the single biggest correctness risk. Zombie tasks poison coordination for *everyone* on the mesh, not just the pilot operator. **Verify explicitly in Phase 2; treat failure as a hard blocker.**

**Operational risks:**
- **`secret__prompt` outside the sandbox** — must surface a blocking interactive prompt in opencode; if it doesn't, credentialed sessions can't proceed and that whole cohort stays on Claude Code.
- **Harness-skill gap** — Claude-Code-only local skills + the dev-mode DB-lockdown hook have no opencode equivalent. Mitigated by scope (§1 keeps repo-internals + skill-dependent work on Claude Code), but limits how universal the migration can be.
- **Two-posture confusion** — `opencode_cli` worker provider (NetworkHost, opt-in-gated) vs opencode-as-outer-harness (this pilot). Conflating them could lead someone to wrongly enable/disable the worker opt-in or misjudge the egress surface. Document the distinction in the pilot brief.
- **Cost attribution noise** — routing through ~30 possible backends makes per-task cost only meaningful if model choice is logged per session. Enforce per-session model recording or the cost metric is unanalyzable.
- **Quality variance from cheaper models** — a "win" on cost that quietly raises rework rate is a net loss. The rework metric (§3) exists specifically to catch this; don't let a cost headline hide it.

**Unknowns we accept (won't resolve in-pilot):**
- Long-term opencode stability/maintenance vs Claude Code — out of scope for a 2-week functional pilot; revisit if the pilot says "expand."
- Whether the eventual mcplexer-proxy UDS egress allowlist (which the worker-provider opt-ins are waiting on) changes any of this — that's a worker-path concern, orthogonal to the outer-harness pilot.

---

### One-line recommendation
Run the pilot **stdio-wired, slim-surface, baseline-anchored**, gate hard on the Day-0 code-mode integrity checklist and the Phase-2 lease-release check, and only expand the cohorts that beat Claude Code on a *correctness tie + a real cost-or-token win*. Record the decision to mcplexer memory so it's durable and mesh-visible.
