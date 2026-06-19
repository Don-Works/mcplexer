# internal/hammerspoon

In-process MCP backend that exposes [Hammerspoon](https://www.hammerspoon.org/)
as a `hammerspoon__*` tool surface to mcplexer agents. Lets a Claude/agent
session list macOS windows, focus apps, capture screenshots, send keystrokes,
post notifications, and (when explicitly enabled) run arbitrary Lua inside
the user's Hammerspoon runtime.

## Architecture

```
Claude (code mode)
    └─ mcpx__execute_code("hammerspoon.list_windows()")
         └─ mcplexer gateway routes hammerspoon__* → DownstreamServer "hammerspoon"
              └─ downstream.Manager.internalFor("hammerspoon") → *hammerspoon.MCPServer
                   └─ MCPServer.Call(toolName, args)
                        └─ Bridge.Exec(luaSnippet)
                             ├─ HTTPDriver:  POST http://127.0.0.1:27123/exec
                             │                Authorization: Bearer <password>
                             │                body: {"lua": "...", "timeout_ms": 5000}
                             │                → Hammerspoon http handler runs Lua,
                             │                  json-encodes {ok,result,err}, returns 200
                             └─ CLIDriver:   exec.Command("hs", "-c", luaSnippet)
                                              parse stdout as same JSON envelope
```

The Go side never speaks Lua semantics directly: it builds Lua call strings
from a small set of templates in `lua.go` (one function per tool). Both
drivers return the same `{ok, result, err}` JSON envelope so the dispatcher
is driver-agnostic. The bridge runtime — a tiny module loaded into the
user's `~/.hammerspoon/init.lua` — lives at `embed/hammerspoon-mcp.lua` and
is shipped baked into the Go binary via `//go:embed`.

## File map

| File | Responsibility |
|---|---|
| `manager.go` | `Manager` — holds the configured `Bridge` + `allowExecLua` gate. |
| `bridge.go` | `Bridge` interface, `Envelope` shape, `nullBridge` placeholder. |
| `bridge_http.go` | HTTP driver (default). Loopback Bearer auth, classifies transport errors. |
| `bridge_cli.go` | CLI driver. Shells to `hs -c`, ~50× slower than HTTP. |
| `lua.go` | One `build*Lua` helper per tool. Strings are quoted via `json.Marshal` (JSON ⊂ Lua for printable ASCII). |
| `tools.go` | Tool schemas advertised via `tools/list`. Split into `alwaysOnTools()` + `execLuaTool()`. |
| `mcpserver.go` | `MCPServer` — implements the `downstream.InternalBackend` shape. `ListTools` filters `exec_lua` by gate. |
| `dispatch.go` | Per-tool `callX` methods. Each unmarshals args, builds Lua, calls bridge, maps envelope to `CallToolResult`. |
| `embed.go` | `//go:embed embed/hammerspoon-mcp.lua` + small exported accessors used by the HTTP handler. |
| `embed/hammerspoon-mcp.lua` | The Lua module dropped into `~/.hammerspoon/`. |

## The `Bridge` interface

```go
type Bridge interface {
    Exec(ctx context.Context, lua string, timeout time.Duration) (Envelope, error)
}

type Envelope struct {
    Ok     bool            `json:"ok"`
    Result json.RawMessage `json:"result,omitempty"`
    Err    string          `json:"err,omitempty"`
}
```

Two production implementations ship:

- **`httpDriver`** (`bridge_http.go`). Default. POSTs to
  `<HAMMERSPOON_BRIDGE_URL>/exec` with `Authorization: Bearer <password>`.
  Classifies transport errors (connection refused → "Hammerspoon is not
  running", 401 → "bridge password mismatch", deadline → "bridge timed
  out") into user-meaningful envelope strings so the agent gets a clear
  fix-it message instead of a raw HTTP code. Per-call deadline lives on
  the context; the HTTP client itself has no top-level timeout.
- **`cliDriver`** (`bridge_cli.go`). Shells out to `hs -c "<lua>"`. Lua is
  passed through stdin via long brackets to dodge shell quoting hazards
  (see the inline comment in `bridge_cli.go` on the `[==[ ... ]==]`
  delimiter choice — `==` is the lowest bracket level that's safe given
  `lua.go` may emit `[==[` in tool templates).

A third type, `nullBridge`, is a placeholder used when no driver is
configured. Its `Exec` always returns `{ok:false, err:"Hammerspoon
downstream not enabled..."}` so the gateway gets a uniform error envelope
rather than panicking. `Manager.HasBridge()` returns false for it.

## Adding a new tool

1. **Schema** (`tools.go`) — append a map to `alwaysOnTools()` with `name`,
   `description`, and `inputSchema`. Tool names are returned without the
   `hammerspoon__` prefix; the gateway adds the namespace via
   `DownstreamServer.ToolNamespace`.
2. **Lua template** (`lua.go`) — add a `buildXxxLua(params...)` helper that
   returns a Lua chunk whose final statement is `return hs.json.encode(<table>)`
   so the envelope's `Result` carries pre-JSON-encoded output. Quote any
   user-supplied string via `luaQuote` (which marshals to JSON — JSON
   string syntax is a subset of Lua's for printable ASCII).
3. **Dispatch case** (`dispatch.go`) — add a `callXxx` method that
   unmarshals args, calls `s.m.Bridge().Exec(ctx, buildXxxLua(...), callTimeout)`,
   and hands the result to `renderEnvelope`. Wire it in `mcpserver.go`'s
   `Call` switch.
4. **Test** — add a row to `mcpserver_test.go`'s table over `fakeBridge`
   covering the Lua shape + the rendered MCP envelope. Cover an error
   path too (missing required arg → `errorResult`).

## Environment variables

All four live in the `hammerspoon-bridge` auth scope (`type:"env"`).
Field metadata is registered in `internal/config/seed_hammerspoon.go`.

| Key | Purpose | Default |
|---|---|---|
| `HAMMERSPOON_BRIDGE_PASSWORD` | Shared Bearer token for `/exec`. Generated on first `POST /api/v1/hammerspoon/install`. | required for HTTP driver |
| `HAMMERSPOON_BRIDGE_URL` | Base URL of `hs.httpserver`. | `http://127.0.0.1:27123` |
| `HAMMERSPOON_DRIVER` | `http` or `cli`. | `http` |
| `HAMMERSPOON_ALLOW_EXEC_LUA` | Gate the raw Lua escape hatch. | `false` |

Reads happen once in `cmd/mcplexer/serve.go` → `buildHammerspoonManager` at
daemon start; runtime flips require a daemon restart. The `Disabled` flag
on the downstream row is read at the same point — disabling and re-enabling
the integration requires a restart for the same reason.

## The `exec_lua` gate

`exec_lua` is a literal remote-code-execution surface inside the user's GUI
session — it can read the clipboard, send keystrokes, snapshot the screen,
and reach out over the network. It is **off by default**. To enable, set
`HAMMERSPOON_ALLOW_EXEC_LUA=true` in the `hammerspoon-bridge` auth scope
(via the dashboard's "Allow exec_lua" toggle or
`PUT /api/v1/auth-scopes/hammerspoon-bridge/secrets`).

The gate is enforced in two places:

1. `MCPServer.ListTools` filters `exec_lua` out of the tool list when the
   gate is off, so the agent can't discover it via the standard MCP
   handshake.
2. `MCPServer.Call("exec_lua", ...)` rejects directly when the gate is off
   — guarding against an agent that hard-codes the tool name.

Both behaviours are covered in `exec_lua_gate_test.go`. The
`integration_test.go` cases exercise the gate against a live (stub) HTTP
bridge.

## CLI driver quoting

`bridge_cli.go`'s `wrapLuaForCLI` embeds the agent's Lua snippet inside a
Lua long-bracket literal: `[==[ <lua> ]==]`. The `==` level (rather than
the bare `[[ ]]`) is deliberate: `lua.go` is free to emit `[[ ... ]]`
inside string literals or table constructors, and using `==` raises the
bridge's delimiter above any plausible payload's. The escape failure mode
is documented inline at `wrapLuaForCLI` — a user snippet that contains
the literal `]==]` will break the wrapper and surface as a load error
through the envelope, not silent corruption.

## Manual smoke testing

See `SMOKE.md` for the macOS checklist (requires Hammerspoon.app installed
and the bridge module loaded). The shell harness's
`scenario_hammerspoon.sh` only covers the REST surface against the
disabled-by-default state — it can't smoke-test the live Lua path on a
Linux CI box. The deeper end-to-end coverage (list_windows return shape,
exec_lua gate flip, 401 password mismatch, bridge-down) lives in
`integration_test.go`, which spins up an `httptest.Server` standing in
for `hs.httpserver`.
