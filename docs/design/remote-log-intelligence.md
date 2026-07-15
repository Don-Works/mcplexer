# Remote Log Intelligence

**Status:** draft for ratification · **Epic:** see task ledger (`tag:logwatch`) · **Author:** planning session 2026-07-08

SSH-pull docker logs off remote machines, distill them into token-cheap digests
(template mining + duplicate counting), and wake a **built-in log-watch worker**
only when something new or bad appears — with configurable escalation levers
(task / task+telegram / telegram+whatsapp).

## 1. Problem

Production boxes emit logs we only look at when something is already on fire.
We want mcplexer to watch them continuously, but naively piping logs at a model
is token-suicide: 12k lines of the same connection-refused error is one fact,
not 12k facts. And each remote box should require zero footprint — no agent
install, no log shipper — just SSH reachability.

## 2. Goals

1. **Remote hosts as first-class, generic, workspace-scoped config** — not
   bespoke per-box scripts. A host + its log sources live in a workspace like
   servers and routes do.
2. **Zero-footprint collection** over SSH (docker first; journald/files later).
3. **Token-cost engineering as a core feature**: dedupe, count, and
   novelty-detect *before* any model sees a byte. Quiet ticks cost zero tokens.
4. **Built-in log-watch worker** (same pattern as `memory-consolidator`
   autoinstall): one lever to turn on per workspace, template in the skill
   registry, budget-capped, auto-pausing.
5. **Escalation levers** as declarative policy: severity → {nothing | create
   task | task+telegram | telegram+whatsapp}, with storm-proof throttling.

## 3. Non-goals (v1)

- **No remediation, full stop (ratified 2026-07-08).** This feature is
  read-only against remote boxes and signal-output only: templates, digests,
  mesh alerts, tasks, notifications. Acting on findings — restarts,
  rollbacks, config changes — is repo-specific judgment that belongs to
  downstream agents picking up the filed task; it is NOT this feature's
  responsibility and never will be. Read-only is enforced by construction:
  the SSH layer exposes no generic exec path — only the fixed read-only
  per-kind argv templates (`docker logs`, `journalctl`, `tail`) exist, so
  adding any mutating command shape requires a new ADR, not a config change.
- No log shipping agents / no mcplexer install required on watched boxes.
  (If a box *is* a paired p2p peer, a mesh transport can come later; SSH is
  the universal baseline.)
- No embedding/semantic clustering in v1 — deterministic template mining
  first. Embeddings are a stretch milestone (M6) if template mining leaves
  residual duplication.
- No full-text log warehouse. Bounded ring buffers + template store, not Loki.
- No streaming `-f` follow in v1 — cursored incremental pulls (robust across
  reconnects). Follow-mode is M6.

## 4. Concepts

| Entity | Scope | What it is |
|---|---|---|
| `remote_host` | workspace | SSH target (user@host:port), auth ref, host-key pin, health |
| `log_source` | workspace | one stream on a host: docker container / compose project / journald unit / file path, poll cadence, retention caps |
| `log_template` | per source | masked line shape ("pgx: connection refused host=…") + count, first/last seen, severity class, sample raw line |
| `log_pull` | per source | cursor state + pull stats (lines, bytes, new templates) |
| `monitoring_channel` | workspace | one alert output: gchat webhook / telegram / whatsapp / mesh, config (secret refs), `min_severity`, enabled |
| digest | computed | budget-bounded render of a window: new templates first, then error classes, then count deltas |
| watch worker | workspace | built-in Worker wired to the digest; zero-spend gate on quiet ticks |
| escalation | daemon code | `monitoring.notify` dispatcher: deterministic envelope + channel fan-out by `min_severity` + throttles |

## 5. Architecture

