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

## Assumptions / known unknowns

See the parent task's follow-ups. In short: the exact Pi package sub-directory
resolution, the precise `clientInfo.name` Pi sends over MCP, and the stability
of the `ExtensionAPI` / `defineTool` signatures across Pi releases were taken
from Pi's public docs (June 2026) and may need a one-line adjustment against
your installed Pi version.
