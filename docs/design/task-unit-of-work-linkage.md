# Task ↔ unit-of-work linkage

Status legend: **[IMPLEMENTED]** ships in this change · **[PROPOSED]** design
only, not yet built.

## Problem

The mcplexer task ledger (`task__*`) is the canonical source of truth for what
an agent is doing. Its value collapses the moment the ledger drifts from
reality:

- A task stuck in a working status (`doing`) with a dead assignee poisons
  coordination warnings and the dashboard for every peer.
- An agent that did the work but never linked the commit leaves the audit trail
  blind.
- A task flipped `doing → done` with no evidence is indistinguishable from a
  lie.

The lease machinery (mcplexer 0.24+) is the backbone: `task__claim` takes an
atomic 5-minute lease, `task__heartbeat` extends it, and two release paths —
the per-session disconnect hook (`ReleaseSessionTasks`) and the 1-minute passive
sweep (`SweepExpiredLeases`) — demote abandoned working rows back to `open`.
This document covers five mechanisms that keep the ledger honest, the first of
which is the structural fix landing now.

---

## 1. Lease sweep demotes **status**, not just assignee **[IMPLEMENTED]**

### Root cause (verified)

A task row with `assignee_session_id` SET but `lease_expires_at` NULL was
**unreclaimable by both release paths**:

- `ClearExpiredTaskLeases` filtered `lease_expires_at IS NOT NULL AND
  lease_expires_at < now` — a NULL-lease row never matches.
- `ClearSessionTaskLeases` filtered `assignee_session_id = ? AND
  lease_expires_at IS NOT NULL` — same NULL-lease blind spot.

So a row that ended up with an assignee but no lease (a status flipped to
`doing` via raw `task__update` instead of `task__claim`, or a lease nulled out
of band) was a **permanent zombie**: dead assignee + working status forever,
invisible to disconnect release AND the passive sweep.

The service layer was already correct — `SweepExpiredLeases` and
`ReleaseSessionTasks` re-run `isWorkingStatus` on each returned id and demote
working rows to `open`. The bug was purely that the store-level Clear functions
never *returned* the zombie ids.

### Fix

The two store-level reclaim queries (`internal/store/sqlite/task.go`) each match
a no-lease working zombie via a shared arm, but they deliberately differ on how
they treat *live* leases because the two callers have different trust models.

**Passive sweep — `ClearExpiredTaskLeases` (`taskReclaimableExpr`).** The sweep
runs globally across all sessions, so it must NOT steal a still-live lease from a
session that's actively working. It reclaims a row if **either**:

1. **Past-lease** — `lease_expires_at IS NOT NULL AND lease_expires_at < now`.
   Status-agnostic: a `blocked` row past lease still gets its dead assignee +
   lease cleared; the service simply won't *demote* a non-working status.
2. **No-lease working zombie** — `assignee_session_id IS NOT NULL AND
   lease_expires_at IS NULL AND <working-status>`. A correctly-claimed working
   row *always* holds a lease, so an assignee-without-lease working row is by
   definition a zombie. This arm is the structural fix.

**Disconnect release — `ClearSessionTaskLeases` (`taskSessionReclaimExpr`).**
This fires for a *named, gone* session, so a still-future lease is no reason to
keep its row — the holder is dead. Scoped to `assignee_session_id = ?`, it
reclaims **either**:

1. **Any leased row** — `lease_expires_at IS NOT NULL` (any expiry).
2. **No-lease working zombie** — same arm as the sweep.

Both predicates share `taskWorkingStatusPredicate`, kept in lock-step with
`Service.isWorkingStatus`: the per-workspace `task_status_vocabulary`
`kind='working'` classification wins; absent **any** vocab row for that status
the literal `'doing'` is the fallback. Keeping the store predicate identical to
the service helper is load-bearing — the store returns the ids it un-leases and
the service re-decides demotion per id; if they disagreed a row could be
un-leased in the store yet left in a working status by the service.

`ClearSessionTaskLeases` additionally guards an empty session id (returns nil —
never a workspace-wide assignee wipe) and stays session-scoped.

Behavioural matrix after the fix:

| status      | lease            | assignee | sweep / release action                 |
|-------------|------------------|----------|----------------------------------------|
| `doing`     | past             | set      | clear assignee+lease, demote → `open`  |
| `doing`     | **NULL** (zombie)| set      | clear assignee+lease, demote → `open`  |
| `doing`     | future           | set      | untouched                              |
| `blocked`   | past             | set      | clear assignee+lease, **status kept**  |
| `blocked`   | NULL             | set      | untouched (nothing to reclaim)         |
| `open`      | NULL             | set      | untouched (not a working status)       |
| custom `kind=working` | NULL   | set      | clear assignee+lease, demote → `open`  |

Tests in `internal/store/sqlite/task_test.go`:

- `TestClearExpiredTaskLeasesDemotesNonWorkingAndReclaimsZombie` — (a) working
  past-lease reclaimed, (b) blocked past-lease keeps status, (d) no-lease
  working zombie reclaimed.