```
                 ┌────────────────────────── mcplexer daemon ──────────────────────────┐
 remote box A ──ssh──► collector ──► redactor ──► distiller ──► SQLite (templates,     │
 remote box B ──ssh──►  (cursored     (secrets     (mask →      cursors, ring buffer)  │
                         pulls)        scrub)       template,        │                 │
                                                    count,           ▼                 │
                                                    novelty)   monitoring.* namespace  │
                                                                (code-mode tools)      │
                                                                     │                 │
                                             anomaly rules ──mesh──► │                 │
                                                                     ▼                 │
                                                        built-in log-watch worker      │
                                                        (interval + mesh trigger,      │
                                                         pre_execute zero-spend gate)  │
                                                                     │                 │
                                                     escalation policy engine          │
                                                     task / telegram / whatsapp        │
                 └─────────────────────────────────────────────────────────────────────┘
```

Placement: `internal/logwatch/` with subpackages `sshx` (executor),
`collect` (pull loop), `distill` (mask/mine/digest), `escalate` (policy).
Wiring follows `cmd/mcplexer/workers_wiring.go` conventions. 300-line file cap
and 50-line function cap apply as usual.

### 5.1 Collector

- Scheduler job per enabled source (reuses `internal/scheduler` specs — cron
  or duration, default `1m`–`5m` per source).
- Incremental pull: `docker logs --since <cursor> --until <now> <container>`
  executed as **argv, never shell string**, over a pooled SSH connection.
- Cursor = last-pulled timestamp + a rolling hash of the tail line to detect
  container restarts / log rotation (restart ⇒ cursor reset + `event`
  template noting the restart — that itself is signal).
- Caps per pull: max bytes (default 4 MiB), max wall clock (default 30s),
  truncation is recorded as a synthetic template (`logwatch: pull truncated`)
  so silent gaps can't hide.
- Health: consecutive-failure counter per host/source → status on dashboard +
  mesh alert at threshold (mirrors worker `max_consecutive_failures`).

### 5.2 Redaction

Before any byte is persisted: reuse `internal/audit` redaction +
`internal/sanitize` patterns (bearer tokens, api keys, emails, secret-shaped
strings). Redaction runs *before* storage so raw ring buffers are already
clean — digests can't leak what was never stored.

### 5.3 Distiller — the token-cost engine

1. **Normalize**: strip ANSI, ISO timestamps, then mask volatile atoms:
   uuids, hex ≥8, ints ≥3 digits, IPs, quoted strings > 24 chars, durations,
   paths with digits. Result = template text.
2. **Template identity** = SHA-256 of (source_id, masked text). Upsert:
   `count += n`, `last_seen = now`, keep first-seen raw line as sample and
   most recent raw line as `last_sample`.
3. **Severity class** per template via ordered regex rules (fixed defaults +
   per-source overrides): `panic|fatal|OOM|SIGSEGV|data race` → critical-class,
   `error|exception|traceback|refused|timeout` → error-class,
   `warn` → warn-class, else info-class.
4. **Novelty**: template never seen before on this source ⇒ `new=true` for
   the next digest window; new **error-class** templates are the primary wake
   signal.
5. **Anomaly rules** (deterministic, cheap, run at pull time):
   - new error/critical-class template
   - rate spike: error-class count in window > K× trailing baseline (default 5×, min 10)
   - collector/source went dark: three consecutive failed scheduled pulls.
     A successful pull with zero lines is healthy, so genuinely quiet services
     do not page. Failed delivery is retried under one episode id; a successful
     pull re-arms the next outage.
   Each enters the deterministic dispatcher once; configured channels receive
   it by severity, and a mesh channel can wake the AI worker for triage.
6. **Digest renderer** — `logs.digest({source_ids?, window, budget_tokens})`
   fills the budget in priority order: new critical/error templates → rate
   spikes → new info templates → top count deltas → steady-state summary
   line. Every entry is `count × template + first/last ts + one sample line`.
   A 12,481-line window typically renders in ~600–900 tokens:

   ```
   [api-prod/docker:api] 15m: 12,481 lines → 37 templates (2 new, 3 error-class)
   NEW ✱ ×214  ERR  "pgx: connection refused host=<*> attempt=<n>"   14:02:11→14:16:59
         sample: 2026-07-08T14:02:11Z ERROR pgx: connection refused host=db-3 attempt=7
   ×4,812 INFO "GET /healthz 200 <n>ms"                              (steady)
   ```

