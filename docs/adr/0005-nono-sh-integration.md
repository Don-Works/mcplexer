# ADR 0005 — nono.sh integration

- Status: Accepted (research spike outcome)
- Date: 2026-04-30
- Deciders: project maintainers
- Branch: `spike/nono-sh-eval`
- Related: ClickUp 86c9ka8wj; ADR 0002 (skill signing); ADR 0004 (skill capability enforcement); `project_code_mode.md`
- Supersedes: —

## Decision

**Decline integration into mcplexer core. Treat nono.sh as a complementary,
*optional* outer-shell that users can wrap mcplexer with. Track three small,
borrowable ideas as separate follow-up tickets and revisit a deeper coupling
only if/when (a) mcplexer starts spawning untrusted skill code in subprocesses
or (b) we move beyond stdio and need on-host network egress controls.**

We will not bundle, vendor, or hard-depend on nono. We will not ship it as an
mcplexer skill or as an MCP server, because nono is not an MCP server — it is
an OS-level sandbox launcher that *wraps* a process. The two tools sit on
different sides of the AI-client boundary and are best left orthogonal.

Concretely:

1. Document a recommended invocation pattern in the README: `nono run -- mcplexer connect ...` for users who want kernel-level filesystem/network containment around the AI agent that talks to mcplexer.
2. File three follow-up tickets to borrow ideas (see Open Questions) without taking the dependency.
3. No code changes in this branch. No vendoring. No new modules.

## Context — what nono.sh actually is (one-page)

