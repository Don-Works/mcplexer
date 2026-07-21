---
name: mcplexer
description: Reach any capability beyond Pi's core tools via the local mcplexer gateway — downstream MCP servers (github, linear, browser…), the task ledger, agent mesh, persistent memory, the skill registry, and secrets. Use when a task needs tools Pi doesn't have built in; discover on demand with mcpx_search then run code-mode snippets with mcpx_exec, no MCP context bloat.
---

# MCPlexer from Pi

Use this skill when you need any capability beyond Pi's four core tools —
downstream MCP servers (github, linear, browser, etc.), the task ledger, the
agent mesh, persistent memory, the skill registry, or secrets. They all live
behind the local **mcplexer** gateway, reachable through four thin Pi tools
this package registers. None of them dump tool definitions into your context:
you discover on demand, then call inside a code snippet.

## The four tools

1. **`mcpx_search({ queries, detail? })`** — discover callable functions.
   Pass terms like `["task create", "github issues"]`. Add `detail:"full"`
   to get TypeScript signatures before you write a snippet.
2. **`mcpx_exec({ code })`** — run JavaScript in mcplexer's Code Mode sandbox.
   Call any discovered function as `namespace.tool(args)`. Calls are
   synchronous (no `await`). Batch related calls in ONE snippet. `print(...)`
   for output. Results auto-unwrap — read `result.id`, not `JSON.parse(...)`.
3. **`mcpx_secret_refs()`** — list available `secret://KEY` references.
4. **`mcpx_secret_prompt({ key, prompt? })`** — ask the human for a credential
   the agent must never see; you get back a `secret://KEY` reference.

## Workflow

Search → execute. Always batch.

```
mcpx_search({ queries: ["task create", "task list"], detail: "full" })
```

then

```
mcpx_exec({ code: `
  const epic = task.create({ title: "Ship feature X", status: "doing",
    meta: { touches_files: ["internal/foo.go"] } });
  const open = task.list({ state: "open", status: "doing" });
  print(epic.id, open.length);
` })
```

## Secrets — never paste plaintext

Pass `secret://KEY` strings as arguments. The gateway substitutes the
plaintext at dispatch time; your context never sees it.

```
mcpx_exec({ code: `github.create_issue({ repo: "acme/app", title: "Bug",
  token: "secret://GITHUB_TOKEN" })` })
```

If a secret is missing, request it:

```
mcpx_secret_prompt({ key: "GITHUB_TOKEN", prompt: "Paste a GitHub PAT" })
```

## Conventions worth knowing

- **Tasks are the durable ledger.** Prefer `task.create/list/update` over any
  session-local todo — they survive session end and are mesh-visible. Declare
  `meta.touches_files` so the gateway can warn about collisions with other
  agents.
- **Memory:** `memory.save({...})` / `memory.recall({...})` for decisions,
  user preferences, and project facts not derivable from the repo. Recall
  before answering "how do I X in this project".
- **Mesh:** `mesh.receive(...)` to see peers + inbox, `mesh.send(...)` to
  coordinate. Set a name + role on first receive.
- **Skill registry:** `mcpx.skill_search(...)` / `mcpx.skill_get({name})`
  before building a capability from scratch; `mcpx.skill_publish(...)` after.
- **Compression markers:** Pi keeps four native tools; expand a `[[ccr key=...]]`
  marker inside `mcpx_exec` with `mcpx.retrieve({key})`.

## Prerequisites

- The mcplexer daemon must be running locally (dashboard at
  <http://localhost:3333>). The shim talks to its Unix socket
  (`/tmp/mcplexer.sock`, or `$XDG_RUNTIME_DIR/mcplexer.sock` on Linux; override
  with `MCPLEXER_SOCKET_PATH`).
- The `mcplexer` binary must be on `PATH` (override with `MCPLEXER_BIN`).

If a tool call fails with "is the daemon running?", start mcplexer first.