### 5.4 `monitoring.*` namespace (code-mode, workspace-scoped)

Named to match the UI feature ("Monitoring", ratified 2026-07-08).

| Tool | Purpose |
|---|---|
| `monitoring.hosts` / `monitoring.sources` | list config + health |
| `monitoring.channels` | list configured alert channels (kind, min_severity, enabled — config secret refs redacted) |
| `monitoring.stats({source_ids?, window})` | cheap counters: lines, new_templates, error_delta — **the zero-spend gate reads this** |
| `monitoring.digest({source_ids?, window, budget_tokens, min_severity?})` | budget-bounded digest |
| `monitoring.search({source_id, q, limit})` | grep the ring buffer (regex), capped output |
| `monitoring.raw({template_id, limit})` | recent raw lines for one template (drill-down) |
| `monitoring.ack({template_id, note?})` | mark a template known/expected → excluded from novelty wake-ups |
| `monitoring.notify({severity, title, body, remote_host_id?, source_id?, template_id?})` | **the only send path** — daemon renders the deterministic envelope and fans out to channels (§5.6) |

Admin CRUD (`mcplexer__create_remote_host`, `…_log_source`,
`…_monitoring_channel`) stays CWD-gated like all admin tools; `monitoring.*`
tools are ordinary workspace tools so watch workers and interactive agents
can use them.

### 5.5 Built-in log-watch worker

Exactly the `memory-consolidator` pattern (`cmd/mcplexer/consolidator_autoinstall.go`):

- Worker template `log-watch` published in the skill registry
  (`publish_worker_as_template` / `install_worker_template`).
- Autoinstall per workspace behind `MCPLEXER_AUTO_INSTALL_LOG_WATCH=1`,
  idempotent, skips when no api_key scope or no enabled log sources exist.
- Schedule: `10m` default + a mesh trigger on `kind:alert, tag:logwatch` so
  the first error-class anomaly wakes it immediately between ticks. The mesh
  trigger groups subsequent novel shapes for 5 minutes; the periodic sweep
  catches the batch without repeatedly paying to analyse the same 10m window.
- **`pre_execute_script` zero-spend gate**:
  ```js
  const s = monitoring.stats({window: "10m"});
  const forced = hook.run.trigger_kind === "mesh" || hook.run.trigger_kind === "manual";
  if (s.new_templates === 0 && !forced) abort("quiet");
  ```
  Quiet tick ⇒ status=blocked, zero model spend. Chronic known errors no longer
  wake the model every sweep; deterministic anomaly mesh triggers still force
  triage immediately.
- Prompt: triage skill + `monitoring.digest({budget_tokens: 2000})`. The
  worker classifies severity (can raise/lower the deterministic floor),
  writes a one-paragraph incident summary, then calls `monitoring.notify`
  ONCE — the daemon owns formatting and channel fan-out.
- `exec_mode: autonomous` with tool allowlist exactly:
  `["monitoring__*", "task__create", "task__list", "mesh__send"]`.
  The worker holds NO channel tools (no telegram/openwa/fetch) — it cannot
  send anywhere except through the deterministic dispatcher. Anything beyond
  notify-and-file (e.g. restarting a service) is downstream agents'
  responsibility entirely — see Non-goals; the filed task with its
  drill-down pointers is this feature's terminal output.
- Budgets: `max_wall_clock 300s`, `max_tool_calls 12`,
  `max_monthly_cost_usd 5` by default, `max_consecutive_failures 5`.
  Autoinstall converges these safety limits on existing workers while preserving
  an explicit positive operator-set monthly cap, schedule, model, and enabled state.

### 5.6 Escalation — channels + deterministic dispatcher

