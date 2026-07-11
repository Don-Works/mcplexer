# mcplexer for Pi

A lightweight [Pi](https://pi.dev) (Earendil / `earendil-works/pi`) package
that bridges the agent to the local **mcplexer** gateway — without the MCP
token bloat Pi was designed to avoid.

## Why a native extension instead of plain MCP

Pi is deliberately MCP-skeptical. The critique is well-founded: a typical MCP
server dumps thousands of tokens of tool definitions into the context window at
startup, and you pay that cost whether or not you use the tools. Connect a few
servers and half your context is gone before the conversation begins.

mcplexer is the direct answer. Its gateway advertises a **slim 4-tool surface**:

| mcplexer tool        | what it does                                   |
| -------------------- | ---------------------------------------------- |
| `mcpx__search_tools` | discover callable functions on demand          |
| `mcpx__execute_code` | run a JS snippet that batches downstream calls |
| `secret__prompt`     | ask the human for a credential, get a ref back |
| `secret__list_refs`  | list available `secret://KEY` references       |

Everything else mcplexer offers (every downstream MCP server, plus the task /
mesh / memory / skill namespaces) is **discovered at runtime and called inside
a code snippet** — never preloaded. That is exactly Pi's "primitives over
features, CLI + README, not MCP bloat" ethos.

This package surfaces those four tools to Pi as four thin native tools, backed
by a tiny CLI shim. No tool-definition dump, no proxy server, ~600 tokens
total instead of 10k+ per MCP server.

## What's in the box

```
integrations/pi/
├── package.json              # Pi package manifest (the `pi` key)
├── extensions/mcplexer.ts    # registers mcpx_search / mcpx_exec / mcpx_secret_*
├── bin/mcpx-shim.mjs         # dependency-free MCP client: one tools/call over the socket
├── skills/mcplexer/SKILL.md  # on-demand playbook the agent reads when needed
└── README.md                 # this file
```

The extension registers four tools and one `/mcplexer` command. Each tool
shells out to `mcpx-shim`, which performs a single MCP `tools/call` against the
mcplexer daemon by spawning `mcplexer connect --socket=<path>` (the same
stdio↔socket bridge every other MCP client uses) and speaking newline-delimited
JSON-RPC 2.0 over it.

## Prerequisites

- **mcplexer running locally.** Dashboard at <http://localhost:3333>. Install
  per the main repo (`task install`). The daemon exposes a Unix socket at
  `/tmp/mcplexer.sock` (or `$XDG_RUNTIME_DIR/mcplexer.sock` on Linux).
- **`mcplexer` on `PATH`** (or set `MCPLEXER_BIN`).
- **Node ≥ 20** (Pi already requires a modern Node).

## Install

From npm or git, using Pi's package manager:

```bash
# git (this repo) — installs to ~/.pi/agent/git/
pi install git:github.com/don-works/mcplexer

# …or project-local (.pi/git/)
pi install -l git:github.com/don-works/mcplexer
```

> The `pi` manifest lives in `integrations/pi/package.json`. If your Pi version
> only auto-discovers a package from its repo root, point Pi at this
> subdirectory, or copy `integrations/pi/` into your own Pi package, or load it
> ad hoc (below). Adjust the install source to match how your Pi build resolves
> sub-package manifests — see Assumptions.

Restart Pi after installing.

### Ad-hoc / development load

Load the extension directly without installing the package:

```bash
pi -e /path/to/mcplexer/integrations/pi/extensions/mcplexer.ts
```

Drop `skills/mcplexer/SKILL.md` into any Pi skills directory (e.g.
`~/.pi/agent/skills/mcplexer/SKILL.md` or `.pi/skills/mcplexer/SKILL.md`) so
`/skill:mcplexer` resolves.

## Configuration (env)

| Variable                  | Default                            | Purpose                              |
| ------------------------- | ---------------------------------- | ------------------------------------ |
| `MCPLEXER_BIN`            | `mcplexer`                         | path to the mcplexer binary          |
| `MCPLEXER_SOCKET_PATH`    | `/tmp/mcplexer.sock` (Linux: XDG)  | daemon Unix socket                   |
| `MCPLEXER_CLIENT_CWD`     | process cwd                        | workspace root reported to gateway   |
| `MCPLEXER_SHIM_TIMEOUT_MS`| `60000`                            | per-call timeout in the shim         |

## Use it

The shim doubles as a plain CLI (matching Pi's CLI-first style):

```bash
# discover
node integrations/pi/bin/mcpx-shim.mjs mcpx__search_tools '{"queries":["task create"]}'

# execute a batched snippet
echo '{"code":"print(task.list({state:\"open\"}).length)"}' \
  | node integrations/pi/bin/mcpx-shim.mjs mcpx__execute_code
```

Inside Pi, the agent calls `mcpx_search` then `mcpx_exec`, and reads
`/skill:mcplexer` for the full playbook.

## How tool names map

Pi is a **HarnessDirect** client to mcplexer (`internal/gateway/client_harness.go`):
it calls tools by their advertised canonical names (`mcpx__execute_code`, …) and
never server-prefixes them into the `mcplexer__mcpx__execute_code` double-`__`
form that Grok/Cursor produce. The shim therefore sends canonical names
verbatim. The gateway maps `clientInfo.name` to the `pi` harness key when it
matches the bare `pi` token, a `pi-coding-agent` name (e.g.
`@mariozechner/pi-coding-agent`), a `pi.` prefix (`pi.dev`), or carries the
`earendil` org marker. Substring lookalikes are intentionally excluded — names
like `picoclaw`, `copilot`, `raspberry-pi`, and `pip` do NOT match.

## Router mode (opt-in)

The optional MCPlexer router intercepts user input, classifies the task, ranks
available model candidates, and delegates to the best model via MCPlexer's
worker system. It is **disabled by default** — normal Pi behavior is preserved.

### Enable

```bash
# CLI flag (on startup)
pi --mcpx-router

# Toggle at runtime
/router on
/router off
/router status
```

### How it works

```
User input → Classifier (LLM) → RouteDecision → Ranker → Best model → MCPlexer delegation → Result
```

1. **Classifier** — a local LLM call returns a strict `RouteDecision` with
   action, task kind, quality, worker mode, tool intents, risk, and requirements.
   It NEVER selects raw model IDs, providers, or tool globs.

2. **Ranker** — eligibility gates filter candidates by required capabilities,
   then scores each on: task-specific prior (40%), review boost (15%),
   reliability (20%), latency (15%), cost (10%). Missing evidence is neutral
   (50 for priors, 80 for reliability), never zero. Ties are broken
   deterministically by candidate id.

3. **Capabilities** — tool intents are compiled through trusted bundles to an
   explicit `capability_preset` + `capability_profile` + `tool_allowlist`.
   Classifier strings are NEVER passed through as permissions. Dangerous intents
   (secrets, admin, force-push) downgrade to read-only.

4. **Dispatch** — the chosen candidate is dispatched via `mcpx__delegate_worker`.
   The router polls for completion and displays the result with auditable route
   metadata (chosen model, score breakdown, delegation id).

### Bypasses

The router does NOT intercept:
- Extension-origin input (prevents recursion)
- Slash commands (handled by Pi natively)
- Input with images (not supported initially)
- Empty/whitespace input
- Input while a previous route is still processing (serialized)

On any classifier or dispatch failure, the router **fails open** to normal Pi
with a concise warning.

### Model candidate catalog

The default catalog includes Claude Sonnet 4, Claude Haiku 3.5, GPT-4o,
GPT-4o Mini, and o3-mini. Each candidate has:

- Task-specific quality priors (coding, research, review, chat) — operator-configurable
- Speed and cost tiers for eligibility gating
- Reliability score from observed delegation success
- Capability list for eligibility filtering

Override via environment variable:

```bash
MCPLEXER_ROUTER_CANDIDATES='[{"id":"custom/model","provider":"custom",...}]'
```

### Ranking formula

```
score = task_prior × 0.40 × quality_multiplier
      + review_boost × 0.15
      + reliability × 0.20
      + latency_score × 0.15
      + cost_score × 0.10
```

Quality multiplier: high=1.2, medium=1.0, low=0.8.

### Safety

- Dangerous tool intents (secrets, admin, force-push, delete-production) always
  downgrade to read-only capabilities.
- External writes/secrets/admin are NEVER auto-granted.
- Admin operations remain impossible through the router.
- The router is opt-in and off by default.

### Limitations

- Workers run in isolated git worktrees — follow-up context does not carry over.
- Image input is not supported initially.
- The classifier uses a local LLM call, adding ~1-2s latency to the first response.
- The candidate catalog uses neutral priors until real review evidence accumulates.
- MCP tool gates do not constrain CLI-native tools or direct network access.

## Testing

```bash
npm test
```

Runs the router test suite (classifier parsing, ranker scoring, capability
compilation, interception logic) with `node:test`. No network required.

## Assumptions / known unknowns

See the parent task's follow-ups. In short: the exact Pi package sub-directory
resolution, the precise `clientInfo.name` Pi sends over MCP, and the stability
of the `ExtensionAPI` / `defineTool` signatures across Pi releases were taken
from Pi's public docs (June 2026) and may need a one-line adjustment against
your installed Pi version.
