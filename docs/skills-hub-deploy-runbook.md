# Skills Hub Deploy Runbook

**Status:** Draft — deploy blocked on Proxmox/Tailscale/peer-access (task `01KTVF4HZB9421AWJ39XP7D5HF`)
**Owner:** mcplexer ops / p2p
**Last updated:** 2026-06-12
**Related:** `docs/p2p-skill-hub-sync.md`, task `01KTVF4HWZ1K7R94TZRDFCQR34`

This runbook covers deploying an mcplexer peer as a **skills hub** — an
always-on daemon that serves its skill-registry index to paired leaf peers.
It is written for a single operator managing a Proxmox LXC + Tailscale setup.

---

## 1. Prerequisites

| Requirement | Notes |
|-------------|-------|
| Proxmox VE host | LXC container recommended (unprivileged, Debian 12/Ubuntu 22.04) |
| Tailscale installed on hub LXC | Hub must be reachable by leaf peers over Tailnet |
| Tailscale installed on each leaf peer | Same Tailnet, or peered via exit nodes |
| mcplexer binary built with `-tags p2p` | Enables libp2p + hub sync handlers |
| At least one leaf peer paired | Hub needs at least one `p2p_peers` row with `mesh.registry_request` scope |
| Existing mcplexer data directory | Use the daemon, dashboard, CLI, or MCP tools to inspect it; do not query the live database directly during normal ops |
| SQLite WAL mode | Default; no special config needed |

### 1.1 Blocked items

- **Proxmox API access** — LXC provisioning requires Proxmox host credentials.
- **Tailscale auth key** — new LXC needs a Tailscale auth key or pre-shared key.
- **Leaf peer access** — leaf peers must be on the same Tailnet or have libp2p
  reachable addresses.

These are tracked in deploy child task `01KTVF4HZB9421AWJ39XP7D5HF`.

---

## 2. Install / Start

### 2.1 Build the binary

```bash
go build -tags p2p -o mcplexer ./cmd/mcplexer
```

The `-tags p2p` flag enables:
- `internal/p2p/hub_sync.go` (index/search/pull handlers)
- `internal/p2p/hub_sync_stream_p2p.go` (stream dispatch)
- `internal/gateway/handler_hub_sync.go` (MCP tool handlers)

### 2.2 Start the daemon

```bash
# Standard start — hub runs like any other mcplexer daemon
./mcplexer serve --p2p --server-profile=skills

# Or with explicit config
MCPLEXER_PORT=13333 ./mcplexer serve --p2p --server-profile=skills
```

The hub daemon is a normal mcplexer peer. There is no separate "hub mode"
flag — the hub role is determined by which peers are paired and what
scopes they have.

### 2.3 Verify hub sync is active

```bash
# Check the daemon is listening
curl -s http://127.0.0.1:13333/api/v1/health
```

Then verify the hub tools are exposed from an MCP client with
`mcpx__search_tools({queries:["mesh skill hub"]})`; expected tools are
`mesh__skill_hub_index`, `mesh__skill_hub_search`, and
`mesh__skill_hub_pull`.

Optional log check:

```bash
grep -i "hub_sync\\|registry_share\\|skill_hub" ~/.mcplexer/logs/*.log
```

---

## 3. Pairing and Scopes

### 3.1 Required scopes for hub communication

| Scope | Purpose | Direction |
|-------|---------|-----------|
| `mesh.registry_request` | Index, search, pull from hub | leaf → hub |
| `mesh.registry_push` | Push skills upstream to hub | leaf → hub (NOT implemented yet) |
| `mesh.registry_notify` | Receive delta notifications | hub → leaf (NOT implemented yet) |

### 3.2 Pair a leaf peer

From the hub's dashboard or CLI:

```bash
# Pair a peer (generates pairing request on leaf, approve on hub)
# In dashboard: Mesh → Pair New Peer
```

After pairing, grant `mesh.registry_request` scope to the leaf peer:

```bash
# Via mcplexer CLI or dashboard: Settings → Peers → Scopes
# Enable: mesh.registry_request
```

### 3.3 Set the hub peer on leaf

On each leaf peer, designate which peer is the hub:

```bash
# Via dashboard: Settings → Skill Sync → Hub Peer
# Or via MCP tool (CWD-gated):
# mcplexer__set_hub_peer({ peer_id: "<hub-peer-id>" })
```

---

## 4. Verification Commands

Run these from a **leaf** peer after pairing + scope grant + hub designation:

### 4.1 Index pull

```
mesh__skill_hub_index({ peer_id: "<hub-peer-id>", since_version: 0 })
```

Expected: returns a list of `HubIndexEntry` objects from the hub's global
skill registry.

### 4.2 Federated search