**Channels are workspace config rows** (`monitoring_channels`), CRUD'd from
the Monitoring UI — these are the levers. Each row: `kind`
(`gchat_webhook` | `telegram` | `whatsapp` | `mesh`), `config_json` (secret
refs only — e.g. a Google Chat incoming-webhook URL as
`secret://GCHAT_WEBHOOK_INCIDENTS`), `min_severity`, `enabled`. An incident
of severity S fans out to every enabled channel whose `min_severity` admits
S — the same self-selection model the googlechat manager already uses for
spaces (which remains available for service-account installs; webhooks are
the zero-setup path, ratified 2026-07-08).

**All sends go through `monitoring.notify` — daemon code, not the model.**
The dispatcher: (1) renders the deterministic envelope (below), (2) resolves
channel secret refs internally (plaintext never crosses into any model
context), (3) enforces throttles, (4) fans out. Anomaly rules (§5.3.5) call
the same dispatcher, so model-less signals carry the identical envelope.

**Deterministic message (ratified 2026-07-08)** — every outbound message
carries workspace + gateway host + affected host, rendered in Go, never by
the model. Rendering is **per channel kind** so channel-specific markup is
never delivered as literal noise to a channel that can't parse it:

- **gchat_webhook** (the rich channel) — compact lightweight-Markdown render
  with `*emphasis*` and a `<url|label>` clickable task link when a public URL
  is configured (`RenderMessage`):

  ```
  *{SEVERITY} · {workspace_name}*
  {title}

  *Host:* {remote_host_name} ({ssh_host})
  *Source:* `{source_name}`
  *Watcher:* `{gateway_hostname}`

  {body}

  *Task:* <{public_url}/tasks/{id}?workspace={ws}|{id}>
  *Template:* `{template_id}`
  ```

- **telegram / whatsapp / mesh** (plaintext channels, incl. dashboards that
  surface mesh text) — the deterministic envelope line, then title, source,
  body, and bare task/template refs, no Markdown (`RenderPlainMessage` →
  `Envelope`):

  ```
  [{workspace_name} · via {gateway_hostname}] {SEVERITY} · {remote_host_name} ({ssh_host})
  {title}
  ...
  ```

  e.g. `[example-system · via lxc-mcplexer] CRITICAL · prod-1 (203.0.113.10)`.

Task titles use the same fields: `[{workspace_name}] {severity}: {headline} @ {remote_host_name}`.

Remaining knobs in the worker's `parameters_json`:

```json
{
  "task":     { "min_severity": "warn", "tags": ["logwatch", "incident"], "dedupe_open_by_template": true },
  "throttle": { "per_template_cooldown": "1h", "max_notifies_per_hour": 6 }
}
```

Channel kinds (v1):

- **gchat_webhook** (primary, zero-setup): plain HTTP POST to a Google Chat
  incoming-webhook URL held as a secret ref. No service account needed. The
  existing `internal/googlechat` space-binding path still works for
  service-account installs — a `mesh` channel reaches it via notify-bus
  priority as before.
- **telegram**: workspace-bound chat via the built-in telegram manager.
- **whatsapp**: via the OpenWA downstream (`openwa__send_text`), dispatched
  through the gateway's internal downstream bridge; number as secret ref.
  Reserved for `min_severity: critical` in practice — the channel row is the
  recorded operator intent OpenWA requires.
- **mesh**: mesh alert with severity-mapped priority (also what wakes the
  worker and feeds googlechat space bindings).
- **PWA/Web Push human escalation** is daemon-wide rather than a channel row.
  A newly discovered `critical` incident publishes one durable Signal event
  before synchronously waiting for an enabled browser/PWA subscription to
  accept the push. Evidence updates do not push
  again; a post-remediation regression may alert when it creates a different
  canonical task. Lock-screen text contains only system/source context, never
  a raw log sample, and clicking opens the canonical task (or Monitoring while
  the first deterministic observation is still awaiting triage).
- **task filing** is not a channel — it's the worker's own `task.create`
  action governed by the `task` knob above (dedupe: if an open task already
  references the template id via meta key `logwatch_template`, append a note
  instead of filing a duplicate).

