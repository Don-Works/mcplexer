# MCPlexer

MCP Gateway (Multiplexer) — single Go binary with an installable web UI (PWA) for managing MCP tool servers.

## Stack
- **Core**: Go, SQLite (modernc.org/sqlite, no CGO), net/http
- **UI**: React, TypeScript, Vite, shadcn/ui, Tailwind CSS — installable as a PWA
- **Encryption**: filippo.io/age for secrets at rest

## Project Layout
- `cmd/mcplexer/` — Go entry point, CLI subcommands (`serve`, `setup`, `daemon`, etc.)
- `internal/gateway/` — MCP server (stdio), tool aggregation, dispatch
- `internal/api/` — REST API handlers
- `internal/store/` — Store interface + domain models (DB-agnostic)
- `internal/store/sqlite/` — SQLite implementation
- `internal/routing/` — route matching engine
- `internal/downstream/` — process lifecycle manager for downstream MCP servers
- `internal/auth/` — credential injection
- `internal/secrets/` — age encryption + secret storage
- `internal/audit/` — audit logging with redaction
- `internal/config/` — YAML config loader, validation
- `internal/web/` — go:embed for SPA static files
- `web/` — React SPA source

## Conventions
- Go: idiomatic, explicit error handling, table-driven tests
- Max 300 lines per file, max 50 lines per function
- TypeScript: strict mode, functional components, no `any`
- Tool namespacing: always `{namespace}__{toolname}`
- DB interface: all methods take context.Context, use sentinel errors (store.ErrNotFound)
- No ORM — raw database/sql with hand-written queries

## Configuring MCPlexer
- **Configure via MCP, never via raw SQL.** Use the admin surface (`mcplexer__list/get/create/update/delete_{workspace,server,route,auth_scope}`, `mcplexer__status`, `mcplexer__query_audit`, `mcpx__provision_mcp` etc.) from inside an agent.
- **Admin tools are CWD-gated.** Visible only when CWD is at or under `~/.mcplexer`. From project directories the agent sees only `mcpx__search_tools`, `mcpx__execute_code`, `secret__prompt`, and `secret__list_refs`; everything else is discovered via search and called inside `execute_code`.
- **No raw-SQL fallback.** If you reach for `sqlite__*` tools or `~/.mcplexer/mcplexer.db` directly, stop. Supported paths: MCP tools, YAML config (`~/.mcplexer/mcplexer.yaml`), or the dashboard.

## MCP harness compatibility
mcplexer detects the connecting client and adapts the tool surface (`internal/gateway/client_harness.go`).

- **Direct harnesses** (Claude Code, Codex, OpenCode, Pi native extension): advertise canonical names (`mcpx__execute_code`, `mcpx__search_tools`, `secret__prompt`, `secret__list_refs`). Call directly.
- **Server-prefixed harnesses** (Grok CLI, Cursor, Windsurf, Gemini CLI, Picoclaw): advertise single-segment aliases (`execute_code`, `search_tools`) so the qualified name has only one `__`. `tools/call` accepts both alias and canonical forms.
- **All namespaces** (memory, task, mesh, skill) are discovered via the search tool and invoked inside `execute_code` — never top-level. Inside JS snippets: `memory.save({...})`, `task.create({...})`, `mesh.send({...})`.
- For full harness setup, install wiring, and worker preamble details, see the **`using-mcplexer`** skill (`mcpx__skill_search`) and the **Setup page** (`/harness-setup`).

## Workers — CLI providers are opt-in
CLI workers (`claude_cli`, `opencode_cli`, `grok_cli`, `mimo_cli`) run with **NetworkHost** and are gated behind env opt-ins:
- `MCPLEXER_ALLOW_CLAUDE_CLI=1`, `MCPLEXER_ALLOW_OPENCODE_CLI=1`, `MCPLEXER_ALLOW_GROK_CLI=1`, `MCPLEXER_ALLOW_MIMO_CLI=1`
- `MCPLEXER_ALLOW_LMSTUDIO=1` — gates `lmstudio__*` tools (network access via `lms` CLI).
- Sandbox **denies writes** to `~/.claude/` and `~/.mcplexer/`; reads open for OAuth/creds.
- `grok_cli` headless JSON may omit usage/cost — treat `0` tokens as missing accounting, not zero spend.
- Enable via launchd plist `EnvironmentVariables` (macOS) or systemd unit `Environment=` (Linux).

