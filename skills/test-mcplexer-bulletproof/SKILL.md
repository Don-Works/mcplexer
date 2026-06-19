---
name: test-mcplexer-bulletproof
description: Use when the user says "bulletproof test", "overnight test", "full coverage test", "test mcplexer bulletproof", "verify the gateway end-to-end at all trust tiers", or invokes `/test-mcplexer-bulletproof`. Spins up a 6-node dockerized harness, runs the full trust-tier matrix (same-user / same-org / cross-org) plus memory deep-dive plus feature-manifest coverage gate, drives all 6 dashboards in parallel via /playwright-browser, aggregates flakes across N iterations, samples resource posture, and publishes a structured PASS/FAIL/FLAKE report + memory-quality grade. Designed for nightly cron via the `nightly-bulletproof` worker template. Companion to /test-mcplexer (the quick 3-node smoke test) — bulletproof is the slow, exhaustive sibling.
---

# test-mcplexer-bulletproof

The exhaustive overnight verification of mcplexer. Where `test-mcplexer` proves the
system works ONCE on three nodes with a single trust posture, this skill proves it works
REPEATEDLY, across all THREE trust tiers, with memory that's CLEVER (not just
functional), and breaks the build the moment a feature ships without a scenario.

## How this differs from `test-mcplexer`

| dimension              | test-mcplexer        | test-mcplexer-bulletproof                                          |
| ---------------------- | -------------------- | ------------------------------------------------------------------ |
| nodes                  | 3 (a, b, c)          | 6 (u1m1, u1m2, u2m1, u3m1, u3m2, scratch)                          |
| trust tiers exercised  | 1 (same workspace)   | 3 (same-user, same-org, cross-org)                                 |
| scenarios              | ~50 smoke            | smoke + B1/B2/B3 + C1/C2 + D1-D7 + E1/E2 + manifest gate           |
| memory testing         | basic CRUD           | hybrid recall@5, supersede chains, 10k stress, scope isolation     |
| iterations             | 1 run                | N runs (default 10) with flake aggregation                         |
| coverage gate          | none                 | `feature_manifest.yaml` — any unscenarioed feature FAILs the run   |
| resource leak guard    | none                 | goroutine / FD / disk delta sampling before vs after               |
| audience               | interactive dev      | nightly cron (`nightly-bulletproof` worker template)               |
| runtime budget         | ~3 min               | ~30-90 min (10 iterations)                                         |

If the user just wants "is mcplexer alive right now" — use `test-mcplexer`. If they want
"prove every cross-machine code path is bulletproof and stays that way" — this one.

## Trust tiers under test

1. **Tier 1 — same user, multiple machines.** Auto-granted default scopes on pair.
   Sharing is silent, immediate, mesh-visible. Skills, memory, tasks, mesh all flow with
   no approval gate.
2. **Tier 2 — same company, different users.** No silent grants. Every cross-user share
   requires an explicit `auth_scope`. Recipient sees an approval entry. Audit captures
   `grant_origin`.
3. **Tier 3 — different companies, multiple workers.** Same as Tier 2 PLUS an org-pair
   boundary on every grant. Unauthorized cross-org attempts get an explicit denial code,
   not a silent drop. Workers running under one org never read another org's memory
   unless explicitly invited.

## Source of truth

The task template that drives this is epic **`01KSK91Q4W8TNED9MAF0CTRVKC`** —
clone per run via:

```js
task.create({ compose_into: "<new-run-epic>", title: "bulletproof run YYYY-MM-DD" })
```

…tick children off there. NEVER close the template epic itself; it's marked
`meta.template: true` and stays canonical.

## Child-task prereqs (must land before this skill runs end-to-end)

