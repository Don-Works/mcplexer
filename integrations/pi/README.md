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

The optional router turns Pi into a thin, stateful input brain: a configured
local model classifies each substantive prompt, trusted policy code ranks the
currently registered MCPlexer worker models, and MCPlexer executes the work.
It is **disabled by default**.

### Enable

```bash
# CLI flag (on startup)
pi --mcpx-router

# Toggle at runtime
/router on
/router off
/router status
/router cancel
/router score 90
```

### How it works

```
input → local classifier → validated RouteDecision → live model catalog
      → evidence ranker → capability compiler → MCPlexer delegate → Pi result
```

1. **Classifier** — Pi calls a separate configured local model through
   `@earendil-works/pi-ai`. It returns task kind, quality, worker mode, safe
   intent labels, risk, and model requirements. Provider names, model IDs,
   namespaces, and tool globs are rejected by the schema.

2. **Live catalog** — `mcpx__list_delegation_model_capacity` supplies the
   registered profile/model pairs plus reviewed quality, reliability, average
   duration, cost/accounting status, active load, and availability. Optional
   operator overlays add capabilities, task priors, measured speed, and
   task-specific benchmark evidence.

3. **Ranker** — hard capability/modalities gates run first. The eligible pool is
   scored with task-specific benchmark/prior evidence, MCPlexer parent reviews,
   operational reliability, speed, cost, active load, and an operator priority.
   High-quality tasks emphasize task/review quality; low-quality tasks emphasize
   speed and operator-supplied normalized cost. Missing evidence is neutral (50).
   MCPlexer's cumulative live `cost_usd` is shown for audit but is not treated as
   a per-run score, so well-established models are not penalized for more history.
   Missing accounting stays neutral; it is never treated as free.

4. **Capabilities** — safe intent labels compile into real MCPlexer
   `capability_preset`, `capability_profile`, and gateway tool allowlist fields.
   Classifier output never becomes a permission string. Secrets, admin,
   deployment, destructive operations, and external writes are passed back to
   normal Pi for direct human-controlled handling rather than auto-delegated.

5. **Dispatch and learning** — the chosen explicit provider/profile/model is
   sent to `mcpx__delegate_worker` with `review_required:true`. Pi polls the
   delegation tree and displays the worker output plus the score breakdown and
   delegation ID. `/router score <0-100>` records user feedback so task-specific
   MCPlexer rankings improve over time.

### Bypasses

The router bypasses:
- Extension-origin input (prevents recursion)
- Slash commands (handled by Pi natively)
- Input with images (not supported initially)
- Empty/whitespace input
- Streaming steering/follow-up input

Only one routed request runs at a time. Additional plain input gets a busy
message instead of silently starting duplicate work. `/router cancel` stops
Pi waiting locally; an already-created MCPlexer delegation continues and stays
visible in the Delegations UI.

Classifier, catalog, policy, and pre-dispatch failures fail open to normal Pi.
Once a delegation ID exists, Pi does not run the same prompt again: timeout or
polling errors are surfaced with that ID to avoid duplicate side effects.

### Configuration

The classifier defaults to `local/qwen3.6-35b-a3b` on this installation. Set a
different persistent, fast local model with:

```bash
export MCPLEXER_ROUTER_CLASSIFIER_MODEL=local/your-fast-router-model
export MCPLEXER_ROUTER_CLASSIFY_TIMEOUT_MS=8000
```

Model evidence overlays are optional JSON. Entries match the live catalog by
`id` (`provider/model-id`) or by `provider` + `model_id`:

```bash
export MCPLEXER_ROUTER_CANDIDATES='[
  {
    "id": "mimo_cli/xiaomi/mimo-v2.5-pro",
    "benchmark_scores": {"coding": 84, "review": 79},
    "task_priors": {"architecture": 82},
    "speed_score": 88,
    "operator_priority": 96,
    "capabilities": ["code", "reasoning", "analysis", "tool_use", "workspace_tools"]
  }
]'
```

`benchmark_scores.coding` is where a normalized SWE-bench-style score belongs;
other tasks can use their appropriate evaluation. MCPlexer does not ship
fabricated benchmark values. With no overlay, task quality is neutral and the
reviewed live evidence plus a conservative workhorse preference drives routing.

Polling is configurable with `MCPLEXER_ROUTER_POLL_ATTEMPTS` (default 180)
and `MCPLEXER_ROUTER_POLL_MS` (default 2000).

### Safety

- External writes, secrets, deployments, destructive operations, and admin are
  never auto-granted.
- Delegate profiles disable mesh, secrets, task offers, and subdelegation.
- Gateway permissions use explicit MCP tool names/globs and namespace limits.
- MCPlexer independently hard-denies admin tools to delegated workers.
- The router is opt-in and off by default.

### Limitations

- Worker isolation and native CLI permissions remain MCPlexer concerns. The
  gateway capability profile constrains MCP tools; it cannot remove a CLI
  harness's native read/bash/edit/write tools or host networking.
- Coding follow-up context and branch integration are not automated yet.
- Image input is not supported initially.
- A cold local classifier can be much slower than a warm one; keep it resident
  or configure a smaller model.

## Testing

```bash
npm test
```

The Node 20-compatible suite imports the same dependency-free `core.mjs` used
by the extension. It covers schema validation, live capacity ingestion,
task/benchmark ranking, missing accounting, load/speed tradeoffs, capability
policy, delegation tree parsing, input bypasses, and the installed Pi API seam.

## Assumptions / known unknowns

The router event and completion paths are verified against installed Pi 0.80.3
(`pi.on("input")`, `@earendil-works/pi-ai/compat.complete`). Package
sub-directory resolution and the exact MCP `clientInfo.name` still depend on
the Pi package loader version; the gateway's harness-name tests cover the
known Pi/Earendil variants.
