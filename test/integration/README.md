# mcplexer integration harness

Multi-node docker harness that spins up three `mcplexer` daemons + an
OpenAI-compatible echo server on a shared bridge network, then exercises
the public REST surface end-to-end: pairing, mesh broadcast, skill
publish + fetch, worker run, audit propagation.

```
                 ┌───────────────┐
                 │   echo-llm    │  /v1/chat/completions stub
                 └───────┬───────┘
                         │
         ┌───────────────┴───────────────┐
 :13333 │ :13334                 :13335 │  ports on the host
  ┌──────┴──┐  ┌──────────┐  ┌───────────┴┐
  │ node-a  │──│  node-b  │──│   node-c   │  libp2p mesh on
  └─────────┘  └──────────┘  └────────────┘  mcplexer-test-net
```

## Quick start

```
make test-integration            # build + run + tear down
TEST_KEEP=1 make test-integration  # keep containers up for debugging
```

The wrapper traps EXIT and tears down `docker compose down -v
--remove-orphans` on success or failure. On failure it also dumps
per-service logs to `test/integration/_logs/`.

## What the scenarios cover

Each step prints `=== STEP n: <name> ===` followed by PASS / FAIL / SKIP
lines, then a final summary count.

| # | Scenario | Notes |
|---|----------|-------|
| 1 | Health   | All three nodes return `status=ok`, `p2p_enabled=true`. |
| 2 | Provision | Each node creates a workspace + auth scope via REST. |
| 3 | P2P identity | Each node exposes a distinct libp2p PeerID. |
| 4 | Pairing  | Tries `pair/start` + `pair/complete` between nodes. **May SKIP** — see "libp2p in Docker" below. |
| 5 | Mesh send | `POST /api/v1/mesh/send` on node-a; node-a observes its own broadcast in `/api/v1/mesh/status`. |
| 6 | Skill registry | Publish a markdown skill on node-a, fetch it back, compare bodies. |
| 7 | Worker run | Create an `openai_compat` worker on node-c pointed at `echo-llm`, fire run-now, poll until terminal status. |
| 8 | Audit    | Every node has audit rows for the workspace/scope mutations; node-c specifically has a `worker_*` row. |

The exit code is non-zero if any step prints FAIL. SKIP rows are
informational and do not fail the run.

## Container layout

* `Dockerfile.mcplexer` — three-stage build:
  1. Node 20 builds the React PWA → `internal/web/dist`.
  2. Go 1.25 builds `mcplexer-p2p` with `-tags p2p` (CGO off; modernc.org/sqlite is pure Go).
  3. Debian-slim runtime + curl (healthcheck) + ca-certificates.
* `Dockerfile.echo-llm` — single-file Go binary that responds to
  `/v1/chat/completions` with a static valid completion. Used by the
  worker scenario so we don't need a real LLM provider.
* `entrypoint.sh` — preps `/data/p2p`, then `exec`s `mcplexer serve` so
  PID 1 == the daemon (SIGTERM propagates cleanly).
* `docker-compose.yml` — `node-a/b/c` on ports `3333/3334/3335`, each
  with a named volume (`mcplexer-data-a/b/c`) and the shared
  `mcplexer-test-net` bridge.

## Adding a new scenario

1. Add a `scenario_<name>() { ... }` function in `scenarios.sh` following
   the existing PASS/FAIL/SKIP helpers.
2. Append a `scenario_<name>` call to `main()`.
3. Document it in the table above.

The helpers expect:

* JSON-returning APIs — use the `api` (curl wrapper) + `assert_jq` pair.
* Status-only checks — use `api_status` (returns the HTTP code).
* `step N "label"` once per logical scenario.

## CI

In GitHub Actions or any CI runner with docker buildx available, run:

```yaml
- name: Integration test
  run: make test-integration
  env:
    TEST_LOGS_DIR: ${{ github.workspace }}/_integration_logs

- if: failure()
  uses: actions/upload-artifact@v4
  with:
    name: integration-logs
    path: _integration_logs
```

A few CI specifics:

* The web build (npm ci) is the cold-cache bottleneck — ~90 s. Mount a
  buildx cache (`type=gha`) keyed on `web/package-lock.json` to drop
  warm builds under 30 s.
* The Go build is fully reproducible on `golang:1.25-bookworm` thanks
  to `-trimpath`. No need to pin to a specific minor.
* The shared `mcplexer-test-net` bridge is created/torn down per run;
  no host-network leakage.

## Troubleshooting

### "Step 4: Pairing — SKIP (status=500 …)"

The pairing handshake walks the libp2p DHT for the responder's
multiaddrs. On a closed docker bridge with no public IPFS bootstrap
peers reachable, that walk times out. We treat this as SKIP rather
than FAIL because:

* The REST contract is exercised (pair/start returns code + peer_id,
  pair/complete is reachable and gates correctly).
* The handshake failure is an environmental property of the closed
  network, not a regression in mcplexer.

To exercise the full handshake, either (a) wire a libp2p bootstrap
peer into the compose file and set `MCPLEXER_P2P_BOOTSTRAP=…`, or
(b) run the test on a host where the daemons can reach each other on
the LAN (mDNS) — set `NODE_A/B/C` to those hosts and skip docker.

### Port collisions (13333/13334/13335 already taken)

The harness uses 1333{3,4,5} on the host (NOT the canonical 3333) so
it can coexist with a running `mcplexer daemon` on the host. If you
need different ports, edit `docker-compose.yml` and set `NODE_A/B/C`
env vars when invoking `scenarios.sh`.

### libp2p discovery + macOS Docker Desktop

macOS Docker Desktop runs containers inside a Linux VM, so the bridge
network is fully Linux. mDNS multicast works between containers on
the same user-defined bridge; it does NOT cross the macOS host
boundary. That is intentional — the harness never depends on host-VM
multicast.

### `failed to read api-key from one or more nodes`

The daemon writes `/data/api-key` on first request. If your container
exited before the first health probe, the file may not exist yet. The
healthcheck (curl on `/api/v1/health`) is what triggers the auth
middleware path that lazily creates the file. Re-run; if it persists,
inspect `docker compose logs node-a` for a sqlite migration failure.

### `worker run did not succeed`

* Confirm `echo-llm` is healthy: `docker compose ps`.
* Curl it from inside node-c: `docker exec mcplexer-test-node-c curl -fs http://echo-llm:8080/healthz`.
* If echo-llm is up but the run is stuck `running`, dump node-c logs:
  `docker compose logs node-c | grep worker_run`.

### Container build takes >5 minutes

The cold web build (npm ci) is the worst offender. Subsequent
rebuilds reuse the docker layer cache. If you change a single Go
file, the Go build stage rebuilds but the web stage stays cached.