| id                                       | gate                                                 | what it ships                                                 |
| ---------------------------------------- | ---------------------------------------------------- | ------------------------------------------------------------- |
| `01KSK954381RFHR10JJ72VH1M9` (A1)        | docker compose has 6 nodes + `BULLETPROOF=1` flag    | `tier_topology.sh`, port 13338, tier env vars per node        |
| `01KSK9594R0BDHHGKHEBDYA8FC` (A2)        | `lib.sh` exposes `pair_same_user/_org/_cross_org`    | tier-aware helpers (default-grant / explicit-grant assertions) |
| `01KSK959597RJNEEXYG31ASHSG` (A3)        | `feature_manifest.yaml` + validator                  | coverage gate that fails if a surface lacks a scenario tag    |
| `01KSK9597J51A4007EC0BF5PFR` (B1)        | `scenario_tier1_same_user.sh`                        | silent auto-grant flow across u1m1+u1m2                       |
| `01KSK9598A33SS901SNSHV68B2` (B2)        | `scenario_tier2_same_org.sh`                         | explicit-grant flow alice→bob                                 |
| `01KSK9598K378WQ5X10NG74BP3` (B3)        | `scenario_tier3_cross_org.sh`                        | org-pair grants AcmeCo↔BetaCo + denial codes                  |
| `01KSK9598ZR58X9C83KJPFB8T9` (C1)        | `scenario_consent_queue.sh`                          | `/approvals` REST+UI parity                                   |
| `01KSK95998X8RTW37C557NE41F` (C2)        | `scenario_consent_audit.sh`                          | `accepted_by` field on every cross-boundary audit row         |
| `01KSK9599EZR2C2QQF1FFF836B` (D1)        | `scenario_concierge_self_improving.sh`               | signals → classifier → A/B → lesson persistence               |
| `01KSK9599RS9EPRMZB97BMG58W` (D2)        | `scenario_memory_consolidator.sh`                    | dedup + link rewrite + cross-peer pickup                      |
| `01KSK959A5H99NJC8NH9V15JBJ` (D3)        | `scenario_task_attachments.sh`                       | upload/list/fetch/delete + cross-peer gating                  |
| `01KSK959ACV89663BS7PYJ11AT` (D4)        | `scenario_skill_bundles.sh`                          | tar.gz bundle publish + cross-peer fetch + checksum           |
| `01KSK959ANCY6C831MT9Y988X6` (D5)        | `scenario_worker_adapters.sh` (env-gated)            | claude_cli + opencode_cli + grok_cli sandbox + opt-in env paths |
| `01KSK959AWY1EDZTX6ZJ04CA8J` (D6)        | `scenario_cmdguard_db_lockdown.sh`                   | downstream-registration rejection for protected paths         |
| `01KSK959CK831PM6SDHM4960NH` (D7)        | `scenario_memory_deep_dive.sh` + `memory-grade.md`   | hybrid search quality / supersede / scope iso / 10k stress    |
| `01KSK959D4Z3XK6QXSQ1Q0DT8X` (E1)        | `test/integration/run_overnight.sh N`                | N-run loop + flake aggregation + non-zero exit on flake       |
| `01KSK959DGF05FW713C6TN89PB` (E2)        | resource posture sampler                             | goroutine/FD/disk before/after delta guard                    |
| `01KSK959DZ66EXNFMMRC03JAA6` (F2)        | `nightly-bulletproof` worker template                | cron `0 2 * * *` UTC, mesh-broadcasts result                  |

When a row is not yet landed, the corresponding section below SKIPs in the run rather
than FAILs — the runner's exit code reflects the union of present scenarios. Re-check
prereq status before declaring a green run "bulletproof".

## Prerequisites (host)

```bash
# CWD must be the mcplexer repo (or a worktree of it)
test -f Makefile && grep -q 'test-integration:' Makefile || {
    echo "Not in mcplexer repo — abort"; exit 1; }

# Docker available
docker --version && docker compose version

# Required ports free (13333-13338 — six nodes)
for p in 13333 13334 13335 13336 13337 13338; do
  if lsof -nP -iTCP:$p -sTCP:LISTEN 2>/dev/null | grep -q LISTEN; then
    echo "port $p bound — abort, do NOT stop the user's daemon"; exit 1
  fi
done

# jq for scenarios.sh
command -v jq >/dev/null || brew install jq
```

If any port is bound, ABORT and ask the user — never silently stop their daemon.

## Required env opt-ins (D5)

Two worker provider adapters are sandbox-gated and only attempt full execution when
opted in. Without these, D5 verifies the REJECTION path; with them, D5 runs the full
provider path:

```bash
export MCPLEXER_ALLOW_CLAUDE_CLI=1    # gates the `claude_cli` model provider
export MCPLEXER_ALLOW_OPENCODE_CLI=1   # gates the `opencode_cli` model provider
export MCPLEXER_ALLOW_GROK_CLI=1       # gates the `grok_cli` model provider
export MCPLEXER_ALLOW_MIMO_CLI=1       # gates the `mimo_cli` model provider
```

Both are passed into the container via the compose file. The sandbox profile denies
writes to `~/.claude/` and `~/.mcplexer/` even when the env is on — D5 verifies the
denial holds even in opt-in mode.

## Run

### 1. Spin up the 6-node tiered harness

