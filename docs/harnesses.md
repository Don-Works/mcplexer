# Harnesses

MCPlexer supports two setup paths:

- **MCP server wiring** for clients that can register MCP servers.
- **Native Pi extension** for Pi, which deliberately avoids generic MCP server
  registration and exposes only the slim mcplexer tools.

The dashboard's **AI Harnesses** page is the single setup surface. It shows MCP
wiring, bootstrap status, last initialization, and any local skill or command
accretion that could bloat a harness session.

## MCP server harnesses

These harnesses use a normal MCP server entry that points at
`mcplexer connect`:

| Harness | MCP client id | Bootstrap target |
| --- | --- | --- |
| Claude Code | `claude_code` | `~/.claude/CLAUDE.md` plus `~/.claude/skills/using-mcplexer/SKILL.md` |
| Codex | `codex` | `~/.codex/AGENTS.md` |
| OpenCode | `opencode` | `~/.config/opencode/AGENTS.md` plus `~/.config/opencode/skills/using-mcplexer/SKILL.md` |
| Gemini CLI | `gemini_cli` | `~/.gemini/GEMINI.md` |
| Grok CLI | `grok` | `~/.grok/config.toml` |
| MiMoCode | `mimocode` | `~/.config/mimocode/AGENTS.md` |

The MCP server only advertises the slim top-level surface:

- `mcpx__search_tools`
- `mcpx__execute_code`
- `secret__prompt`
- `secret__list_refs`

All other namespaces, downstream MCP servers, and skills are discovered through
`mcpx__search_tools` and invoked inside `mcpx__execute_code`.

## Server-prefixed clients

Some clients qualify tool names with the MCP server name. For example, a tool
called `mcpx__execute_code` can become `mcplexer__mcpx__execute_code`, which
contains too many namespace separators for clients such as Grok, Cursor,
Windsurf, Gemini CLI, and Picoclaw.

MCPlexer detects those clients and advertises single-segment aliases instead:

- `search_tools` maps to `mcpx__search_tools`
- `execute_code` maps to `mcpx__execute_code`
- `prompt` maps to `secret__prompt`
- `list_refs` maps to `secret__list_refs`

Incoming calls are normalized back to the canonical names at the gateway.

## Pi native extension

Pi is a minimal, MCP-skeptical terminal coding agent
([pi.dev](https://pi.dev), `earendil-works/pi`). MCPlexer should not be added to
Pi as a classic MCP server entry. That would preload tool definitions into Pi's
context, which is exactly what Pi is designed to avoid.

Use the native package in [`integrations/pi/`](../integrations/pi/) instead.
It surfaces four thin Pi tools:

- `mcpx_search` -> `mcpx__search_tools`
- `mcpx_exec` -> `mcpx__execute_code`
- `mcpx_secret_refs` -> `secret__list_refs`
- `mcpx_secret_prompt` -> `secret__prompt`

Install it with:

```bash
pi install git:github.com/don-works/mcplexer
```

Restart Pi, run `/mcplexer`, then use `mcpx_search({queries:["task create"]})`
and `mcpx_exec({code:"..."})`. The native package also provides an on-demand
`/skill:mcplexer` playbook.

For development, load the extension directly:

```bash
pi -e integrations/pi/extensions/mcplexer.ts
```

Pi is still a **HarnessDirect** client to the gateway. The native shim sends
canonical tool names verbatim, and client detection maps Pi-like
`clientInfo.name` values to the `pi` harness key for bootstrap and initialization
receipts.

## Bootstrap sync

The `using-mcplexer` bootstrap is managed per harness. It tells agents to use
the four top-level tools, fetch the full `using-mcplexer` skill contract through
`mcpx.skill_get({name:"using-mcplexer"})`, and use mcplexer memory as the source
of truth.

The dashboard can install or recheck the bootstrap for every supported harness,
including Pi. The generic MCP install list intentionally excludes Pi because Pi
uses the native extension path above.
