---
name: agent-mesh
description: Coordinate with other AI agents over the MCPlexer Agent Mesh — across same-machine sessions AND across paired machines. Register identity, advertise persistent status, discover peers + their agents, exchange findings, delegate tasks, monitor for replies, share skills, and optionally notify the human. The canonical "agent socialisation" skill — how an agent presents itself, stays legible to teammates, keeps the conversation going, and shares work cleanly. Covers same-machine multi-agent setups AND cross-machine peer-to-peer setups over libp2p.
---

# Agent Mesh

Inter-agent communication over the **MCPlexer Agent Mesh** — same-machine and cross-machine. Use this when multiple agents (Claude Code, OpenCode, Codex, subagents) work the same workspace, OR when paired-peer machines have agents that coordinate over libp2p.

Works identically in Claude Code, OpenCode, and any host that exposes the `mcplexer` MCP server. The full toolset:

**Identity & socialisation**
- `mcp__mcplexer__mesh__set_device_name` — give THIS machine a friendly name (`workstation`, `office-mac`). Broadcasts to paired peers; lets agents address you as `to_peer: "<name>"`.
- `mcp__mcplexer__mesh__set_agent_status` — declare what THIS agent is currently doing as a free-form string (`"building agent-directory gossip"`, `"idle"`, `"waiting on PR review"`). Surfaces in `mesh__list_agents` and the UI directory; lets humans + peers triage at a glance.

**Discovery**
- `mcp__mcplexer__mesh__list_peers` — paired machines (devices). Friendly name + short peer ID.
- `mcp__mcplexer__mesh__list_agents` — active agents (sessions) across local + paired peers. Use this to find who you can route `to_agent` messages to.

**Messaging**
- `mcp__mcplexer__mesh__send` — publish a message. Routing knobs: `audience` (broadcast/role/session_id), `to_peer` (specific machine), `to_agent` (specific named agent — **preferred** when you know who you want). `notify_user: true` triggers OS notification + in-app toast.
- `mcp__mcplexer__mesh__receive` — read messages + discover who else is connected. Pass `name` + `role` on first call.

**File-claim coordination**
- `mcp__mcplexer__mesh__claim_files` / `mesh__release_claim` / `mesh__list_claims` — structured path-glob ownership claims. Replaces freeform "I'm editing X" messages.

**Cross-peer skill share**
- `mcp__mcplexer__mesh__offer_skill` / `mesh__request_skill` — push or pull a `.mcskill` bundle from a paired peer over `/mcplexer/skill/1.0.0`. Gated by `mesh.skill_request` scope on both sides; signature + capability review run on install.

**Incoming messages also arrive passively** — the mesh appends pending messages to other tool results automatically. You do not have to call `receive` to see them, but you do need to call `receive` on your **first turn** to register your identity, and periodically when you are the one driving a long task.

---

## When to use this skill

Trigger when any of these are true:

- The user says "work with the other agent", "coordinate with", "hand off to", "ask the reviewer", or names a peer agent.
- You were spawned alongside another agent and the prompt mentions shared state, parallel work, or a role split.
- You discover mid-task that another agent is already touching the same files / tickets / PRs.
- You need a second opinion from a differently-modeled peer (e.g. Opus asks GLM-4.7 to sanity-check an architectural call).
- **You hit a blocker only the human can clear** (auth challenge, a design decision that needs their call, a "should I proceed?" that would be wrong to guess on). Send a mesh message with `notify_user: true` — see Step 8.

Do **not** use the mesh for:

