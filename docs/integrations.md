# Built-in integrations

mcplexer bundles a handful of optional in-process MCP backends for the
integrations that come up often enough to be worth shipping in the binary.
Each one is gated behind a `Disabled:true` row in the downstream-server
catalogue — you turn it on from the dashboard, supply credentials, and
restart the daemon.

Engineer-facing docs live under `internal/<name>/README.md`. This page is
the operator-facing index.

## Hammerspoon (macOS desktop automation)

Exposes [Hammerspoon](https://www.hammerspoon.org/) as a `hammerspoon__*`
tool surface: `list_windows`, `focus_app`, `screenshot`, `send_keys`,
`notify`, and (when explicitly enabled) `exec_lua` for arbitrary Lua
execution inside the user's Hammerspoon runtime. Lets a Claude session
drive the macOS GUI — focus an app, grab a screenshot, type a string —
without leaving the agent loop.

The integration is macOS-only. The mcplexer daemon talks to Hammerspoon
over a loopback `hs.httpserver` (default `127.0.0.1:27123`), authenticated
with a Bearer password mcplexer generates on first install. A CLI fallback
(`hs -c`) is selectable for zero-config setups but runs ~50× slower than
the HTTP path.

The raw-Lua `exec_lua` tool is off by default — it's a literal RCE
surface inside the user's GUI session. Toggle **Allow exec_lua** in the
dashboard only after you've thought about the threat model.

### Install steps

1. Install [Hammerspoon](https://www.hammerspoon.org/), launch it, and
   grant Accessibility permission.
2. In the mcplexer dashboard: **Servers → Hammerspoon → toggle on**.
   Restart the daemon (the integration manager is built at boot).
3. Click **Install bridge**. The installer writes
   `~/.hammerspoon/hammerspoon-mcp.lua`, `~/.hammerspoon/.mcp-password`,
   and appends `require("hammerspoon-mcp")` to `~/.hammerspoon/init.lua`
   (with a timestamped backup of the prior contents).
4. Click **Probe**. Five green checks means the bridge is live and the
   tool surface is callable from any agent session bound to a workspace
   that allows `hammerspoon__*`.

Full developer notes: `internal/hammerspoon/README.md`. Manual smoke
checklist: `internal/hammerspoon/SMOKE.md`.