```bash
BULLETPROOF=1 make test-integration
```

`BULLETPROOF=1` flips the docker-compose to the 6-node tier topology (A1) and enables
the B/C/D/E scenarios on top of the existing ~50 smoke scenarios. Per-service logs land
in `test/integration/_logs/` on failure. Set `TEST_KEEP=1` to inspect containers
afterwards; tear down with `make test-integration-down`.

### 2. While running — drive 6 dashboards in parallel

Open ALL six dashboards in the SAME `/playwright-browser` tool-call batch (parallel,
not serial — that's the whole point):

- Tab 1: `http://localhost:13333` — **u1m1** (AcmeCo alice, machine 1) — Tier 1 reference
- Tab 2: `http://localhost:13334` — **u1m2** (AcmeCo alice, machine 2) — Tier 1 partner
- Tab 3: `http://localhost:13335` — **u2m1** (AcmeCo bob)                — Tier 2 peer
- Tab 4: `http://localhost:13336` — **u3m1** (BetaCo carol)               — Tier 3 peer
- Tab 5: `http://localhost:13337` — **u3m2** (BetaCo carol, machine 2)    — Tier 1 within BetaCo
- Tab 6: `http://localhost:13338` — scratch / extra worker

For each tab visit `/mesh`, `/skills`, `/memory`, `/tasks`, `/approvals`, `/workers`;
screenshot to `_artifacts/dashboard-<node>-<route>.png`. Then assert UI/API parity for
every route — UI showing 0 when API returns 1 is a FAIL row in the report.

### 3. Tier-matrix assertions (B1/B2/B3 + C1/C2)

Every cross-boundary transfer produces an audit row whose shape varies by tier:

- **Tier 1**: `accepted_by.kind = "auto_pair"`, NO approval queue entry
- **Tier 2**: `accepted_by.kind = "human"` with `user_id` + `agent_id` + `timestamp`;
  approval queue entry visible in `/approvals`
- **Tier 3**: same as Tier 2 PLUS `accepted_by.org_pair` field set
- **Unauthorized**: explicit `denial = "no_scope"` or `"cross_org_boundary"` code in the
  response; audit row created on the rejecting node; ZERO side-channel (no
  existence-leak in the error path)

C2 walks every audit row with `kind in (skill_share, memory_share, task_offer,
mesh_direct)` and FAILs the run if any cross-boundary row is missing `accepted_by`.

### 4. Memory deep-dive (D7) — produces `_artifacts/memory-grade.md`

Five sub-buckets:

- **D7.1 — hybrid search quality**: seed ~200 memories with tricky overlaps (homonyms,
  synonyms, near-duplicates, multi-language); grade recall@5 across an oracle query
  fixture. Threshold: recall ≥ 0.85.
- **D7.2 — supersede chain integrity**: A supersedes B supersedes C; recall must surface
  A, link traversal must walk A→B→C, audit must record the chain.
- **D7.3 — scope isolation per tier**: memory visible on u1m1 but Tier 2 peer u2m1
  must not see it unless explicitly granted; cross-org u3m1 must not see it ever.
- **D7.4 — large-N stress**: 10k entries on one node, query p95 latency target
  (TBD baseline). Downgrade `MEMORY_STRESS_N=1000` on a constrained host.
- **D7.5 — redaction holds**: memories containing secret markers (`secret://...`,
  api-key-like patterns) are redacted in audit output.

A memory grade below 0.85 is a FAIL distinct from scenario failures and gets its own
line in the report.

### 5. Overnight loop (E1)

```bash
test/integration/run_overnight.sh 10
```

Runs the matrix 10× and aggregates flakes. Any scenario that flapped (PASS in one run,
FAIL or SKIP in another) is a FLAKE — flakes are bugs, file a child task on the epic
(see step 8). Non-zero exit if any flake.

### 6. Resource posture (E2)

Before run 1 and after run N: sample `pprof goroutine`, `/proc/<pid>/fd | wc -l`, `du
-sh data/` per daemon. Deltas outside ±20% of baseline are leaks → file a bug task.

### 7. Structured report

Always emit, even on success:

```
mcplexer bulletproof report
===========================
docker harness:       PASS  (PASS=N FAIL=0 SKIP=M; runtime Xs; 10 runs)
tier matrix:          PASS  (T1 silent / T2 explicit / T3 org-pair — all green)
consent surface:      PASS  (approval queue + accepted_by audit complete)
memory grade:         0.91  (recall@5; >= 0.85 = pass)
flake count:          0
resource deltas:      goroutines +3% / fds +1% / disk +8%   (within budget)
dashboard parity:     PASS  (6/6 nodes — all routes match API)
feature manifest:     PASS  (84 features, 84 scenarios)
screenshots:          _artifacts/dashboard-*.png (36 files)
memory grade report:  _artifacts/memory-grade.md

per-scenario:
  ✓ STEP 1: ...
  ✗ STEP 17: cross-org skill share — missing accepted_by.org_pair
  ...
```

On failure, replace the relevant ✓ with ✗ and inline the FAIL detail line(s).

### 8. Bug-task filing on FAIL or FLAKE

For every FAIL or FLAKE, file a child task on epic `01KSK91Q4W8TNED9MAF0CTRVKC`:

```js
task.create({
  compose_into: "01KSK91Q4W8TNED9MAF0CTRVKC",
  title: "BUG: <scenario> — <one-line symptom>",
  description: "<failure tail from scenarios.sh + reproducer>",
  tags: ["bug", "found-by:bulletproof-e2e"],
  meta: { source_run: "<run-id>", tier: "<1|2|3>", scenario: "<file>" }
})
```

Discovery policy: **fixes do NOT land in the scenario**. Keep scenarios deterministic;
fixes go in product code on their own branch with a regression test in the relevant
scenario file. See the "Discovery-of-issues policy" section of the epic.

### 9. Mesh broadcast of result (F2 worker does this automatically)

When run interactively, broadcast a summary so paired peers see the result:

```js
mesh.send({
  recipient: { kind: "audience", value: "*" },
  kind: "finding",
  priority: result === "FAIL" ? "high" : "low",
  content: report_summary,
  tags: ["bulletproof", "nightly"]
})
```

The `nightly-bulletproof` worker template (F2) does this on a cron schedule
(`0 2 * * *` UTC) so a human doesn't need to be in the loop.

## Interpreting results

- **PASS**: every scenario green, no flakes, memory grade ≥ 0.85, resource deltas within
  ±20%, all features in the manifest cited by at least one scenario tag. Ship it.
- **FAIL**: one or more scenarios red. File bug tasks per step 8, do NOT mutate the
  scenarios, fix in product code with its own branch.
- **FLAKE**: scenario passed some runs, failed others. Treat as a FAIL — non-determinism
  in a verification suite is itself a bug.
- **SKIP**: a prereq isn't present (libp2p closed bridge, opt-in env missing, child
  task not landed). A run with many SKIPs is NOT "bulletproof"; re-check the prereq
  table.