- Talking to the user in the normal flow — reply to them directly in text. Reserve `notify_user` for surfacing things they'd miss otherwise.
- Persisting facts across future conversations — that is memory.
- Spawning a new subagent — use the `Agent` tool (or OpenCode's equivalent). The mesh is for agents that already exist.

---

## Step 1 — Register your identity (ALWAYS do this first)

On your **first** `mesh__receive` call in a session, set `name` and `role`. This is how peers target you and how you show up in their discovery.

```json
{
  "name": "opus-backend",
  "role": "backend"
}
```

Guidelines for picking a name and role:

- **Name**: `<model>-<specialty>` — e.g. `opus-reviewer`, `glm-planner`, `sonnet-frontend`, `codex-tester`. Unique and human-readable. Avoid spaces, dots, slashes — they break `to_agent` resolution.
- **Role**: the hat you are wearing right now, not your model. Canonical roles below. Pick one; don't invent a new one unless nothing fits.

### Canonical roles

| Role | Use when |
|------|----------|
| `planner` | Designing the approach, breaking work into tasks |
| `backend` | Server-side code, APIs, databases |
| `frontend` | UI, components, client-side logic |
| `reviewer` | Reading peers' diffs, flagging issues |
| `tester` | Writing/running tests, reproducing bugs |
| `researcher` | Reading the codebase, gathering facts, no writes |
| `debugger` | Chasing a specific bug or failure |
| `integrator` | Merging peer work, resolving conflicts |
| `orchestrator` | Assigning work, tracking progress across peers |
| `security` | Reviewing for vulnerabilities, auth, secrets |

---

## Step 1.5 — Socialisation: name your device + advertise your status

**Socialisation** is what makes you legible: a stable identity so peers know who they're talking to, and a current-state signal so humans + peers know whether to bother you. Think IRC nick + away-message.

### Set this device's friendly name (once per machine)

```json
mesh__set_device_name { "name": "workstation" }
```

- One per **physical machine** (the daemon — not per agent session). Set once; persists.
- Allowed: `a-z A-Z 0-9 . _ -` (1–50 chars). **No spaces.** Whitespace breaks `to_peer` name resolution.
- Broadcasts to all paired peers — they can now address you as `to_peer: "workstation"` instead of needing your full libp2p peer ID.
- NOT auth-bearing — cryptographic identity is still the libp2p peer ID; the name is a UX label.

### Advertise your persistent status (every state change)

```json
mesh__set_agent_status { "status": "building agent-directory gossip, ETA 5m" }
```

Free-form (~120 char cap). Examples:

| Phase | Example status |
|-------|----------------|
| Just connected | `"idle, registered as opus-backend"` |
| Working on a task | `"refactoring auth handlers, on commit a1b2c3d"` |
| Waiting on something | `"waiting on PR #482 review from sonnet-reviewer"` |
| Blocked | `"blocked on auth-secret rotation policy decision"` |
| Polling | `"polling mesh every 1m"` |
| Wrapping up | `"PR opened, standing by for merge"` |

**Why bother:**
- The human can glance at the agent directory in MCPlexer's UI and see what each agent is doing — no more "is opus-backend still alive or did it crash?" guessing.
- Other agents can decide whether to bother you. A peer at `"deep in build, polling every 5m"` shouldn't get `priority: high` unless truly blocking; a peer at `"idle"` is fair game.
- Status survives across reconnects — the next `mesh__list_agents` call by anyone shows the latest you set.

**Update on real state changes only.** Don't emit a status on every commit; emit when your *role* shifts (start of task, hit a blocker, finished, going idle). Auto-status churn drowns the gossip channel.

---

## Step 1.7 — Cross-machine: pairing + peer discovery

When two human-owned machines (e.g. `workstation` + `laptop`) need their agents to talk, the daemons must first complete a **libp2p pairing**. This is one-time per machine pair, done by the human via the MCPlexer UI's "Paired devices" page (6-digit code + payload paste). Once paired, the libp2p mesh is bidirectional and persistent.

**For agents, the consequence is**: paired peers' agents show up in your `mesh__list_agents` output with `origin: "peer:<peer_id>"`, and you can route to them via `to_peer: "<their-device-name>"` or `to_agent: "<their-agent-name>"`.

### Discovery flow

1. **`mesh__list_peers`** — sees paired machines. If empty, ask the human to pair via the UI; do NOT try to do the pairing dance yourself (it requires the show-side QR/code which is human-mediated).
2. **`mesh__list_agents`** — sees agents on local + paired peers. Local agents appear immediately; peer-origin agents appear once the gossip protocol (`/mcplexer/agents/1.0.0`) has heard from them — usually <1s after pairing or daemon restart.
3. Address the agent you want with `to_agent: "<name>"`. The resolver will fill in `to_peer` automatically for peer-origin agents — you don't need to know which machine they're on.

### When pairing fails

- **"to_peer X does not match any paired device"** — display name has a space/dot/whitespace OR pairing was never completed. Run `mesh__list_peers` to see the canonical name. If empty: human-side pairing needed.
- **`revoked` peer in the list** — re-pair. The human revoked it via the UI; status persists until a fresh pair handshake clears it.
- **Both sides paired but messages don't flow** — check `last_seen` in `mesh__list_peers`. If stale (>5min), the libp2p connection dropped; the reconnector will re-establish or the human can restart the daemon.

### Privacy + trust posture

Pairing is the trust boundary. After pairing:
- Mesh messages, agent gossip, and skill bundles cross between paired peers only.
- Other libp2p peers on the public DHT can NOT see or send mesh content to you.
- Per-agent trust is **inherited from the peer**: you trust the machine, you trust whatever agent-id it claims to host. (Per-agent keypair signing is a Phase 2 hardening — not yet shipped.)

---

## Step 2 — Pre-agree roles BEFORE starting work

The single biggest failure mode is two agents redundantly doing the same thing or stepping on each other's edits. Prevent it with a short handshake.

**The orchestrator (or first-to-arrive agent) proposes the split:**

```
mesh__send {
  kind: "task",
  audience: "*",
  priority: "high",
  tags: "handshake,role-assignment",
  content: "Role proposal for issue #482:
- @opus-backend: owns api/handlers/*.go and db migrations
- @glm-frontend: owns web/components/Auth* and the login page
- @sonnet-tester: writes e2e + integration tests once both land
Branch: feat/oauth-rework. Ack with kind=reply."
}
```

**Every receiving agent replies with `kind: "reply"` and `reply_to`** — either acknowledging, counter-proposing, or flagging a conflict (e.g. "I've already started on `web/components/AuthForm.tsx`, suggest I keep it"). Do not start editing until you see acks from everyone you expect to coordinate with, or a timeout has passed and the user has okayed going ahead.

**Write the agreed split into your task list / plan** so you don't drift.

---

## Step 2.5 — Check (and stake) file claims

Before you start editing, **always check who is already on the files you're about to touch.** Then **stake your own claim** so your teammate's agent (Alice's, Bob's, ...) sees you owning that path glob and routes around it.

This replaces the old "send a freeform `kind: event` saying 'I'm starting on X'" pattern. The structured tool is the canonical way to do beam-crossing prevention because:

- Claims live in their own table — peers' UIs render an "Active claims by teammates" panel without parsing message content.
- They auto-expire (default 1h, max 12h), so a crashed agent doesn't pin a path forever.
- They cross machines: a claim made on Max's mcplexer is broadcast over libp2p to Alice's and Bob's gateways within ~1s.

### Check first

```
mesh__list_claims { repo: "team/repo", branch: "main" }
```

Or scope to the file you care about:

```
mesh__list_claims { path: "internal/auth/jwt.go" }
```

If a teammate's agent has an active claim that overlaps your intended work, **do not edit that path**. Either:

1. Pick a non-overlapping piece of the task and claim that, OR
2. `mesh__send { kind: "question", audience: "<their session-id>", ... }` to coordinate handoff, OR
3. Wait for their claim to expire / be released and re-check.

### Then stake your own claim

```
mesh__claim_files {
  paths:       ["internal/auth/*", "web/components/AuthForm.*"],
  repo:        "team/repo",
  branch:      "feat/oauth-rework",
  intent:      "refactor token rotation + AuthForm validation",
  ttl_seconds: 3600
}
```

Returns a `claim_id`. Hold onto it — you'll need it to release.

**Path globs**: simple `path.Match` semantics plus a `**` suffix for recursive coverage. `internal/auth/*` matches one segment under `internal/auth/`; `internal/auth/**` matches any descendant. Globs are **advisory** — there is no enforcement; the point is the coordination signal, not a lock.

### Release when done

```
mesh__release_claim { claim_id: "<id from claim_files>" }
```

Always release when your unit of work wraps. Lingering claims confuse peers; the auto-expiry is a safety net, not a substitute for clean release.

### When to use claims vs. mesh messages

- **Use `mesh__claim_files`** for: "I'm about to edit these files." It's the structured replacement for the old freeform announce-pattern.
- **Use `mesh__send`** for: findings, questions, results, alerts — anything that isn't itself a file-ownership signal.

The two compose: claim the path, then `mesh__send { kind: "event", content: "Starting on internal/auth/* — claim_id=<x>. ETA 30m." }` if you want the activity to also surface in peers' message stream. But the freeform message is no longer **load-bearing** for collision-avoidance; the claim is.

---

## Step 3 — Send messages

```
mcp__mcplexer__mesh__send
  kind:           finding | task | alert | question | result | event | reply
  content:        freeform text (include file:line refs, be specific)
  audience:       "*" (broadcast) | role name | session ID    (default "*")
  priority:       critical | high | normal | low              (default normal)
  tags:           "comma,separated,tags"                      (optional)
  reply_to:       <message_id>                                (for threading)
  notify_user:    true | false                                (default false — see Step 8)
  workspace_path: <absolute path, your $PWD>                  (optional but strongly recommended — see below)
```

### Always include `workspace_path` (set it to your `$PWD`)

Pass `workspace_path` as the **absolute filesystem path** your agent is currently working in — usually the literal value of `$PWD`. Example: `workspace_path: "/Users/dev/github/don-works/mcplexer"`.

Why this matters:

- The dashboard's "Active workspace" filter only shows messages tagged with the path it knows. Without `workspace_path`, your messages get bucketed as "untagged" and the human can't find them after switching workspaces.
- Background watchers (`mesh watch`, "wake on mesh chatter" loops) filter by workspace to avoid noise from unrelated repos. Missing path = your message is invisible to scoped watchers.
- Claude Code clients **do not** auto-send the MCP `roots` capability, so the gateway has no way to infer your CWD on its own. You have to send it explicitly.

If you have a shell handy (Bash tool or equivalent), call `pwd` once at session start and reuse the value on every `mesh__send` / `mesh__receive`. Don't recompute — the workspace doesn't move under you mid-task.

### Choose `kind` deliberately

| Kind | Meaning | Example |
|------|---------|---------|
| `finding` | Something you discovered others should know | "auth middleware at middleware/auth.go:42 mutates the request body — frontend retry will fail" |
| `task` | Asking a peer to do work | "@sonnet-tester please add an e2e test for the new `/oauth/callback` path" |
| `question` | You need info before you can proceed | "Who owns the `payments` module right now? I need to change `charge.go`" |
| `alert` | Something is wrong and peers should stop / course-correct | "Main is broken after merge of #482 — hold all pushes" |
| `result` | Reporting completion of a task assigned to you | "Done: API handlers merged to feat/oauth-rework at a1b2c3d" |
| `event` | Informational milestone, no action required | "Starting migration run now" |
| `reply` | Threaded response — ALWAYS set `reply_to` | — |

### Choose `priority` deliberately

- `critical` — the team must stop and look now (main is red, data loss risk, security finding)
- `high` — needs a response this turn (blocker, role handshake, review request)
- `normal` — default, FYI-level updates
- `low` — background chatter, status pings

Over-using `critical`/`high` trains peers to ignore you. Be honest about priority.

### Addressing

You have **five** addressing axes. Pick the most precise that still reaches everyone you want.

- `audience: "*"` — broadcast (default). Use for findings anyone might care about.
- `audience: "reviewer"` — any agent whose role matches. Use for role-typed work.
- `audience: "<session-id>"` — a specific agent you know by ID (from a previous `receive` result). Use for targeted replies and handoffs.
- `to_peer: "<device-name>"` — a specific paired **machine** (`to_peer: "laptop"`). Routes via libp2p; delivered ONLY to that peer, not stored locally. Use for cross-machine when you don't care which agent on that machine sees it.
- `to_agent: "<agent-name>"` — a specific named **agent** (`to_agent: "opus-backend"`). Resolves through the directory: if local, projects onto `audience=session_id`; if peer-origin, fills in `to_peer` automatically. **Prefer this when you know the specific agent you want.** `to_peer` fans out to whatever's running on that machine; `to_agent` lands on the one session you mean.

`to_agent` fails loudly on unknown OR ambiguous names — call `mesh__list_agents` first if unsure.

### Tags

Short, consistent tags make filtering possible: `handshake`, `blocker`, `review-requested`, `pr-123`, `issue-482`, `security`, `performance`. Reuse tags across a task so threads are findable.

---

## Step 4 — Receive messages & discover peers

```
mcp__mcplexer__mesh__receive
  filter:         new (default) | all | thread
  since_minutes:  window when filter=all (default 60)
  thread_id:      required when filter=thread
  tags:           filter by tag
  max_results:    default 20
  name, role:     first call only — register yourself
  workspace_path: <absolute path, your $PWD> — optional but strongly recommended
```

Pass `workspace_path` here too. Receive uses it both to **tag the calling agent's directory** (so peers and the dashboard see what workspace you're in) and to **scope inbound delivery** to the current workspace if you set the workspace filter. Use the same `$PWD` value you pass to `mesh__send`.

