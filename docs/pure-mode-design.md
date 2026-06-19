# Pure Mode Design

Pure Mode is a harness/operator setting that makes a coding session token-cheap
by hiding every MCP tool and denying cached MCP calls. It is for local-only
coding sessions where the model should use the harness's native file/search/shell
tools instead of the mcplexer MCP surface.

## Behavior

- Default is off.
- When on, the client sees an empty `tools/list` response.
- Every `tools/call` is denied, including stale cached built-ins and downstream
  calls through Code Mode.
- Dispatch still enforces Pure Mode even if a stale client tries to call a tool
  it learned before the toggle changed.
- The dashboard/settings API and `MCPLEXER_PURE_MODE=0` env override are the
  recovery paths.

## Enforcement Layers

Use both advertisement-time and dispatch-time enforcement:

1. Tool advertisement returns the canonical empty payload: `{"tools":[]}`.
2. Dispatch returns a clear denied error for every tool.
3. Harness setup docs should mention the Pure Mode recovery path.
4. Audit records denied calls with `actor_kind`, workspace, and tool name.

The dispatch gate is required because MCP clients cache tool lists.

## Scope

The first shipped version is global and default-off:

- `pure_mode=false` by default in settings.
- `MCPLEXER_PURE_MODE=1` enables it for the process.
- `MCPLEXER_PURE_MODE=0` or `false` disables it even if the settings row says
  `pure_mode=true`.
- Workspace/session scope remains future work.

Global Pure Mode can strand MCP clients, so operators should expose it with a
clear dashboard warning. The env override exists specifically for headless
recovery when the settings row cannot be edited through MCP.

## Recovery

Recovery paths:

- Dashboard/settings API: set `pure_mode=false`.
- Environment: restart with `MCPLEXER_PURE_MODE=0`.
- CLI command and a dedicated MCP recovery tool remain future work.

There is intentionally no MCP recovery tool in v1 because the shipped behavior
is a uniform "no MCP tools" hard gate.

## Implementation Steps

1. Add settings fields for Pure Mode.
2. Add a pure-mode decision helper.
3. Return an empty `tools/list` before tool descriptions are serialized.
4. Deny dispatch in the common gateway call path.
5. Add dashboard and CLI toggles.
6. Add tests for cached-tool dispatch denial and env recovery.

## Tests

- With Pure Mode on, `tools/list` returns `{"tools":[]}`.
- Calling a cached tool returns a denied error.
- `MCPLEXER_PURE_MODE=0` beats a settings row set to true.
- Audit rows are written for denied calls.