## Token-Preserving Delegation (delegation-first)
**Delegation is the default execution path, not an optional optimisation.** Frontier model sessions (Opus/Fable/GPT-5.5 class) are planners, reviewers, and integrators. They must NOT burn context on token-heavy work when a bounded Worker can do it.
- **Delegate when:** multi-file exploration, repeated file reads, long command output, implementation after the architecture is clear, test/log triage, mechanical edits, or parallel investigation. If the architecture and acceptance criteria are known, the work belongs in a worker.
- **Keep in the parent:** problem framing, architecture decisions, worker decomposition, result review/scoring, integration, secret handling, and final user communication.
- **Tools:** `mcpx__delegate_worker` (create), `mcpx__list_delegations` (poll), `mcpx__review_delegation` (score). `mcplexer__spawn_subagent` is an admin escape hatch only.
- **Lifecycle:** decompose → delegate with `review_required: true` → poll → review diffs/tests/git state → score (>=80 accepted, 50–79 partial, <50 rejected) → close. Score every delegation in the session that spawned it; sweep `needs_review` rows before session end.
- **Handoff packet:** objective, scope, known facts with file refs, constraints, acceptance criteria, verification commands, return contract. Under ~4 000 tokens; put heavier context in a `task__create` work context and pass the task ID.
- **Workers:** prefer mcplexer Workers on cheap code-cutter profiles over native Claude/Codex subagents (better cross-client pickup, audit, budgets, provider routing). For OpenCode-backed workers (MiniMax, GLM, OpenRouter), prefer a local OpenCode server endpoint so parallel workers attach through one server.
- **Worker isolation:** workers must use isolated git worktrees, never the parent checkout. Workers must not touch `~/.mcplexer/` (DB, logs, secrets, p2p, backups) — all config/state operations go through MCP tools, never raw SQL or direct file access.
- **Metrics:** the Delegations UI (`/delegations`) shows parent/worker context trees, spend, frontier tokens/cost avoided, worker token delta, review scores, and model rank. Compare avoided frontier cost against worker spend; raw MiniMax/GLM token count is not the win condition.
- See `skills/token-preserving-delegation.md` for the full workflow, calling conventions, exploration pattern, savings rubric, and handoff template.

## DB lockdown — `~/.mcplexer/` is off-limits
`~/.mcplexer/{mcplexer.db,mcplexer.db.age,api-key,secrets/,p2p/,backups/,mcplexer.log*}` is OFF-LIMITS. Enforcement:

1. **Harness denylist** — instant block on Read/Edit/Write of protected paths.
2. **PreToolUse hook** — pattern-matches tool inputs for protected fragments. Dev-mode escape: if `CLAUDE_PROJECT_DIR` is this repo (or a worktree), the block lifts for gateway development.
3. **Gateway-side `cmdguard.go`** — rejects downstream MCP server `command`/`args` referencing protected paths.
4. **File modes** — `0600`/`0700` applied by `scripts/harden-data-dir.sh`, wired into `make install` + `make upgrade`.

## Commands

All commands use [Task](https://taskfile.dev) (`task <name>`). Run `task` with no args to list available tasks. `make` exists as a compatibility shim but is deprecated.

### Install / Upgrade
- `task install` — build daemon (with p2p), sync binary, run interactive setup (launchd + MCP-client config + skill install). Install the PWA from `http://localhost:3333`.
- `task upgrade` — in-place atomic swap + launchctl kickstart (~1-2s downtime).
- `task install-cli` — slim build (no p2p) + setup. For headless boxes.
- `task uninstall` — stop daemon, remove launchd plist + binary.

### Development
- `task run` — build + start daemon locally
- `task dev` — run Go server in HTTP mode (foreground, no daemon)
- `cd web && npm run dev` — web UI dev server (hot reload, proxies `/api` to `:3333`)

### Build / Test / Verify
- `task build` — slim Go binary + web UI (~30 MB)
- `task build-p2p` — p2p-enabled Go binary + web UI (~54 MB), output `bin/mcplexer-p2p`
- `task test` — run Go tests (incl. p2p tag)
- `task lint` — run `go vet` + `golangci-lint` (with `.golangci.yml` config). **Run this before every commit to verify changes.**

<!-- VERCEL BEST PRACTICES START -->
## Best practices for developing on Vercel

These defaults are optimized for AI coding agents (and humans) working on apps that deploy to Vercel.

- Treat Vercel Functions as stateless + ephemeral (no durable RAM/FS, no background daemons), use Blob or marketplace integrations for preserving state
- Edge Functions (standalone) are deprecated; prefer Vercel Functions
- Don't start new projects on Vercel KV/Postgres (both discontinued); use Marketplace Redis/Postgres instead
- Store secrets in Vercel Env Variables; not in git or `NEXT_PUBLIC_*`
- Provision Marketplace native integrations with `vercel integration add` (CI/agent-friendly)
- Sync env + project settings with `vercel env pull` / `vercel pull` when you need local/offline parity
- Use `waitUntil` for post-response work; avoid the deprecated Function `context` parameter
- Set Function regions near your primary data source; avoid cross-region DB/service roundtrips
- Tune Fluid Compute knobs (e.g., `maxDuration`, memory/CPU) for long I/O-heavy calls (LLMs, APIs)
- Use Runtime Cache for fast **regional** caching + tag invalidation (don't treat it as global KV)
- Use Cron Jobs for schedules; cron runs in UTC and triggers your production URL via HTTP GET
- Use Vercel Blob for uploads/media; Use Edge Config for small, globally-read config
- If Enable Deployment Protection is enabled, use a bypass secret to directly access them
- Add OpenTelemetry via `@vercel/otel` on Node; don't expect OTEL support on the Edge runtime
- Enable Web Analytics + Speed Insights early
- Use AI Gateway for model routing, set AI_GATEWAY_API_KEY, using a model string (e.g. 'anthropic/claude-sonnet-4.6'), Gateway is already default in AI SDK
  needed. Always curl https://ai-gateway.vercel.sh/v1/models first; never trust model IDs from memory
- For durable agent loops or untrusted code: use Workflow (pause/resume/state) + Sandbox; use Vercel MCP for secure infra access
<!-- VERCEL BEST PRACTICES END -->
