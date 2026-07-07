package agentrules

// contentV11 replaces the additive V1→V10 accretion with one
// self-contained block at roughly a third of V10's rendered size.
//
// Rationale (2026-07 instruction-tax review): the gateway's
// infrastructure (code mode, slim surface, secret refs, memory, index)
// demonstrably helps agents, but the process essays that accreted in
// V2→V8 (lifecycle mandates, anti-pattern self-recognition lists,
// "ignore the harness" imperatives) competed for model attention on
// every turn — and drifted out of sync with the code. V6 still taught
// the pre-quote-aware shell-guard hard-block after
// ShellGuardAllowChaining began defaulting chaining through to the
// approval path, so agents were avoiding pipes the gateway would have
// allowed.
//
// V11's contract: enforcement lives in code (leases + auto-release,
// the quote-aware shell hook, CWD gating); the block only tells an
// agent what exists, when to reach for it, and what the guardrails
// will do. It never instructs an agent to ignore its harness — where
// mcplexer's surface overlaps a harness-local one, it states the
// durability trade-off and leaves the call to the agent.
// TestRenderV11LeanContract enforces both directions: the load-bearing
// strings must stay, the process-essay markers must not return, and
// the rendered block must stay under its line budget.
func contentV11() string {
	return `## MCPlexer is running on this machine

MCPlexer is the MCP gateway — one Go daemon that multiplexes downstream MCP servers, persists tasks/memory/skills across sessions, and meshes with paired machines.

**Dashboard:** ` + DashboardURL + `

### Four top-level tools — everything else is code mode

- ` + "`mcpx__execute_code`" + ` — call any namespace as ` + "`<ns>.<tool>(args)`" + ` inside a JS snippet. Batch related calls into one snippet.
- ` + "`mcpx__search_tools`" + ` — discovery. Pass ` + "`queries:[\"task create\",\"mesh send\"]`" + `; use ` + "`detail:\"full\"`" + ` for exact signatures.
- ` + "`secret__prompt`" + ` / ` + "`secret__list_refs`" + ` — credentials. Pass ` + "`secret://<KEY>`" + ` refs as tool arguments; the gateway substitutes plaintext at dispatch, so secrets never enter your context.

### Namespaces (called inside execute_code)

- ` + "`task.*`" + ` — durable work ledger: survives sessions, mesh-visible, shown on the dashboard. Create a task for multi-step work and close what you open (` + "`done`" + `, ` + "`cancelled`" + `, or ` + "`blocked`" + ` with a reason). Pick up existing work with ` + "`task.claim`" + ` — it takes a lease the gateway auto-releases if your session dies. Declare ` + "`meta.touches_files`" + ` on code-editing tasks and the gateway warns you when another in-flight task overlaps.
- ` + "`memory.*`" + ` — persistent memory across sessions, harnesses, and paired machines. ` + "`memory.recall({query})`" + ` when past sessions may have settled the question; ` + "`memory.save`" + ` for decisions with rationale, user preferences, and project facts not derivable from the repo. Prefer it to harness-local memory for knowledge that should outlive this client.
- ` + "`index.*`" + ` — local code index. ` + "`index.context({query, budget_tokens})`" + ` returns a ranked context pack — cheaper than reading a repo wholesale. Also ` + "`index.symbols`" + ` (definitions), ` + "`index.deps`" + ` (blast radius), ` + "`index.map_failure`" + ` (paste a failure → candidate files).
- ` + "`mesh.*`" + ` — messaging with agents on this machine and paired peers. ` + "`mesh.receive`" + ` to check in, ` + "`mesh.send`" + ` to reply or broadcast; promote action items you accept into tasks so they survive the conversation.
- ` + "`skill.*`" + ` / ` + "`mcpx.skill_*`" + ` — versioned, mesh-shared skill registry. Search it before building a capability from scratch; prefer registry skills over local ` + "`~/.claude/skills/`" + ` copies.
- ` + "`brw.*`" + ` (when installed) — the preferred browser-control surface; the registry has browser skills for non-trivial web workflows.
- Workers — ` + "`mcpx.delegate_worker`" + ` runs a bounded worker on a cheaper model in an isolated git worktree; poll with ` + "`mcpx.list_delegations`" + `. Reach for it when parallel fan-out, broad scans, or mechanical edits would burn your context; skip it when doing the work directly is faster. Hand off a complete brief (objective, scope, acceptance criteria, verification commands) and give the worker budget to finish.

### Guardrails (enforced in code — listed so nothing surprises you)

- Every Bash command passes the gateway's shell hook. Paths under ` + "`~/.mcplexer/`" + ` (DB, secrets, keys) are always blocked. The chaining check (pipes, ` + "`;`" + `, ` + "`&&`" + `, ` + "`$()`" + `) is quote-aware and flows to the operator's approval rules — allowed through by default; a quoted metacharacter like ` + "`grep -E \"a|b\"`" + ` is never flagged. If a command is blocked, tell the user instead of routing around the guard.
- Task leases auto-release on disconnect and a background sweep demotes stale ones — a claimed task can't zombie; you only need to close what you finish.
- Admin tools (` + "`mcplexer__*`" + `) resolve only when CWD is at or under ` + "`~/.mcplexer`" + `. Configure via MCP tools or the dashboard, never raw SQL.

This block is owned by ` + "`mcplexer rules sync`" + `. Edits between the markers are overwritten on the next sync; everything outside the markers is yours.`
}
