# ADR 0004: Skill Capability Enforcement

- Status: Accepted (spike)
- Date: 2026-04-30
- Branch: `spike/capability-enforcement`
- Related: M2.3 (skill execution), `project_skill_sharing.md`, ADR for Code Mode

## Decision

**Enforce skill capabilities with a declarative allowlist check inside the gateway dispatch path, gated on a `skill_id` carried via `context.Context`.** A skill's manifest declares the namespaces it needs (e.g. `["browser", "freeagent"]`); when a tool call is dispatched with an active skill context, the gateway compares the call's namespace against the manifest and blocks mismatches with a standard "blocked" audit record. No new sandbox layer. No subprocess isolation. We rely on the existing Goja sandbox (Code Mode) only for compute/timeout containment, not as the primary capability boundary.

Scope: applies to the first JS-skill format. Non-JS skills (M3+) defer to a future ADR; the threat model below explicitly excludes them.

## Threat model (one-page)

**Defended against**

1. *Honest-but-broad skill author* — skill calls a namespace it forgot to declare. Caught: declarative allowlist throws on dispatch, surfaces as a clear audit-logged error to the user during install/dev.
2. *Drift between manifest and code* — manifest says `["freeagent"]`, code calls `customer.list()`. Caught: namespace mismatch blocked at gateway, audit record links call to skill.
3. *Casual marketplace skill that escalates over time* — author publishes v1 needing `["browser"]`, later changes code to call `linear.*` without bumping manifest. Caught: every call is rechecked against the manifest at dispatch time; manifest is the single source of truth, signed and version-pinned (out of scope for this ADR but assumed).
4. *Tool-call exfil via the regular MCP path* — a skill cannot bypass the allowlist by calling `tools/call` directly: the gateway already blocks all non-builtin direct tool calls (`internal/gateway/handler_tools.go:206`), forcing every external dispatch through `mcpx__execute_code`, which is where we hang the skill context.

**Not defended against (explicit out-of-scope)**

1. *Malicious JS executing arbitrary in-process Go* — Goja runs in-process; a Goja escape (CVE in `dop251/goja` or our binding code) yields full daemon privileges. We accept this risk for v1. JS-language attacks (prototype pollution, regex DoS) are bounded by the existing 30s timeout and the VM interrupt watchdog (`internal/codemode/sandbox.go:120`).
2. *Network egress / filesystem from skill code* — Goja has no `fetch`, `fs`, or `require` bindings unless we add them. Capabilities are purely "which MCP namespaces" — anything else a skill wants must be added as an explicit binding and pass through the same allowlist.
3. *Side-channel data leaks across skills in the same session* — two skills running in the same Claude session share session state (workspace context, mesh inbox). We do not isolate per-skill state in v1.
4. *Compromised registry* — manifest signing/registry trust is a separate ADR. This ADR assumes the manifest a user installs is the manifest the skill was published with.
5. *Resource argument scoping* — a skill declared for `freeagent` can call any FreeAgent tool with any argument. Per-resource scoping (e.g. project IDs) is left to the existing `ScopePolicy` mechanism (`internal/gateway/scope_policy.go`) configured at the route level.

**Threat-model summary (one sentence):** the attacker we care about is a sloppy or slowly-drifting skill author who calls a namespace they didn't declare; we are not trying to contain a Goja-escape exploit or a malicious skill that already has the user's trust to install code.

## Code Mode primer (relevant prior art)

Today, every external tool call from an LLM is forced through `mcpx__execute_code` (see the block at `internal/gateway/handler_tools.go:204-212`). The Goja sandbox in `internal/codemode/sandbox.go` exposes namespaces as Goja globals: for each tool `ns__name`, it sets `vm.Set(ns, {name: fn})` where `fn` is a Go closure that calls `s.caller.CallTool(ctx, "ns__name", args)`. The caller is `handlerToolCaller` (`internal/gateway/handler_codemode.go:41-64`), which re-enters `handleToolsCall` with a context tagged via `withInternalCodeModeCall` and `withExecutionID`. The dispatch path then runs routing → scope policy → approval → cache → downstream → audit. **This is the seam we extend.** Adding a `withSkillID(ctx, skillID)` and a single allowlist check at the top of `handleToolsCall` requires no changes to the sandbox, the routing engine, or any downstream code.

## Implementation outline