- `TestClearExpiredTaskLeasesRespectsCustomVocab` — custom `kind=working`
  zombie reclaimed, custom non-working untouched.
- `TestClearSessionTaskLeasesScopesToSession` — session scoping + the no-lease
  working zombie reclaim.
- `TestClearSessionTaskLeasesGuardsEmptySession` — empty session is a no-op.
- `TestClearExpiredTaskLeasesTargetsRightRows` (pre-existing) — past-lease vs
  live vs non-working no-lease.

### Migration / rollout

Pure query-logic change — **no schema migration**, no new columns. Existing
zombie rows are healed automatically on the next sweep tick (≤1 minute) or the
next disconnect of their dead session. Backward-compatible: callers see the same
function signatures and the same "ids that were reclaimed" contract; they simply
now receive the previously-invisible zombie ids too.

---

## 2. Invisible PreToolUse heartbeat hook **[PROPOSED]**

### Goal

Keep a claimed task's lease fresh without the agent having to remember
`task__heartbeat`, so a working agent never loses its row to the sweep mid-task,
and a *stopped* agent's row is released promptly.

### Design

A harness `PreToolUse` hook (installed alongside the existing
`block-mcplexer-db.sh` hook) that, on every tool call, makes a fire-and-forget
call to extend the lease of the task this session currently holds.

- The hook reads the session id from the harness env and asks the gateway
  "which task does this session hold in a working status?" then heartbeats it.
  The gateway already exposes `task__heartbeat`; a thin
  `task__heartbeat_session` (heartbeat *all* working rows held by the calling
  session) avoids the agent needing to know the task id.
- Idempotent + silent: heartbeating a task you don't own is already a no-op
  (`HeartbeatTask` matches on `assignee_session_id`), so the hook can fire
  blindly.
- Throttled: skip the call if the lease was bumped within the last ~60s (the
  middleware auto-heartbeat covers the common case; the hook only matters during
  long non-MCP stretches — extended Read-tool reads, planning).

### Why a hook and not middleware alone

The gateway already auto-heartbeats on every tool call routed *through it*. The
gap is host-side tool calls (Read/Edit/Bash) that never touch the gateway — a
30-minute refactor with no `mcpx__execute_code` call lets the lease lapse and the
sweep steals the row. The PreToolUse hook closes exactly that gap because it
fires on **every** harness tool call.

### Rollout

Opt-in via the installer (`internal/install/hooks.go`), same pattern as the DB
guard hook. Dev-mode escape hatch identical to the existing hook so it doesn't
interfere with gateway development. Failure is non-fatal: a hook error must never
block the underlying tool call.

---

## 3. Smart-commits — parse task IDs from commit messages **[PROPOSED]**

### Goal

Close the loop between "the diff exists" and "the task knows about it" without a
manual `task__append_note`. A commit message that references a task id should
auto-link the commit to the task and optionally transition it.

### Syntax

Borrow the well-worn Jira/GitLab smart-commit grammar, scoped to ULID task ids:

```
fix(tasks): reclaim no-lease zombies

Refs 01KST3C8J419VHPXY6S5GZYRDF
Closes 01KST4T3ATXACVMPRWCTP03241 #verified: go test ./internal/store/...
```

- `Refs <id>` / `[[<id>]]` → append a `commit_linked` note (sha + subject) to
  the task's history. Non-transitioning.
- `Closes <id>` / `Fixes <id>` → as above, plus transition the task to `review`
  (not straight to `done` — see mechanism 4). The trailing `#verified: <cmd>`
  becomes the evidence payload.
- ULID match is anchored (`01[0-9A-HJKMNP-TV-Z]{24}`) so prose never
  accidentally trips it; `[[ref]]` double-bracket form matches the existing
  `[[ref]]` convention already used in task bodies.

### Plumbing

A `post-commit` (or `post-receive` server-side) git hook shells `mcplexer task
link --commit <sha>`, which parses the message, resolves ids in the current
workspace, and calls the existing `task__append_note` + `task__update` surface.
No new storage — reuses the status_history audit trail and emits the existing
`task_event:note_appended` / `status_changed` mesh events so peers see the link
land.

### Rollout

Hook installed per-repo by `mcplexer setup` (idempotent). Safe by default:
unmatched ids are skipped silently; a `Closes` for a task in another workspace
is ignored rather than cross-linked. Parsing is best-effort — a malformed
trailer never blocks the commit.

---

## 4. Evidence-required-on-close **[PROPOSED]**

### Goal

Make `doing → done` without verification *impossible*, enforcing the
`open → doing → review → done` lifecycle the rules describe. The failure mode
this kills: an agent flipping a working task straight to `done` having observed
nothing.

### Design

Gate the terminal transition at the service layer (`Service.Update`):

- A transition **into** a terminal status (per `task_status_vocabulary`
  `is_terminal`) requires an evidence payload — a non-empty `body`/note that
  names what was verified, OR a structured `meta.evidence` object (e.g.
  `{tests: "...", commit: "...", behaviour: "..."}`).