- **MANIFEST FAIL**: a shipped feature has no scenario tag. This is the gate that makes
  the suite *bulletproof* rather than *comprehensive-today*. Add a scenario tag (or a
  new scenario) before merging the feature.

## Known limitations

- **libp2p on closed docker bridge**: pair-handshake may SKIP when mDNS misses a pair.
  Tier scenarios that depend on a successful pair will SKIP, not FAIL, if the prereq
  pair didn't land. To exercise the handshake reliably, run across two real LAN hosts
  (override `NODE_U*M*=<lan-ip>` and bypass docker).
- **`claude_cli` / `opencode_cli` / `grok_cli` adapters**: require env opt-ins (see Prerequisites).
  Without the env, D5 verifies the rejection path; with the env, full execution against
  stub binaries. Sandbox writes to `~/.claude/` and `~/.mcplexer/` are denied even with
  opt-in — D5 verifies this holds.
- **Memory 10k stress (D7.4)**: requires ~2GB RAM headroom per daemon. On a constrained
  host, downgrade with `MEMORY_STRESS_N=1000` to avoid OOM.
- **Coverage gate strictness**: a new feature without a scenario tag will FAIL the run
  on the first cycle after it merges. That's by design — fix forward by adding the
  scenario tag, not by deleting the manifest entry.

## Why this skill exists

`make test-integration` proves the system works once. This skill proves it works
REPEATEDLY, ACROSS ALL THREE TRUST TIERS, with MEMORY THAT'S ACTUALLY CLEVER, and
breaks the build the moment a feature ships without a scenario.

The user's bar:

> overnight testing to be sure that mcplexer is bulletproof. That it works for sharing
> skills, memories, working on tasks collaboratively internally as 1 user with multiple
> machines, multiple users with multiple machines, and multiple companies with multiple
> workers/machines - and that users can be confident and safe that data transfer is
> acknowledged and explicitly allowed by them.

This is the skill that enforces that bar.