**Storm-proofing is layered**: mesh-trigger `throttle_seconds` (per-source),
severity-aware per-template cooldown, and independent hourly budgets. A higher
severity bypasses the existing template cooldown; lower-severity traffic gets
6 channel notifications/hour and cannot consume the separate critical budget
(12/hour). Durable Signal history records every new critical incident, while
lock-screen interruptions are capped at 6/hour. Transient channel and Web Push
failures receive three bounded attempts.

The current delivery contract is **accepted by at least one route**, not human
acknowledgement. Signal persistence survives reloads and push/channel failure is
observable, but there is not yet a durable channel outbox with acknowledgement,
re-page, and failover scheduling. Production operators should configure at least
two independent critical routes and run a synthetic end-to-end notification
drill; do not describe this as pager-grade until outbox/ack/re-page lands.

## 6. Data model (SQLite, migration NNN)

```sql
CREATE TABLE remote_hosts (
  id            TEXT PRIMARY KEY,          -- ulid
  workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
  name          TEXT NOT NULL,             -- unique per workspace
  ssh_user      TEXT NOT NULL,
  ssh_host      TEXT NOT NULL,
  ssh_port      INTEGER NOT NULL DEFAULT 22,
  auth_scope_id TEXT NOT NULL,             -- ssh key material / agent ref
  host_key_pin  TEXT,                      -- TOFU-recorded, change ⇒ hard fail
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, name)
);

CREATE TABLE log_sources (
  id             TEXT PRIMARY KEY,
  workspace_id   TEXT NOT NULL,
  remote_host_id TEXT NOT NULL REFERENCES remote_hosts(id),
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL,            -- docker | compose | swarm | journald | file
  selector       TEXT NOT NULL,            -- container name / project / unit / path (validated charset)
  schedule_spec  TEXT NOT NULL DEFAULT '2m',
  max_pull_bytes INTEGER NOT NULL DEFAULT 4194304,
  retention_mb   INTEGER NOT NULL DEFAULT 50,
  retention_days INTEGER NOT NULL DEFAULT 7,
  severity_rules TEXT,                     -- JSON overrides, nullable
  enabled        INTEGER NOT NULL DEFAULT 1,
  cursor_ts      TEXT, cursor_hash TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  error_spike_active INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, name)
);

CREATE TABLE log_templates (
  id           TEXT PRIMARY KEY,           -- sha256(source_id, masked)
  source_id    TEXT NOT NULL REFERENCES log_sources(id),
  masked       TEXT NOT NULL,
  severity     TEXT NOT NULL,              -- info|warn|error|critical
  count        INTEGER NOT NULL DEFAULT 0,
  window_count INTEGER NOT NULL DEFAULT 0, -- reset per digest window
  first_seen   TEXT NOT NULL, last_seen TEXT NOT NULL,
  sample_first TEXT NOT NULL, sample_last TEXT NOT NULL,
  acked        INTEGER NOT NULL DEFAULT 0, ack_note TEXT
);
CREATE INDEX idx_templates_source_seen ON log_templates(source_id, last_seen);
CREATE UNIQUE INDEX idx_templates_source_id ON log_templates(source_id, id);

-- raw ring buffer, bounded by retention_mb/days, redacted before insert
CREATE TABLE log_lines (
  source_id   TEXT NOT NULL REFERENCES log_sources(id) ON DELETE CASCADE,
  template_id TEXT NOT NULL,
  ts          TEXT NOT NULL,
  line        TEXT NOT NULL,
  FOREIGN KEY (source_id, template_id)
    REFERENCES log_templates(source_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_lines_source_ts ON log_lines(source_id, ts);

CREATE TABLE monitoring_channels (
  id           TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL,               -- gchat_webhook | telegram | whatsapp | mesh
  config_json  TEXT NOT NULL DEFAULT '{}',  -- secret refs only, never plaintext creds
  min_severity TEXT NOT NULL DEFAULT 'error',-- info|warn|error|critical floor
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, name)
);
```