- Transitions **from a working status directly to a terminal status** (skipping
  `review`) are rejected with a typed `store.FieldError`
  (`code=review_skipped`, hint: "transition to review first, or pass evidence").
  A single-hop `doing → done` is allowed only when evidence is present AND the
  workspace policy permits self-review (trivial-fix path).
- `review → done` requires either self-review evidence (trivial) or a recorded
  peer/human signoff (the `assigned_by`/note trail).

### Vocabulary additions

`task_status_vocabulary` already carries `kind`. Add a per-workspace policy flag
(`require_evidence_on_terminal`, default on) so a workspace can relax the gate
for low-stakes ledgers. No new table — a row in the existing workspace settings.

### Rollout

Phased: ship in **warn** mode first (the transition succeeds but emits a
`task_event:closed` carrying `evidence_missing: true` so the dashboard flags
it), gather a week of signal, then flip to **enforce**. Enforcement is
workspace-scoped so a noisy migration doesn't break every ledger at once.

---

## 5. Exit hook on clean shutdown **[PROPOSED]**

### Goal

Release a session's working tasks the *instant* the agent exits cleanly, rather
than waiting up to 5 minutes for the lease to lapse — tightening the disconnect
path's freshness for the graceful case.

### Design

The gateway already runs `ReleaseSessionTasks(sessionID)` on MCP session
teardown (the disconnect hook), which is the authoritative path. This mechanism
adds a **client-side** belt:

- A harness `SessionEnd` / `Stop` hook that calls `mcplexer task release
  --session $SESSION` on clean exit. This races ahead of the server-side
  disconnect detection (which can lag on a half-open socket) and guarantees the
  row demotes to `open` with a `released on clean shutdown` history note before
  the agent's process is gone.
- Idempotent with the server-side path: whichever fires first wins; the second
  is a no-op because the row no longer holds the lease.

### Interaction with the structural fix (mechanism 1)

The exit hook calls the **same** `ReleaseSessionTasks` → `ClearSessionTaskLeases`
path, so it automatically inherits the no-lease-zombie reclaim. An agent that
flipped a task to `doing` without claiming (no lease) and then exits cleanly now
has that row released by the exit hook too — previously it would have been a
permanent zombie regardless of how cleanly the agent left.

### Rollout

Installed by the setup flow alongside the heartbeat hook (mechanism 2).
Non-fatal on error — a failed release hook must never block shutdown; the
passive sweep remains the backstop.

---

## Mesh-visible task-event vocabulary

Every observable mutation publishes a `task_event:<evt>` mesh message via the
`Emitter` (`internal/tasks/events.go`). Peers and workers subscribe to keep their
view of the ledger live. The lease machinery rides on these existing events — no
new event kinds are required for mechanism 1.

| event             | fires when                                           | notify | relevance to lease linkage |
|-------------------|------------------------------------------------------|--------|----------------------------|
| `created`         | task created                                         | no     | —                          |
| `updated`         | generic non-status patch                             | no     | lease chip refresh         |
| `assigned`        | assignee changed                                     | conditional | claim / hand-off       |
| `claimed`         | `task__claim` took the lease                         | conditional | lease acquired         |
| `status_changed`  | status transitioned (incl. **sweep/disconnect demotion → `open`**) | yes | **mechanism 1 emits this on every demotion** |
| `closed`          | entered a terminal status                            | conditional | mechanism 4 evidence gate |
| `note_appended`   | history note added (incl. `commit_linked`, `lease_expired`) | no | mechanisms 3 + 1 audit |
| `deleted`         | soft-deleted                                         | no     | —                          |

History-only audit markers (written to `status_history_json`, surfaced via
`note_appended`):

- `lease_expired` — a reclaim happened; note distinguishes
  `"lease expired, demoted from working status"` (sweep) from
  `"agent disconnected, demoted from working status"` / `"released on disconnect"`
  (release path).
- `commit_linked` *(proposed, mechanism 3)* — a commit sha was associated.
- `evidence_missing` *(proposed, mechanism 4)* — terminal transition lacked
  evidence (warn mode).

---

## Summary

| # | mechanism                          | status        | needs migration | needs new event |
|---|------------------------------------|---------------|-----------------|-----------------|
| 1 | sweep demotes status not assignee  | **IMPLEMENTED** | no            | no (reuses `status_changed`) |
| 2 | invisible PreToolUse heartbeat     | **PROPOSED**  | no              | no              |
| 3 | smart-commits parse task ids       | **PROPOSED**  | no              | no (reuses `note_appended`/`status_changed`) |
| 4 | evidence-required-on-close         | **PROPOSED**  | settings flag   | extends `closed` payload |
| 5 | exit hook on clean shutdown        | **PROPOSED**  | no              | no (reuses `status_changed`) |

Mechanism 1 is the load-bearing structural fix: it makes the ledger
*self-healing* for the zombie case that previously required manual intervention.
Mechanisms 2–5 build the linkage that keeps the *honest* agent's ledger in sync
with the real unit of work — commits, evidence, lifecycle — without relying on
the agent to remember every bookkeeping step.
