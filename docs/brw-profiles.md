# brw multi-profile sync

Drive several browser profiles (e.g. 2 Chrome + 2 Chromium) from one agent and
let it pick which browser per task. Each [brw](https://brw.donworks.co.uk)
profile runs its own `brwd --bridge` daemon (one extension connection per
daemon), and MCPlexer exposes each as its own namespace so an agent selects a
browser by calling `brw__*`, `brw_chromium__*`, etc.

`mcplexer brw sync` automates the gateway-side wiring: it consumes brw's
discovery output and ensures one downstream server + route per browser.

## How it fits together

```
brwctl daemons ──▶ [{namespace, http_addr, identity}, …]
                        │
        mcplexer brw sync (this command)
                        │
        ┌───────────────┴───────────────┐
   downstream server                route(s)
   (stdio: brwd --upstream-http      (workspace → namespace,
    <http_addr> --mcp)                allow brw__*)
```

The brw HTTP daemon is brw's own control API, **not** an MCP endpoint — so each
namespace is a stdio `brwd --mcp` child that proxies to the daemon's HTTP bridge
via `--upstream-http <http_addr>`. That `http_addr` comes straight from
`brwctl daemons`.

## Usage

```sh
# Dry-run (default): print the plan, write nothing.
mcplexer brw sync --workspace <workspace-id>

# Feed an explicit roster instead of exec'ing `brwctl daemons`:
mcplexer brw sync --from daemons.json --workspace <workspace-id>

# Apply for real:
mcplexer brw sync --apply --workspace <workspace-id>

# Remove brw-owned servers/routes that no longer appear in the roster:
mcplexer brw sync --apply --prune --workspace <workspace-id>
```

Flags: `--from <file|->` (roster JSON; empty execs `brwctl daemons`),
`--workspace` (repeatable / comma-separated target workspace ids for routes),
`--apply` (default is dry-run), `--prune`, `--brwd-path`, `--policy`,
`--db <path>`, `--json`.

## What it does (and won't do)

- **Namespace** is derived deterministically from each daemon's identity
  workspace (`brw-chromium` → `brw_chromium`); server id `brw-<namespace>`,
  route id `brw-route-<workspace>-<namespace>`. Re-running is idempotent.
- **Adopts** an existing server that already holds a namespace: a `source="brw"`
  server is updated in place (preserving its created-at + capabilities cache); a
  server with **any other source is left completely untouched** — the sync never
  overwrites a manually-registered server, it just points routes at it.
- **Routes** are created only for workspaces that exist (missing ones are
  reported as skipped). One allow route per (workspace, namespace), priority 50.
- **Prune** (opt-in) deletes only `source="brw"` rows absent from the roster.
- All writes go through `config.Service`, so transport / namespace-uniqueness /
  route-reference validation runs.

To pin a workspace to a single browser, sync only that namespace's route into
it; to let the agent choose, route all brw namespaces into the workspace.

After an `--apply`, reload so the gateway introspects the new children:
`mcpx__reload_server` (or it picks them up on the next routing-cache refresh).

## Adding a new browser profile, end to end

1. Add a `Profile` to brw's `browser-profiles.json` (its own `bridge_http_addr`
   / `bridge_ws_addr` / `bridge_extension_id`) and launch its `brwd --bridge`.
2. Point that profile's extension at the daemon's port (Options page).
3. `mcplexer brw sync --apply --workspace <ws>` → the new browser shows up as its
   own namespace, ready for the agent to drive.