Templates are tiny and persist past raw retention — long-term novelty memory
costs ~nothing.

## 7. SSH security model (→ ADR 0007)

1. **Auth**: BOTH mechanisms land in M2 (ratified 2026-07-08). (a) New
   auth-scope type `ssh_key`: key material age-encrypted in the existing
   secrets store; at dial time the key is loaded into memory only
   (golang.org/x/crypto/ssh signer) — never written to disk, never crosses
   into worker/model context. (b) `ssh_agent` passthrough scope for hosts
   where an agent socket is available.
2. **Host key pinning**: first successful dial records the pin (TOFU) on the
   `remote_hosts` row; any subsequent mismatch hard-fails the source and
   raises a `critical` mesh alert. No `InsecureIgnoreHostKey`, ever.
3. **Fixed shell command**: SSH exec carries one command string interpreted by
   the remote login shell (there is no protocol-level argv exec). Commands use
   fixed per-kind templates, and selectors are validated against
   `^[A-Za-z0-9._/][A-Za-z0-9._/-]*$` then single-quoted at CRUD and dial time.
4. **Bounded reads**: byte + wall-clock caps per pull; the SSH session is
   killed past deadline.
5. **Box-side privilege boundary**: Docker-group access is root-equivalent,
   not least privilege. Production should use a dedicated non-login account
   with a forced-command, root-owned exact-grammar wrapper (and forwarding/PTY
   disabled), or a dedicated rootless Docker daemon. See ADR 0007 §7.
6. **Redaction before persistence** (§5.2) — digest/model layers only ever
   see scrubbed text.

## 8. Milestones

| # | Deliverable | Acceptance |
|---|---|---|
| M0 | ADR 0007 (SSH security model) + this design ratified; migration drafted | ADR merged; open questions §9 answered |
| M1 | Store: migrations + models + CRUD for hosts, sources, channels (admin MCP tools + REST + validation) | table-driven store tests; `mcplexer__create_remote_host/log_source/monitoring_channel` round-trip; selector validation rejects shell metachars; channel config accepts secret refs only |
| M2 | `sshx` executor (ssh_key + ssh_agent auth) + collector loop (docker kind) + redaction + cursors + health | pull from the IP prod box in CI-skippable integration test; host-key change hard-fails; truncation template appears at cap; redaction test vectors pass |
| M3 | Distiller + `monitoring.*` namespace (incl. `notify` dispatcher + envelope) + anomaly rules | 10k-line synthetic corpus → <50 templates; digest respects budget_tokens ±10%; `monitoring.stats` returns in <50ms; new-error-template fires exactly one alert under storm test; every channel payload carries the deterministic envelope |
| M4 | Built-in `log-watch` worker template + autoinstall + escalation engine + levers | quiet tick = blocked run, zero spend; seeded error storm → 1 task + 1 gchat message, no dupes; a newly discovered critical incident sends one durable PWA/Web Push human alert; policy editable via `update_worker` |
| M5 | "Monitoring" page per workspace: hosts/sources/channels CRUD (settable gchat webhooks), template explorer, digest preview with token estimate, min_severity levers, install-worker button | e2e: add host → add source → add gchat_webhook channel → see templates → flip lever → worker installed; PWA passes existing lint/build |
| M6 | Stretch — DONE: journald (systemd unit) + compose (project) + swarm (service) source kinds via fixed read-only argv templates, multi-format leading-timestamp parsing, UI kind selector; `monitoring.ack` shipped in M3. REMAINING: file kind (needs byte-offset cursoring, not time), follow-mode streaming, embedding-assisted residual clustering, mesh transport for paired peers | journald/compose/swarm collected + tested; file explicitly refused with a clear message |

Rough sizing: M1–M4 are each ~2–4 focused sessions; M5 similar on the web
side. M2 and M3 are parallelizable after M1 lands (M3 can develop against
fixture corpora without SSH).

## 9. Decisions (Q&A with Max, 2026-07-08 — all six answered)