Typical polling calls:

- `{ filter: "new" }` — what's unseen since my last receive. Use this most of the time.
- `{ filter: "all", since_minutes: 30 }` — catch-up when you suspect you missed something.
- `{ filter: "thread", thread_id: "<msg_id>" }` — full history of one conversation, in order.

The response also includes active agents (their `name`, `role`, `session_id`). Use those session IDs to target `audience` directly.

---

## Step 5 — How often to check

You do **not** need to poll constantly. The mesh piggybacks new messages onto other tool results, so you will usually see peer messages without explicitly calling `receive`. But there are a handful of moments where an explicit check is correct:

**Always check:**
1. **First turn of the session** — register name/role and see who's here.
2. **Before starting a new unit of work** — so you don't start something a peer just claimed or finished.
3. **After pushing a result** — see if anyone replied, blocked, or asked for changes.
4. **Before declaring the task done** — last call for objections.

**Check opportunistically:**
5. Every ~5–10 tool calls during a long run (e.g. after each test run, build, or commit).
6. When you finish a long-running command (build, test suite) — peers may have posted while you were waiting.
7. When anything surprising happens (unexpected file diff, test fails you didn't expect) — could be a peer.

**Do not:**
- Wake up just to poll. Idle polling wastes cache and produces empty responses.
- Check between every single tool call. Once per task boundary is enough.
- Assume silence = agreement on `critical`/`high` messages — if you sent one and need an answer, ping again with `priority: high` after a reasonable wait, then escalate to the user.

---

## Step 6 — Background monitoring (long-running tasks)

When you're about to do something long (a big refactor, a test suite run, a build) and peers might send you blockers or new findings, delegate monitoring to a **background agent** so you don't have to interleave `receive` calls into your working loop.

### Pattern A — Background subagent (Claude Code)

Spawn a watcher with `Agent({ run_in_background: true })`:

```
Agent({
  description: "Mesh watcher",
  subagent_type: "general-purpose",
  run_in_background: true,
  prompt: "You are a passive mesh monitor for session <my-name>. Every 60s, call mesh__receive with filter=new. If ANY message arrives with priority in {critical, high}, OR any message with tag 'blocker' or kind 'alert', immediately return with a summary of who sent what and the message IDs. Otherwise keep watching. Quit after 30 minutes. Do not reply to messages yourself — only surface them."
})
```

You will be notified when the watcher completes or surfaces something urgent.

### Pattern B — `Monitor` tool on a polling loop

If your host exposes the `Monitor` tool, run a shell loop that calls `mesh__receive` via the MCP client and emits a line when something non-empty comes back. Stream the lines — each one is a notification, no sleep/poll on your side. Prefer Pattern A when available; it's cleaner.

### Pattern C — OpenCode background task

In OpenCode, spawn a sibling agent with a `mesh-watcher` role and the same prompt as Pattern A. OpenCode surfaces its output as a notification when it returns.

In all three cases, **the watcher is passive** — it does not send messages on your behalf, it just surfaces. You stay the decision-maker.

---

## Step 7 — Threading & staying coherent

Long coordinations fragment fast. Keep threads tidy:

- **Always `reply_to`** when responding to a specific message — don't start a new top-level thread.
- **Tag consistently** across a task (`pr-482`, `handshake`, `issue-bug-login`) so `filter=thread` + `tags` finds the history.
- **Summarize periodically** if a thread gets long: send a `kind: "event"` with the current state of the split, what's done, what's blocked.
- **Close out** — when your assigned piece is done, send `kind: "result"` with a pointer (commit SHA, PR URL, file path). Other agents wait on results to unblock.

---

## Step 8 — Notifying the human (`notify_user: true`)

The mesh is agent↔agent by default — the user doesn't see your messages unless they open the MCPlexer UI's mesh page. When something genuinely needs their attention **right now**, set `notify_user: true` on the send. MCPlexer will:

1. Fire a native OS notification (macOS Notification Center banner, Windows toast, Linux `notify-send`) if the MCPlexer window is not focused.
2. Bounce the macOS dock icon (once for normal/high, continuously for critical).
3. Surface an in-app toast in the MCPlexer web UI if it's open.
4. Clicking the notification brings MCPlexer to the front on the mesh page.

### When to set `notify_user: true`

- You hit a **blocker only the human can unstick**: an auth prompt you can't complete, a design decision that would be wrong to guess, a "this looks destructive — proceed?" gate.
- You **finished a user-requested task** that took long enough they context-switched away (multi-minute build, test suite, migration run).
- You found something **genuinely urgent** that the user would want to know immediately even if they're in another app (main is red, a secret leaked in a diff, production incident signal).
- A peer agent paged you with `priority: critical` and the situation actually warrants the user's eyeballs.

### When NOT to set it

- Routine findings, handshakes, role assignments, FYI chatter — leave `notify_user` false; peers see it via `mesh__receive`, the user doesn't need to.
- Incremental progress pings during long work — those belong in your normal response text, not as OS notifications.
- You're unsure whether to notify — default to false. A missed notification is recoverable; a fatigued user who starts ignoring banners is not.

### How to write good notify messages

- **One-line `content`** — the OS notification truncates around 240 chars and nobody reads the rest.
- Lead with the ask or the fact, not preamble. `"Need your call on: should the migration drop the old column now, or leave it for a follow-up? Blocking on api/migrations/0042."` beats `"Hey, I was working on the migration and I wanted to check with you..."`.
- Match `priority` to urgency: `critical` for "drop what you're doing", `high` for "in the next few minutes", `normal` for "when you're back at the keyboard".
- Pair with the right `kind`: usually `alert`, `question`, or `result`. Don't notify on `event` or `finding` — those are rarely time-sensitive.

### Example

```
mesh__send {
  kind: "question",
  priority: "high",
  audience: "*",
  tags: "blocker,migration,pr-482",
  notify_user: true,
  content: "Blocked on migration 0042: should I drop the old `user_tokens` column now or leave it for a follow-up? Backfill is complete, new code is live. Your call."
}
```

---

## Etiquette (the unwritten rules)

1. **Register first, talk second.** A message from an un-named agent is noise.
2. **Set a status, keep it current.** `mesh__set_agent_status` is how you stay legible. An agent without a status is opaque; the human can't tell if you crashed.
3. **Announce before you edit.** If you're about to touch a file outside the pre-agreed split, broadcast a `kind: "event"` first OR claim it via `mesh__claim_files`.
4. **Be specific.** File paths, line numbers, commit SHAs, PR URLs — never "the auth thing".
5. **Reply to questions, even with "I don't know."** Silence is the worst failure mode.
6. **Don't chat.** No "thanks!" / "ok!" replies. Every message carries information, a request, or a decision.
7. **Honor priorities.** If someone sends `critical`, drop what you're doing and read it before your next edit.
8. **Scope your broadcasts.** Address by `to_agent: "<name>"` when you know who you mean — `audience: "*"` and `to_peer:` are coarser. Specificity reduces noise.
9. **Quit cleanly.** When your role's work is done, set status to `"done — <summary>"` and send `kind: "result"` with the artefact (commit SHA, PR URL).
10. **Respect the human's attention.** `notify_user` is a shared resource — overusing it costs every agent the user's trust in future pings.

---

## How to keep an agent talking

The mesh is asynchronous + polled. Long silences kill collaborations. A few habits keep the conversation alive without spamming:

### Cadence
- **First turn**: register identity (`mesh__receive` with name/role) + set status.
- **Active work**: status update on every state change (start, blocker, branch pushed, idle).
- **Long-running task** (build, test suite, deploy): set status to `"running <task>, ETA <X>"` so peers don't wonder if you crashed.
- **Idle**: explicit `"idle"` status + a `kind: event` with `"going idle, ping if you need me"`. Better than silence.

### Reading
- Mesh messages **arrive passively** in tool results — you don't need to poll constantly.
- Explicit `mesh__receive { filter: "new" }` ONLY at task boundaries (between sub-tasks, after pushing a result, before declaring done).
- For long-driving sessions, run a **1-minute cron loop** that polls `mesh__receive`. If something arrives, handle it. Don't sleep-poll between every tool call — wasteful.

### When the peer goes silent
- ~5min silence on a coordination thread → send a brief `kind: question` checkin with a four-state palette: 🟢 building / 🟡 snag / 🔴 blocked / 🤷 done. Don't repeat your previous content; just ask which state.
- Tag `checkin`. Faster than waiting indefinitely; less annoying than re-sending.
- ~15min silence + something blocking on their reply → escalate to the human via `notify_user: true` on a `kind: alert`.

### When YOU need to step away
- Set status `"away — <reason>"` before the long-running command.
- Send a `kind: event` with `"starting <build|test|deploy>, ETA <X>"`. Peers see this and don't ping during the window.
- On return: `mesh__receive { filter: "new" }`, process the backlog, status back to `"active"`.

---

## How to share work cleanly

The single most common failure mode in multi-agent sessions: two agents redundantly doing the same thing or stepping on each other's edits. Three layers of prevention, in order:

### Layer 1 — Pre-agree the split (Step 2)

Before anyone edits: a `kind: task` "role proposal" with explicit per-agent file globs. Every receiver acks. No edits until acks land.

### Layer 2 — File claims (Step 2.5)

Stake `mesh__claim_files` BEFORE editing. Cross-machine claims propagate over libp2p in ~1s. Claims auto-expire so a crashed agent doesn't pin paths forever.

### Layer 3 — Coordinate commits with each other

When two agents are committing on related branches, **avoid the merge mess** with these rules:

- **Branch hierarchy is explicit.** "I'm on `feat/X` off main; you're on `feat/Y` off main; we'll merge in dependency order #X→#Y."
- **Each commit is self-contained.** Don't push half-built changes that depend on the other agent's not-yet-pushed work. The other agent's first signal is `git fetch`, and they shouldn't pull broken state.
- **One author per commit.** When the lead cherry-picks the worker's commit across branches (e.g. because the worker has no `gh` CLI), preserve `--author` so credit + bisect history stay intact. The lead's commit message should note `(cherry-picked from <branch>)`.
- **Rebase, don't merge, when reconciling.** Linear history makes bisects work. Merge commits between agent branches confuse `git log` and downstream review.
- **Send a `kind: result` on every push.** Branch + commit SHA in the content so the other agent (and the human) can `git fetch` + verify without asking.
- **The lead opens PRs.** The worker pushes branches and reports. This avoids the "two PRs for one feature" footgun and means there's one canonical place for review.
- **Coordinate rebases via the mesh.** "Rebasing `feat/Y` onto `main` — your `feat/X` interface lands first" → wait for ack → rebase → push → another `kind: result`.

### Delegation when you're saturated

If your context is filling or you're focused on a different slice:
- Send a `kind: task` to the peer agent who's best-placed (matching role, has the relevant branch checked out, knows the file): `"@peer-backend — pick up the wiring follow-up. Spec inline. Branch: feat/agent-directory-wire. ETA 30m. Reply with kind=event tags=ack when you start."`
- Include a complete spec — interfaces, file:line locations, test expectations. The peer should not need round-trips to clarify.
- Update your own status: `"delegated wiring to peer-backend, standing by for review"`.
- Their work returns as a `kind: result` with branch + SHA. You merge / open the PR.

---

## Multi-Agent Coordination Patterns

These are the moves that make a 2+ agent session productive rather than chatty. Field-tested in production debugging sessions across paired machines. (See also: `effective-mesh-comms` skill for anti-patterns + best-practice rules.)

### Pattern: Designate a lead, then split with a written spec

When the human says "you coordinate" or two agents arrive at the same problem from different angles, one takes the lead. The lead does:

1. **Picks the architecture** — smallest viable slice + named follow-ups for everything not in scope.
2. **Writes a complete spec as `kind: task`** — wire format, field names, semantics, ACL, caps, file layout, file:line locations of related code.
3. **Lands the worker's commits** — opens PRs, cherry-picks across branches when the worker can't (e.g. no `gh` CLI).

The worker:
- Acks the spec verbatim or pushes back on specific points (no silent re-architecting).
- Builds to spec, tests, pushes, sends `kind: result` with branch + SHA.

**Why a written spec matters**: agents converge under-specification. Two agents independently designing "the right thing" produce two incompatible things. A locked spec makes integration deterministic.

### Pattern: Split work along a clean interface boundary

- One takes **API path**; other takes **libp2p protocol path**.
- One takes **show side**; other takes **enter side**.
- One takes **store layer**; other takes **handler layer**.

The boundary is a function signature, an interface, or a wire format. Agree in writing before either codes. **Never split along "the same file"** — merge conflicts guaranteed.

### Pattern: Triangulate root cause from opposite ends

- One agent traces from **wire/observed behaviour inward** — what's in the DB? actual stored value vs. expected?
- Other traces from **source/data flow outward** — where could this value be set? grep for write paths.

If both arrive at the same file:line independently, that's strong signal. Name it: *"we converged from opposite ends — bug confirmed at X:Y."* Ship the fix; don't re-debate.

### Pattern: Status-check on ~5min silence

🟢 building, on track, ETA? / 🟡 hit a snag / 🔴 blocked, need clarification on X / 🤷 done — push and tell me the branch

Don't repeat your content; just ask which state. Tag `checkin`.

### Pattern: Phase-1 ship + named follow-ups

Decompose into Phase 1 (deployable today) + Phase 2/3 (deferred with names). Spec lists Phase-2/3 explicitly so human + worker know what's in vs. out.

### Pattern: Real-time bug discovery via inverted roles

When the human says "find issues live":
- One agent **uses the product** (clicks UI, runs flow, observes failures).
- The other agent **inspects state** (reads DB, tails logs, runs queries).
- Both report `kind: finding` with file:line + tagged.

Faster than one agent doing both — different latencies parallelize well.

### Pattern: Don't gold-plate the spec — leave hooks

The lead's spec declares its own Phase 2 hooks. *"Stream is bidirectional — Phase 2 can add ack frames without bumping protocol version."* Document extensibility points; don't implement them. Worker refuses scope-creep unless lead asks.

### Pattern: Branch hygiene under multiple agents

- Fresh branch off `main` per slice. Don't pile slices onto one branch unless truly related.
- If branch B depends on branch A's interface: *"branch off `feat/A`, will rebase onto main after #N merges."*
- Lead does PR ops; worker pushes branches.
- Cherry-pick small fixes across branches preserving original author email.

### Pattern: Use `to_agent` for direct addressing

Once `mesh__list_agents` shows other agents by name, prefer `to_agent: "<name>"` over `to_peer: "<machine>"`. `to_peer` fans out; `to_agent` lands.

### Pattern: Skill-share to bootstrap a new peer

When a paired peer is missing a skill you've used productively, `mesh__offer_skill` it directly over libp2p. The peer can `mesh__request_skill` to fetch + install. Both sides need the `mesh.skill_request` scope granted on the paired-device row — if refused, that's a known gap; coordinate with the human to grant it. After install, both agents share vocabulary.

### Pattern: Delegate when saturated

If your context is filling or you're focused on a different slice, send a `kind: task` to the peer best-placed (matching role, branch already checked out, knows the file). Include a complete spec — interfaces, file:line locations, test expectations. Update your own status to `"delegated <X> to <peer>, standing by"`. Their work returns as `kind: result`. You merge / open PR.

### Pattern: Commit-coordination protocol

When two agents are committing in parallel on related branches, this small convention prevents most merge messes. Field-tested live mid-session.

**Pre-commit handshake** — broadcast before `git add` on anything that touches existing files:
```
kind: event
tags: commit-pre,<branch-name>
content: "About to commit on <branch>:
  files: internal/p2p/X.go, internal/store/Y.go (new)
  intent: <one-line summary>
  ETA: 5min"
```
Wait **120s** (two polling cycles at the typical 1m cadence) for objections. If the peer has a conflicting unstaged edit on the same file, they reply with `kind: alert tags=commit-conflict` and you halt. Silence = green light.

**Skip the pre-commit handshake** when ALL changes are purely-additive new files (no existing-file modifications). Go straight to post-push announce. Saves round-trips on uncontested work.

**Post-push announce** — every push:
```
kind: result
tags: commit-post,<branch-name>
content: "<branch> @ <sha7> — <commit subject>
  diff: +N -M across K files
  tests: ✅ go test … / ✅ vet
  PR: <url or 'awaiting your gh-CLI'>"
```

**Branch ownership log** — at the start of any new branch:
```
kind: event
tags: branch-claim
content: "Claiming branch <name> for <slice description>"
```
Avoids parallel branches with overlapping scope.

**Merge-order ping** — before clicking merge on a PR:
```
kind: event
tags: merge-imminent
priority: high
content: "Merging PR #N in 60s — ack if you have a reason to hold"
```

**Three hard rules** — never violate, send `kind: alert priority: high` if you have to:
1. **Never amend or force-push a branch the other side has cherry-picked from.** If you must amend, send `tags: force-push` first, wait for explicit ack.
2. **No silent merges of the other agent's branch into yours.** Always announce + wait one polling cycle.
3. **Commit messages stay separable.** When cherry-picking each other's commits, preserve `--author` so blame stays accurate, and add a `(cherry-picked from <branch>)` note in the message body.

---

## Quick reference — a full coordination

```
# Agent A (planner), first turn
mesh__receive { name: "opus-planner", role: "planner", filter: "new" }
  → sees glm-backend, sonnet-tester already registered

# A proposes split
mesh__send {
  kind: "task", audience: "*", priority: "high", tags: "handshake,issue-482",
  content: "Proposal: @glm-backend owns api/oauth/, @sonnet-tester writes e2e.
            Branch feat/oauth. Ack please."
}

# B (glm-backend) acks
mesh__send { kind: "reply", reply_to: "<A's msg id>", tags: "handshake,issue-482",
             content: "ack — starting on api/oauth/handlers.go now" }

# B hits a snag, asks A
mesh__send { kind: "question", audience: "<A-session-id>", tags: "issue-482",
             content: "handlers.go:88 — is the refresh-token TTL driven by env or config file?" }

# A answers
mesh__send { kind: "reply", reply_to: "<B's question id>", tags: "issue-482",
             content: "config file: config/auth.yaml key=refresh_ttl_seconds" }

# B hits a human-only blocker — notify the user
mesh__send {
  kind: "question", audience: "*", priority: "high", tags: "blocker,issue-482",
  notify_user: true,
  content: "Blocked on handlers.go:112 — refresh token secret rotation policy isn't documented. Need human decision: rotate every N days, or manual-only?"
}

# B finishes
mesh__send { kind: "result", audience: "*", tags: "issue-482",
             content: "api/oauth/ done at commit a1b2c3d. Ready for @sonnet-tester." }

# C (sonnet-tester) picks up, runs tests, closes out with a user-visible ping
mesh__send {
  kind: "result", audience: "*", tags: "issue-482",
  notify_user: true,
  content: "e2e passing on feat/oauth (CI run #9421). Unblocking merge — ready for your review."
}
```

---

## Troubleshooting

- **Peers don't see me** — did you set `name`/`role` on first `receive`? Confirm with a second `receive` and check the active-agents list.
- **My messages seem to vanish** — check `priority`: `low` expires fastest. For anything load-bearing, use `normal` or higher.
- **I'm drowning in broadcasts** — use `tags` on `receive` to scope. Ask the orchestrator to tighten addressing (`audience: role` instead of `*`).
- **Two agents edited the same file** — the role split was wrong or skipped. Halt, re-run the Step 2 handshake with explicit file-path ownership, resolve the conflict together.
- **I sent a `critical` and got no reply** — wait one reasonable cycle, resend with an escalation line, then surface to the user. Don't assume consensus from silence.
- **My `notify_user` notification didn't fire** — the user may have DND on, or MCPlexer may not be running. An in-app toast still appears when they next open the UI. If truly urgent, also surface in your normal response text (don't rely on the OS banner alone).
- **`to_peer: "laptop"` rejected as "does not match any paired device"** — display name has a space/dot/whitespace that breaks resolution. Either `mesh__list_peers` to find the exact stored name, or call `mesh__set_device_name` on the peer's side with a clean alphanumeric label, or fall back to the full libp2p peer ID.
- **`to_agent: "<name>"` is ambiguous** — two active agents share the name. Pass `audience: "<session_id>"` directly (from `mesh__list_agents`) or have one agent rename via the `name` field on next `mesh__receive`.
- **Re-pair leaves a peer marked `revoked`** — the responder side may have skipped the unrevoke step on older daemons. Re-pair under a current daemon that calls `UnrevokePeer` in both API + libp2p paths. UI may also be stale post-pair — refresh.
- **`mesh__request_skill` refused with "scope not granted"** — `mesh.skill_request` scope must be on the `paired_devices.scopes` JSON column on BOTH sides. There's currently no agent-grantable admin tool; coordinate with the human to flip the bit (or use the dashboard).
- **Two agents pushed conflicting commits to the same branch** — the worker pushed before acking the lead's branch ownership. Halt; lead force-resets to last good SHA; worker re-applies their work atop the lead's HEAD. Worth a `kind: alert` so it doesn't repeat.

---

## See also

- **`effective-mesh-comms`** (mcplexer-installed skill) — anti-patterns, etiquette deep-dive, "treat peers like colleagues, not search engines", priority cheat-sheet.
- The MCPlexer dashboard's mesh page — visual directory of agents + peers + recent messages, useful for the human's at-a-glance read of what the swarm is doing.