**Identified as:** [github.com/always-further/nono](https://github.com/always-further/nono) (homepage <https://nono.sh>, docs <https://docs.nono.sh>). Apache-2.0. Early alpha. Authored by the team behind Sigstore. Go bindings live at [always-further/nono-go](https://github.com/always-further/nono-go); also Rust core, Python (`nono-py`), TypeScript (`nono-ts`).

**One-line:** a capability-based, kernel-enforced sandbox CLI + library for running AI agents on a developer machine, plus a multiplexer to run several agents in parallel with separate sandboxes.

**Primitives**

| Primitive | What it does |
| --- | --- |
| `CapabilitySet` | Declarative `allow_read(path)` / `allow_write(path)` / `set_network_mode(...)` builder. |
| `Sandbox::apply(caps)` | Irreversible kernel-level enforcement: Landlock on Linux, Seatbelt / `sandbox_init` on macOS, WSL2 ~84% coverage. Inherited by all child processes. |
| Credential injection proxy | Keeps API keys outside the sandbox; supports OS keystore, 1Password, Apple Passwords. Sandboxed process talks to a local proxy that injects on the way out. |
| Network filter | Allowlist host/endpoint filtering via local proxy. Cloud metadata endpoints (169.254.169.254 etc.) hard-denied. |
| Snapshots | Content-addressable, SHA-256-deduped, Merkle-tree-committed filesystem snapshots for atomic rollback. |
| Sigstore attestation | Sign and verify "instruction files" (`SKILLS.md`, `CLAUDE.md`, etc.) so an agent can refuse to act on tampered prompts. |
| Audit log | Append-only event audit, optional integrity hashing, optional rollback-backed FS evidence. |
| Profiles | Pre-built per-agent: Claude Code, Codex, OpenCode, OpenClaw, Swival. Custom profiles supported. |
| Multiplexing | Parallel agents in separate sandboxes; attach/detach to long-running agents. |

**Threat model.** "Agents shouldn't inherit full user trust by default." Defends against: agent reading files outside its allowed paths, exfil over network, credential leakage into agent process memory, tampered instruction files (via Sigstore), unaudited / irreversible damage (via snapshots). Does **not** defend against: malicious upstream model output that stays within the allowlist; bugs in Landlock/Seatbelt themselves; anything Windows-native (planned, not shipped).

**Target users.** Developers running AI coding agents locally; CI pipelines wanting a uniform sandbox layer; teams needing audit/compliance evidence for agent-driven changes. Distribution is `brew install nono`, `.deb`, `nix shell nixpkgs#nono`, or source.

**UX.** A CLI (`nono ...`) that wraps an agent process and applies the sandbox before exec'ing the child. Library mode embeds the same capability model into a Go/Rust/TS/Py program so the *program itself* opts into the sandbox.

## Comparison — overlap vs complementarity with mcplexer

| Concern | mcplexer | nono.sh | Verdict |
| --- | --- | --- | --- |
| **Per-tool allow/deny** | Workspace + subpath + tool-pattern routing, deny-first, priority-ordered (`internal/routing/`) | n/a — nono cannot see MCP tool calls | **Complementary.** mcplexer governs *which MCP tool a call hits*; nono governs *what the agent process can touch on the host*. |
| **Resource scoping** | `ScopePolicy` (`internal/gateway/scope_policy.go`): GitHub org/repo, Slack channel, etc. extracted from tool args | Path-level FS allowlist, host-level network allowlist | **Complementary.** Different layers — ours is semantic (which repo), nono's is syntactic (which path/host). |
| **Credential isolation** | Encrypted at rest with age, injected into downstream MCP server processes only, redacted from logs (`internal/secrets/`, `internal/auth/`) | Proxy keeps creds outside the sandbox; agent never sees raw keys | **Overlap.** Both keep credentials away from the AI. mcplexer protects creds from the *AI client*; nono protects creds from the *whole agent process*. Together: defence in depth. |
| **Audit log** | Per-tool-call SQLite audit with redaction, SSE stream, REST query (`internal/audit/`) | Per-event append-only log, optional integrity hashing, optional FS evidence via snapshots | **Overlap, different granularity.** mcplexer = MCP-call grain. nono = syscall/FS-event grain. |
| **Rollback** | None. Tool calls are best-effort idempotent. | Content-addressed snapshots, Merkle-committed | **Complementary.** Genuine gap in mcplexer. (See Open Questions.) |
| **Instruction-file integrity** | None. We trust whatever `CLAUDE.md` is on disk. | Sigstore-verified `SKILLS.md` / `CLAUDE.md` | **Complementary.** Adjacent to ADR 0002 (skill signing) but for a different artifact class. |
| **Approval workflow** | Human-in-the-loop with self-approval prevention, justification, SSE dashboard (`internal/api/approvals*`) | Runtime supervisor with dynamic permission expansion | **Overlap.** Both support "agent asks human to widen the box at runtime." mcplexer's is per-tool-call; nono's is per-capability-broadening. |
| **Multiplexing** | Multiple AI clients → one mcplexer → many MCP servers (`internal/downstream/`) | Multiple agents → many sandboxes, attach/detach | **Complementary.** mcplexer multiplexes *tools*, nono multiplexes *agents*. Composable. |
| **Tamper-proof CWD** | `os.Getwd()` in stdio mode is unspoofable; foundational to directory-scoped routing | n/a | mcplexer-specific. |
| **Code Mode / capability enforcement for skills** | Goja sandbox + namespace allowlist on dispatch (ADR 0004) | Kernel sandbox could host Goja's parent process | **Complementary upgrade path.** Today our threat model excepts a Goja in-process escape (ADR 0004 §"Not defended against" #1). nono around the mcplexer daemon would shrink that hole — but only if mcplexer itself is the wrapped process, which is a deployment choice, not a code change. |

**Top three overlaps**, in priority order:

1. **Credential isolation** — mcplexer keeps creds away from the AI client; nono keeps them away from the agent process. Same goal, different blast radius.
2. **Audit logging** — both write append-only event records; nono adds integrity hashing and FS evidence; mcplexer adds MCP-protocol semantics.
3. **Approval / supervisor workflow** — both have a "pause and ask the human" loop, mcplexer per tool call, nono per capability broadening.

**Where nono is strictly complementary (no overlap):**

- Kernel-enforced FS / network containment of the agent process itself (mcplexer has none — we trust the AI client to not exfiltrate via its own subprocess shells).
- Filesystem rollback (mcplexer has none).
- Sigstore-attested instruction files (mcplexer has none).

## Alternatives considered

1. **(a) Bundle into mcplexer core.** *Rejected.* Bringing in `nono-go` adds CGo (the bindings explicitly require gcc/clang), kills our "pure Go, zero CGO, single binary" property (CLAUDE.md, `mcplexer-features.md` §16.4), and forces every mcplexer user onto Linux/macOS-only with kernel version constraints (Landlock needs Linux 5.13+). Hard breakage of a load-bearing distribution promise. Also: nono is early-alpha, "not yet security audited for production use" — bundling alpha into a security-positioned product is the wrong shape.

2. **(b) Ship as an mcplexer skill.** *Rejected.* Category error. A skill (per ADR 0004) is JS code that runs inside Goja and calls MCP tool namespaces. nono is an OS-level process sandbox that wraps mcplexer or its callers — it cannot meaningfully be expressed as JS-inside-Goja. There is nothing for a skill to *do* that the user couldn't do better by running `nono run -- claude-code` in their terminal.

3. **(c) Wire as an MCP server alongside mcplexer.** *Rejected.* nono is not an MCP server. It exposes no `tools/list` or `tools/call`. Forcing it into the MCP shape would mean writing an adapter that exposed `nono__snapshot_create`, `nono__rollback`, `nono__capability_grant` etc. as tools — useful but premature: we have no skill that needs these primitives today, and the natural caller (mcplexer the daemon) is itself the thing being sandboxed, so it can't sandbox itself via its own tool surface without a chicken-and-egg loop.

4. **(d) Decline integration; document an outer-wrap pattern; cherry-pick ideas.** *Selected.* Fits the user's "secure-by-default kitchen-sink harness, capped at mcplexer's own tooling" lens — we keep our boundary clean (MCP-protocol-shaped) and let nono own the OS boundary. The two compose at the user's terminal, not in our code.

## Consequences

**Positive**

- Pure-Go / zero-CGO distribution promise preserved. No new kernel-version constraints, no Windows regressions (our roadmap, not nono's).
- We don't carry the operational risk of an alpha dependency in a security-critical path.
- Users who want kernel containment can already get it today: `nono run -- mcplexer connect` with no work from us.
- Three concrete ideas (snapshots, instruction-file attestation, network proxy) become independent backlog items, evaluated on their own merits rather than as a package deal.

**Negative**

- We ship nothing visible in this spike. The user asked "can we include the smarts of nono.sh"; the honest answer for now is "we already cover the MCP-call layer; the OS layer is genuinely nono's job and they do it better than we would in a fork-week".
- Users have to discover the wrap pattern themselves (mitigated by adding a README section).
- If nono's ecosystem accelerates faster than mcplexer's, we may regret not standardising on its profile format (Claude Code / Codex / etc.) earlier. Acceptable risk: profile formats are cheap to mirror later.

**Neutral**

- ADR 0004's "future-proofing" table already names "subprocess sandbox" as the upgrade path when we start caring about Goja escapes. nono is now the named candidate for that upgrade — concretely, swap `handlerToolCaller` (`internal/codemode/sandbox.go:17`) for a process-isolated caller whose subprocess is launched under nono. No code change today; this ADR just records the option.

## Open questions (follow-up tickets)

These are the three "smarts" worth borrowing from nono, each as a standalone evaluation, not a precondition for shipping anything:

1. **Filesystem snapshot / rollback for tool-call side effects.** mcplexer has no rollback today. A content-addressed snapshot of paths touched by a tool call would let us add an "undo last tool call" feature. Could be implemented with our own primitives (we already use SQLite + age) or by depending on nono's snapshot subsystem. Spike when we have a concrete user request for it.

2. **Sigstore-attested skill manifests.** ADR 0002 picked minisign for `.mcskill` signing precisely because Sigstore added 15 transitive modules and 4.8MB of binary. nono shows it's possible to make Sigstore feel light at the agent layer. Worth re-evaluating when we have a skill registry (R0.3) and a public CA story; until then, minisign holds.

3. **Egress / network-allowlist proxy as an mcplexer feature.** We control credentials; we don't control where downstream MCP servers (or their child processes) phone home. A local egress proxy with per-server allowlists would close that gap and lives naturally next to `internal/auth/`. Independent of nono — we'd build this with `net/http` and our existing proxy machinery.

4. **Documentation: "Running mcplexer under nono."** README section showing `nono run --profile claude-code -- mcplexer connect` for users who want defence in depth today. Trivial doc PR; no code.

5. **Revisit if/when threat model changes.** ADR 0004's accepted risk is a Goja in-process escape. The day we ship a public skill registry where any author can publish JS that runs inside our daemon, that risk goes from "academic" to "real," and an OS-level sandbox around the daemon (or around skill subprocesses specifically) becomes the right answer. nono is the named candidate.