```
mesh__skill_hub_search({ peer_id: "<hub-peer-id>", q: "deploy to fly.io", limit: 5 })
```

Expected: ranked search hits with score, name, version, description.

### 4.3 Pull a skill

```
mesh__skill_hub_pull({ peer_id: "<hub-peer-id>", name: "<skill-name>", version: 0 })
```

Expected: the skill entry is imported into the leaf's local registry as a
global-scope entry. `version: 0` pulls the latest.

### 4.4 Verify local registry

```
mcpx__skill_search({ query: "<imported skill name>" })
```

Expected: the pulled skill appears in local results.

---

## 5. Export/Import Workaround (Push)

**`mesh__skill_hub_push` is NOT implemented.** There is no code path for
upstream push — `ErrHubSyncNotImplemented` is returned for unregistered
message types.

### 5.1 Export a skill from leaf

On the leaf peer, export the skill entry to a file:

```
mcpx__skill_export({ name: "<skill-name>", version: "latest", include_bundle: true })
```

Save the returned package JSON as the handoff artifact, or use the dashboard:
Skill Registry -> select entry -> Export.

### 5.2 Import on the hub

On the hub peer, import the exported package:

```
mcpx__skill_import({ package_json: "<exported-package-json>", commit: false })
mcpx__skill_import({ package_json: "<exported-package-json>", commit: true })
```

Keep the first call as a dry run. Only run the second call after reviewing the
diff/provenance returned by the dry run. The dashboard import flow should use
the same dry-run-before-commit pattern.

### 5.3 Notes

- This is a **manual workaround** until `mesh__skill_hub_push` is implemented
  (Phase C in `docs/p2p-skill-hub-sync.md` §8).
- Content-hash dedup still applies: importing a skill that already exists with
  the same `(name, version, content_hash)` is a no-op.
- Conflict model (§3 in `docs/p2p-skill-hub-sync.md`) applies: if hub has a
  different hash for the same `(name, version)`, the import is rejected.

---

## 6. Upgrade

### 6.1 Upgrade the hub daemon

```bash
# Stop the running daemon
kill $(pgrep -f "mcplexer serve")

# Rebuild with p2p tag
go build -tags p2p -o mcplexer ./cmd/mcplexer

# Restart
./mcplexer serve
```

### 6.2 Upgrade a leaf peer

Same process. Leaf peers do not need to re-pair after upgrade; pairing state
is persisted by mcplexer and should be inspected through the dashboard or peer
tools.

### 6.3 After upgrade: re-verify

Run §4 verification commands to confirm hub sync still works.

---

## 7. Rollback

### 7.1 Rollback the hub daemon

```bash
# Stop the running daemon
kill $(pgrep -f "mcplexer serve")

# Check out the previous release tag or commit
git checkout <previous-tag-or-commit>

# Rebuild and restart
go build -tags p2p -o mcplexer ./cmd/mcplexer
./mcplexer serve
```

### 7.2 Rollback a leaf peer

Same process. Pairing state is daemon-managed and is not affected by binary
rollback.

### 7.3 Rollback considerations

- **No data loss**: skill registry entries are persisted in the mcplexer data
  store, not in the binary.
- **Wire compatibility**: hub sync uses `/mcplexer/skill-registry/1.0.0`
  protocol. As long as both hub and leaf run a version that speaks this
  protocol, pairing works. If the protocol version changes, both sides
  must be on compatible versions.
- **Conflict state**: any `conflict` flags in `skill_sync_conflicts` (or
  metadata) are preserved across rollback.

---

## 8. Known Blockers

| Blocker | Status | Notes |
|---------|--------|-------|
| Proxmox LXC provisioning | blocked | Requires Proxmox API credentials + Tailscale auth key |
| Tailscale mesh connectivity | blocked | Hub LXC must be reachable from all leaf peers |
| `mesh__skill_hub_push` | not implemented | Phase C in design doc; manual export/import workaround |
| `mesh__skill_hub_sync` (batch) | not implemented | Phase E in design doc; manual index + pull per skill |
| `mesh.registry_push` scope | not implemented | No code path to grant or check this scope |
| `mesh.registry_notify` | not implemented | No background delta push from hub to leaves |

---

## 9. Quick Reference

### Hub-side verification

```
mesh__list_peers({})
mcpx__skill_list({})
```

### Leaf-side verification

```bash
# List local skills
mcpx__skill_search({ query: "" })

# Check hub peer is set
# Dashboard -> Settings -> Skill Sync -> Hub Peer
```

### Log inspection

```bash
# Hub sync activity
grep -i "hub_sync\|registry_share\|skill_hub" ~/.mcplexer/logs/*.log

# Pairing events
grep -i "pairing\|peer_connected" ~/.mcplexer/logs/*.log
```