Files touched (all in this branch's M2.3 implementation, not in this spike):

- `internal/gateway/handler_codemode.go` — accept a `skillID string` and `allowedNamespaces []string` on the code-execution entrypoint; add `withSkillID(ctx, id)` and `withSkillAllowlist(ctx, allowed)` mirroring the existing `withInternalCodeModeCall` pattern (`handler_codemode.go:21-37`).
- `internal/gateway/handler_tools.go` — after the `isInternalCodeModeCall` block at line 206, add a `if allowlist := skillAllowlistFromContext(ctx); allowlist != nil { ... }` check. Use the existing `splitNamespace` helper (`handler_codemode.go:272`) on `req.Name`. On mismatch: build an `RPCError` with `CodeInvalidParams`, call `recordAuditBlocked` (already exists, line 97 of `handler_audit.go`), and return. Built-in `mcpx__*` and `mesh__*` namespaces are always permitted (skills need search/execute themselves).
- `internal/store/sqlite/...` and `internal/store/types.go` — add `SkillID string` to `AuditRecord`. The bus (`internal/audit/bus.go`) and SSE feed pick it up for free.
- `internal/api/...` — a new endpoint that runs a skill: loads the manifest, validates the allowlist syntactically, calls into `handleCodeExecute` with the skill context attached.
- Skill manifest schema lives next to `internal/addon/types.go` (the addon YAML loader is a useful precedent — same shape: parent, scopes, tools).

**Data flow for a skill-originated tool call**

1. UI/CLI invokes `POST /api/skills/{id}/run` with arguments.
2. Handler loads manifest, derives `allowedNamespaces`, builds `ctx = withSkillID(withSkillAllowlist(reqCtx, allow), id)`, then calls `handleCodeExecute(ctx, manifest.Code)`.
3. `handleCodeExecute` builds the Goja sandbox with the **full** tool set (we do not pre-filter the bindings — the allowlist enforces, not the binding step; this keeps error messages truthful).
4. Skill JS calls e.g. `linear.list_issues(...)`. Goja invokes the Go closure, which calls `caller.CallTool(ctx, "linear__list_issues", args)`.
5. Re-enters `handleToolsCall`. The new check fires: `splitNamespace("linear__list_issues") → "linear"`, `"linear" ∉ ["freeagent"]` → blocked, audit recorded with `skill_id`, RPC error returned. Error bubbles back into the JS as a normal tool failure (`Tool call failed: …`), which the skill author sees during dev/install.

## Alternatives considered

1. **Filter Goja bindings at sandbox creation** (only `Set` allowed namespaces). Rejected: the check then lives in the sandbox, but the audit/blocked machinery lives in the dispatcher. Two enforcement sites is worse than one. Also gives the skill a `ReferenceError` instead of a clear audit-logged "blocked: capability not declared" message. Keep the binding step capability-blind; let the dispatcher decide.
2. **Goja sandbox as the primary boundary** (strategy 2 from the spike brief). Rejected as unnecessary: the boundary already exists at dispatch (every external call must traverse `handleToolsCall`). Adding a second check inside Goja is duplication. The sandbox earns its keep on compute/timeout/output-size, not capabilities.
3. **OS-level subprocess + `sandbox-exec`/seccomp** (strategy 3). Rejected for v1: heavy lift, doesn't help against the threats we actually care about (manifest drift), and we have no non-JS skills yet. Cited here because it's the upgrade path if the threat model changes (see below).
4. **Per-resource scoping baked into the skill allowlist** (e.g. `freeagent: { project_ids: [...] }`). Rejected: `ScopePolicy` already does this at the route level (`internal/gateway/scope_policy.go`). Skill manifests stay namespace-coarse; argument-level scoping is the route's job.

## Future-proofing — upgrade paths if the threat model evolves

| Threat we start caring about | Next step |
| --- | --- |
| Malicious JS exploiting Goja in-process | Move sandbox into a subprocess, call over JSON-RPC. The `ToolCaller` interface (`internal/codemode/sandbox.go:17`) is already process-agnostic — only `handlerToolCaller` and the sandbox creation site change. |
| Non-JS skills (Python, WASM) | Strategy 3 (OS sandbox / WASI) becomes mandatory. The allowlist check stays where it is — the boundary is still `handleToolsCall`, regardless of what wrote the call. |
| Per-tool not per-namespace granularity | Manifest gains `tools: ["linear__list_issues", ...]`. Check in `handler_tools.go` switches from namespace match to full-name match. One-liner. |
| Argument-level constraints | Reuse `ScopePolicy` per-skill: extend the manifest with route-style `scope_policy` blocks, evaluate them in the same place we evaluate route policy today (`handler_tools.go:256-277`). |
| Cross-skill state isolation | Per-skill session keys; gate `mesh__*` and any session-stateful builtins on a skill-instance ID. Out of scope for this ADR. |

The bias is: keep one enforcement site (`handleToolsCall`), keep the manifest declarative, and let every future need express itself as a richer manifest rather than a richer runtime.