1. **First target**: the Intervals Pro production box (its docker
   containers). Bonus: IP already ships to Loki, giving a ground-truth
   comparison corpus for distiller validation.
2. **Chat channel**: Google Chat, not telegram — via the existing
   `internal/googlechat` manager. Escalation emits mesh alerts with
   severity-mapped priority; workspace-bound spaces receive them filtered by
   per-space `MinPriority` ("per thing" = per-space binding + threshold).
   Telegram stays as an optional off-by-default action.
3. **WhatsApp**: yes — critical-only, via the installed OpenWA MCP
   (`openwa__send_text`), number stored as `secret://WHATSAPP_PERSONAL_MSISDN`.
4. **SSH auth**: BOTH `ssh_key` scope and `ssh_agent` passthrough in M2.
5. **Retention**: defaults confirmed — 50 MB / 7 days per source.
6. **Wake floor**: error-class novelty (+ rate spikes + source-went-dark)
   wakes the worker immediately; info-class novelty batches into the next
   scheduled tick's digest.
7. **`docker logs` is THE collection contract** (ratified 2026-07-08): our
   deploy contexts stay maximally simple — containers log to stdout/stderr
   with stable names, nothing else required per deploy. The only box-level
   prerequisite (one-time, documented in §7.5 guidance): logging driver
   `local` (or default `json-file`) with rotation caps in
   `/etc/docker/daemon.json` (`max-size`/`max-file`) so chatty containers
   can't fill disks — note `docker logs` does not work with remote drivers
   (`awslogs`/`syslog`/`gelf`). Pulls use `docker logs --timestamps --since
   <cursor>` against the stable name; redeploys surface as restart-detection
   event templates (deploy boundary = signal). Consequence for M6:
   journald/file kinds are demoted to "third-party boxes we don't control
   only" — they are not part of our own deploy story.
8. **UI feature = "Monitoring", per workspace** (ratified 2026-07-08): one
   page owning what to monitor (hosts + sources), where it is, and the alert
   output channels — settable Google Chat webhook URLs etc. — plus template
   explorer, digest preview, and worker install/levers.
9. **Channels are config rows, dispatch is daemon code** (ratified
   2026-07-08): `monitoring_channels` with per-row `min_severity`;
   `monitoring.notify` is the ONLY send path. The worker holds no channel
   tools; secret refs resolve inside the daemon.
10. **Deterministic announcement envelope** (ratified 2026-07-08): every
    outbound message on every channel deterministically carries WORKSPACE
    NAME + the gateway hostname it runs on + the remote hostname having the
    issue: `[{workspace_name} · via {gateway_hostname}] {SEVERITY} ·
    {remote_host_name} ({ssh_host})`. Rendered in Go, never by the model.
11. **Deployment topology: the always-on LXC daemon owns Monitoring**
    (ratified 2026-07-08): the dedicated LXC mcplexer install runs the
    collector + log-watch worker 24/7 — monitoring is a property of the
    network, not of a personal laptop that sleeps. dev-laptop-a / dev-laptop-b
    interact over the existing p2p mesh: alerts propagate as mesh
    messages, filed tasks replicate via workspace links, and
    `grant_trigger_to_peer` lets laptops fire the LXC's workers. The
    envelope's `via {gateway_hostname}` therefore names the LXC —
    exactly the origin attribution wanted. Local (dev-laptop-a) deploy is the
    dev/test rig first; LXC rollout is its own milestone.

## 10. Risks

- **Template explosion** on genuinely high-cardinality logs (e.g. request
  logs with unmasked atoms) → mitigation: masking-rule iteration + per-source
  template cap with overflow bucket template + M6 embeddings.
- **SSH flakiness** → cursored pulls make every pull idempotent-ish; health
  counter + `source went dark` rule turns silence into signal.
- **Notification fatigue** → three throttle layers + `logs.ack` to retire
  known-noisy templates; the lever default for `warn` is task-only.
- **Scope creep toward remediation** — this epic ends at *notify + file
  task with drill-down pointers*. Auto-fix workers are a follow-up epic.
