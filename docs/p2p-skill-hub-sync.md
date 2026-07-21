# P2P Skill Hub Sync

**Status:** Partial — index, search, and pull implemented (Phases A–B); push, notify, background sync, and workspace bindings are planned (Phases C–E). Deploy to hosted hub blocked on Proxmox/Tailscale/peer-access.
**Owner:** mcplexer core / p2p
**Last updated:** 2026-06-12
**Related:** `internal/p2p/registry_share_p2p.go`, `internal/p2p/hub_sync.go`, `internal/gateway/handler_hub_sync.go`, `internal/gateway/builtin_tools_hub_sync.go`, `docs/skill-format.md`, `.planning/linked-workspaces/PLAN.md`, `.planning/picoplexer/10-directory-revocation.md`

This document is the **design and implementation reference** for hub-sync, federated search, push
semantics, workspace bindings, and the public-scale upgrade path. Phases A–B
(index, search, pull) are merged to `main`; the skeleton branch is no longer
separate. Push (`mesh__skill_hub_push`) and batch sync (`mesh__skill_hub_sync`)
are **not yet implemented** — see §7 and §8.

---

## 1. Overview

The hub-sync extension lets an always-on mcplexer peer (the **hub**) serve its
skill-registry index to paired peers. Callers compare content hashes against local
state, selectively pull missing entries, and (optionally) search the hub catalog
before pulling bodies.

Git is an optional mirror for backup/versioning, but the live backing store is
the hub's local SQLite `skill_registry_entries` table — not a git repo.

### 1.1 Goals

1. **Pull-first sync** — leaf peers discover what the hub has, pull only what
   they lack, never auto-overwrite on hash mismatch.
2. **Federated search** — agents run `mcpx__skill_search` locally first; when
   the local catalog is thin, query the hub with the same natural-language
   contract and rank remote hits alongside local ones.
3. **Explicit push** — upstream propagation is opt-in and scope-gated; the hub
   never silently overwrites a leaf's divergent copy.
4. **Workspace routing** — workspace-scoped registry rows can replicate across
   *linked* workspace bindings (same pattern as memory/task routing).
5. **Public-scale path** — paired libp2p mesh today; content-addressed HTTPS
   snapshots tomorrow without rewriting the wire model.

### 1.2 Non-goals (this epic)

- Replacing `.mcskill` signed install (`/mcplexer/skill/1.0.0`, `mesh.skill_request`).
- CRDT merge of skill bodies.
- Anonymous write to a hub (all mutating paths require pairing + scope).

---

## 2. Protocol surface

The extension reuses `/mcplexer/skill-registry/1.0.0` (same libp2p protocol as
the existing single-entry request). The `type` field in the JSON header
determines the message kind.

| `type`     | Direction | Purpose                                      |
| ---------- | --------- | -------------------------------------------- |
| `request`  | pull      | Fetch one entry's body + bundle (existing)   |
| `index`    | pull      | Full or incremental index                    |
| `search`   | pull      | Ranked query over hub-visible entries        |
| `push`     | push      | Upstream publish of one global entry         |
| `notify`   | push      | Lightweight delta after hub mutation         |

All message types require `mesh.registry_request` scope on the paired peer
(independent of `mesh.skill_request` which gates `.mcskill` install).

### 2.1 Index request (pull)

```
→ {"type":"index","since_version":0,"remote_workspace_id":""}\n
← {"entries":[{...}],"cursor":"..."}
```

| Field                 | Meaning                                                                 |
| --------------------- | ----------------------------------------------------------------------- |
| `since_version`       | Optional. Per-name monotonic filter; `0` = full index.                  |
| `remote_workspace_id` | Optional. Sender's workspace id when pulling via a workspace binding.     |

The hub returns all entries it is willing to share for the resolved scope.
**Phase 1 (MVP):** global entries only (`workspace_id IS NULL`).
**Phase 2:** workspace-scoped entries when a `workspace_peer_binding` exists
for `(peer, remote_workspace_id)` — see §5.

Response shape (`HubIndexResponse`):

```json
{
  "entries": [
    {
      "name": "deploy-fly",
      "version": 3,
      "content_hash": "sha256:abcdef1234567890",
      "description": "Use when deploying to Fly.io",
      "author": "agent-1",
      "bundle_sha": "sha256:fedcba0987654321",
      "scope": "global",
      "remote_workspace_id": ""
    }
  ],
  "cursor": ""
}
```

`cursor` is reserved for paginated indexes at public scale (§6). Empty string
means complete in one line.

### 2.2 Single-entry request (existing)

```
→ {"type":"request","name":"foo","version":0}\n
← framed (body, bundle)  OR  {"type":"error","code":"...","message":"..."}
```

Unchanged from `internal/p2p/registry_share_p2p.go`. `version=0` resolves to
latest active on the responder.

### 2.3 Search request (federated pull)

```
→ {"type":"search","q":"deploy to fly.io","limit":10,"remote_workspace_id":""}\n
← {"hits":[{...}]}
```

The hub runs the **same TF-IDF ranking** as local `mcpx__skill_search`
(`internal/skillregistry` search index) over entries visible for the resolved
scope. This keeps agent ergonomics identical: natural-language `q`, ranked
hits with score + description preview.

Response (`HubSearchResponse`):

```json
{
  "hits": [
    {
      "name": "deploy-fly",
      "version": 3,
      "score": 0.842,
      "description": "Use when deploying to Fly.io",
      "content_hash": "sha256:abcdef...",
      "scope": "global",
      "remote_workspace_id": ""
    }
  ]
}
```

**Gateway tool:** `mesh__skill_hub_search` — wraps the wire call and returns
the same concise ranked-result style as `mcpx__skill_search`, clearly scoped to
the queried hub peer.

**Federated search contract (agent workflow):**

1. Call `mcpx__skill_search({ query })` against the session scope
   (`workspace ∪ ancestors ∪ global` per `handler_skill_registry.go`).
2. If hits are insufficient (empty, low score, or agent needs more catalog depth),
   call `mesh__skill_hub_search({ peer_id, q, limit })` against the configured
   hub peer(s).
3. For any remote hit worth keeping, call `mesh__skill_hub_pull` (or
   `mesh__request_registry_skill`) — never inline body bytes in search results.
4. Merge policy: local workspace-scoped heads **shadow** global heads with the
   same name (existing registry precedence). Remote imports land as global
   unless workspace binding routes them (§5).

**Multi-hub merge (open for v2):** when multiple hub peers are configured,
query in priority order, dedupe by `(name, version, content_hash)`, keep the
highest score per name.

### 2.4 Push request (upstream)

Leaf peers may propagate a **new global** registry entry upstream to the hub.
Workspace-scoped pushes are deferred to Phase 2 (linked bindings only).

Two-phase, mirroring memory/task offer safety:

```
→ {"type":"push","phase":"offer","name":"foo","version":3,
    "content_hash":"sha256:...","description":"...","body_len":4096,
    "bundle_sha":"sha256:...","bundle_len":0}\n
← {"type":"push","phase":"offer_ack","accept":true}\n

→ {"type":"push","phase":"payload","name":"foo","version":3}\n
← framed (body, bundle)  OR  error line
```

Hub-side handling:

1. **Dedup:** if `(name, version, content_hash)` already present → `accept:false`
   with code `deduped` (no payload transfer).
2. **Conflict:** if `(name, version)` exists with a **different** hash →
   `accept:false` with code `conflict` — hub does not overwrite (§3).
3. **Accept:** hub publishes via `PublishSkillRegistryEntry` (global scope),
   records `origin_peer_id` in metadata, fans out `notify` to subscribed peers.

Push requires `mesh.registry_push` scope (new boolean scope, distinct from
`mesh.registry_request`). Default off; operator enables per paired peer.

**Gateway tool (NOT implemented):** `mesh__skill_hub_push` does not exist yet.
There is no handler in `internal/gateway/handler_hub_sync.go` and no
`builtin_tools_hub_sync.go` entry for it. The wire protocol is designed but has
no code path. Until implemented, use the **export/import workaround** described
in `docs/skills-hub-deploy-runbook.md` to move skills between daemons.

### 2.5 Notify (downstream delta)

After the hub publishes or receives an upstream push, it may push a lightweight
delta to paired peers that have `mesh.registry_request` and opted into
`mesh.registry_notify` (default on for same-user Tier-1 peers).

```
→ {"type":"notify","entries":[{"name":"foo","version":4,"content_hash":"..."}]}\n
← {"type":"notify_ack"}\n
```

Notify carries **index metadata only** — no bodies. Receivers enqueue a
background pull (or surface in dashboard) using existing conflict rules.
Notify is idempotent and safe to drop; index pull is the reconciliation source
of truth.

**Relationship to replication coordinator:** today's `OnSkillInstall` in
`internal/replication/replication.go` fans out `.mcskill` installs to Tier-1
peers. Registry hub-sync is a **parallel path** for the agent-facing registry
(`skill_registry_entries`), not a replacement. A peer may receive the same
skill via both paths; content-hash dedup prevents duplicate versions.

---

## 3. Versioning and conflict model

Each registry entry is identified by `(name, version, content_hash)`.
`content_hash` is SHA-256 of `(body + bundle)` (existing publish path).

- **Immutable content hash:** once published, an entry's hash never changes.
  Different content → new monotonic version.
- **No last-writer-wins:** hash mismatch for the same `(name, version)` marks a
  **conflict candidate**. Both sides preserve their copy; sync stops for that
  name until resolved.
- **Stale divergent base:** peer A at v3, hub at v5 → A pulls v4+v5
  incrementally. A at v3 with a **different** hash than hub's v3 → conflict;
  A does **not** auto-pull v4+v5 until resolved.

### 3.1 Conflict resolution (operator / agent)

| Resolution            | Action                                                        |
| --------------------- | ------------------------------------------------------------- |
| Keep local            | Pin local; exclude name from auto-pull                        |
| Take remote           | Import hub entry as new version (never rewrite in-place)      |
| Fork                  | Publish local body as `name-fork` or new major version        |
| Defer                 | Leave `conflict` flag; dashboard shows side-by-side diff      |

Planned store surface: `skill_sync_conflicts` table or flags on
`skill_registry_entries` metadata — implementation detail left to skeleton
follow-up (`ConflictDetector` in `hub_sync.go`).

---

## 4. Scope gating

| Scope                    | Gates                                              |
| ------------------------ | -------------------------------------------------- |
| `mesh.registry_request`  | Inbound index/search/request + outbound pull       |
| `mesh.registry_push`     | Inbound push payload + outbound upstream push      |
| `mesh.registry_notify`   | Inbound notify deltas (optional, default Tier-1)   |
| `mesh.skill_request`     | `.mcskill` install path only (unchanged)           |

Peers never infer scope from message content; missing scope → `skillShareDenied`
wire error (same posture as `registry_share_p2p.go`).

---

## 5. Workspace bindings

Mesh registry replication must respect workspace isolation the same way memory
and tasks do after the routing fix documented in
`.planning/linked-workspaces/PLAN.md`.

### 5.1 Phase 1 — global only

Hub index/search returns rows where `workspace_id IS NULL`. Matches the
skeleton branch behaviour and the comment on `HubIndexProvider` in
`internal/p2p/hub_sync.go`.

### 5.2 Phase 2 — linked workspace routing

Reuse `workspace_peer_bindings` (migration 061) — no parallel identity table.

| Concept              | Mapping                                                       |
| -------------------- | ------------------------------------------------------------- |
| Sender workspace     | `remote_workspace_id` on index/search/push wire fields        |
| Receiver routing     | Resolve `(peer_id, remote_workspace_id) → local_workspace_id` |
| Send-side gating     | Only peers with `linked=1` binding for the workspace          |
| Visibility           | Hub includes workspace-scoped rows for bound remote ids only  |

**Index/search with binding:**

```
→ {"type":"index","remote_workspace_id":"01JABC..."}\n
```

Hub `HubIndexProvider` filters:

- global rows (always), plus
- workspace rows where `workspace_id` matches a binding that authorises the
  requesting peer.

**Import routing:** `HandleIncomingRegistryEntry` gains optional
`remote_workspace_id`; receiver publishes into the bound local workspace
(same pattern as `HandleIncomingMemory` post-routing-fix).

**Linked workspace auto-sync:** when `internal/replication.Coordinator` fires
`OnSkillRegistryPublish` (new hook, mirrors `OnTaskEvent`), fan out only to
peers with `linked=1` for that workspace — not all Tier-1 peers.

### 5.3 Workspace shadowing preserved

Local precedence is unchanged:

1. Workspace-scoped registry row shadows global for the same name.
2. Global row is visible everywhere.
3. Remote import into a workspace does not delete a pre-existing local
   workspace-scoped head with the same name — conflict path applies.

---

## 6. Public-scale upgrade path

Today's design targets **paired libp2p peers** with one or more designated hub
daemons (always-on Mac / home server). The wire types are deliberately
transport-agnostic so a later HTTPS catalog does not fork the data model.

### 6.1 Scale stages

| Stage | Topology                              | Discovery              | Index transport        |
| ----- | ------------------------------------- | ---------------------- | ---------------------- |
| S0    | Single hub peer, manual `peer_id`     | `mesh__list_peers`     | libp2p `type:index`    |
| S1    | Hub role flag on peer + UI picker     | Paired peers tagged    | libp2p + background sync |
| S2    | Multi-hub read replicas               | Ordered hub list in config | libp2p fan-out      |
| S3    | Public read-only catalog              | `/.well-known/mcplexer/v1/skills/index` | HTTPS GET + signature |
| S4    | Federated directories                 | Per-org well-known (see picoplexer §B) | DNS/HTTPS + libp2p fallback |

### 6.2 HTTPS catalog (S3) — additive

Publish periodic **content-addressed snapshots** of the global index:

```
GET https://skills.example.com/.well-known/mcplexer/v1/skills/index
→ {
     "snapshot_id": "sha256:...",
     "published_at": "2026-06-11T12:00:00Z",
     "signature": "...",
     "entries": [ HubIndexEntry, ... ]
   }
```

- Bodies remain pull-only: paired `type:request` over libp2p, or
  `GET .../skills/{name}/{version}/body` when operator enables public read.
- Snapshot signature uses the hub's existing age/signing key material (exact
  scheme deferred — see Open Questions).
- `cursor` pagination in `HubIndexResponse` maps 1:1 to `?cursor=` on HTTPS.

### 6.3 What does not change across stages

- `(name, version, content_hash)` identity
- Conflict model (no LWW)
- Scope gating on mutating paths
- TF-IDF search semantics

---

## 7. Gateway MCP surface

Built on the registry-share stream handlers. Index, search, and pull are
implemented on `main`. Push, notify, batch sync, and policy management are
**not yet implemented** — `ErrHubSyncNotImplemented` is returned for unregistered
message types in `hub_sync_stream_p2p.go`.

| Tool                      | Status          | RW    | Description                              |
| ------------------------- | --------------- | ----- | ---------------------------------------- |
| `mesh__skill_hub_index`   | implemented     | read  | Pull hub index                           |
| `mesh__skill_hub_search`  | implemented     | read  | Federated TF-IDF search over hub catalog |
| `mesh__skill_hub_pull`    | implemented     | write | Pull one entry into local registry       |
| `mesh__skill_hub_push`    | **NOT implemented** | write | Upstream push of global entry        |
| `mesh__skill_hub_sync`    | **NOT implemented** | write | Index + classify + pull missing (batch)  |

Admin (CWD-gated): `mcplexer__set_hub_peer`, `mcplexer__set_skill_sync_policy`.

---

## 8. Implementation sequence

Ordered so each step is shippable and testable on paired integration nodes
(`test-mcplexer` / `scenario_skill_*`).

### Phase A — skeleton merge (branch `feat/hub-sync-skeleton`)

1. Merge `hub_sync.go`, `hub_sync_stream_p2p.go`, stream handler branch for
   `type:index` in `registry_share_p2p.go`.
2. Wire `HubIndexProvider` adapter in `cmd/mcplexer/serve.go`.
3. Implement `ConflictDetector` backed by `skill_registry` store.
4. Gateway: `mesh__skill_hub_index`, `mesh__skill_hub_pull`, dispatch wiring.
5. Unit tests: provider, detector, stream round-trip (already on skeleton).
6. Integration: two-node index + pull + hash dedup.

### Phase B — federated search

1. Add `type:search` handler; hub runs `skillregistry.Registry.Search`.
2. Gateway `mesh__skill_hub_search` + agent-mesh skill doc update.
3. Integration: search hit → pull → local `mcpx__skill_get` round-trip.

### Phase C — push + notify

1. Migration: `mesh.registry_push` + `mesh.registry_notify` scope booleans on
   `p2p_peers` (or scopes JSON).
2. Two-phase `type:push` wire + hub publish path.
3. `type:notify` fan-out on hub publish; receiver background pull queue.
4. Gateway `mesh__skill_hub_push`; audit events.

### Phase D — workspace bindings

1. Wire `remote_workspace_id` on index/search/import.
2. `HubIndexProvider` binding-aware filter.
3. `OnSkillRegistryPublish` replication hook gated on `linked=1`.
4. Integration: linked workspace pair syncs workspace-scoped skill; global-only
   peer does not see it.

### Phase E — operator UX + background sync

1. Dashboard: hub peer picker, conflict resolution UI, sync status.
2. Periodic `HubSyncService.SyncFromPeer` cron (daemon config).
3. Metrics: entries synced, conflicts, pull latency, search QPS.

### Phase F — public catalog (optional / later)

1. Snapshot exporter job on hub.
2. `/.well-known/mcplexer/v1/skills/*` static or `mcplexer catalog serve`.
3. Signature verification on leaf import.

---

## 9. Open questions

| # | Question | Notes |
| - | -------- | ----- |
| 1 | **Search index freshness** | Hub search rebuilds on every request vs cached index invalidated on publish? Prefer same cache as local `mcpx__skill_search`. |
| 2 | **Push auth for non-Tier-1 peers** | Should cross-user push ever be allowed, or hub-only for Tier-1 same-user? Default: Tier-1 only. |
| 3 | **Notify vs poll** | Is notify required for MVP, or is periodic index pull enough? Notify reduces latency; poll is simpler. |
| 4 | **Conflict store shape** | Separate `skill_sync_conflicts` table vs metadata flag on existing rows? |
| 5 | **HTTPS signature scheme** | Reuse skill signing (ADR 0002) vs new catalog key? |
| 6 | **Rate limits** | Per-peer index/search RPM at scale — token bucket on hub? |
| 7 | **Public body access** | Are any skills world-readable without pairing, or index-only public? Default: index public, body requires pair. |
| 8 | **since_version semantics** | Per-name high-water mark vs global monotonic cursor? Per-name matches version model. |

---

## 10. Relationship to skeleton branch

The skeleton branch (`feat/hub-sync-skeleton`) has been merged to `main`.
This table shows what was delivered vs what remains in this doc.

| Item | `feat/hub-sync-skeleton` (merged) | This doc (gaps filled) |
| ---- | --------------------------------- | ---------------------- |
| Index wire format | ✓ `HubIndexEntry` | ✓ + `scope`, `cursor`, `remote_workspace_id` |
| Pull | ✓ `mesh__skill_hub_pull` | ✓ unchanged |
| Contention | ✓ conflict model prose | ✓ + resolution table |
| Federated search | listed as follow-up | ✓ §2.3 contract + tool |
| Push semantics | — | ✓ §2.4–2.5 two-phase push + notify (design only) |
| Workspace bindings | global-only note | ✓ §5 phased binding routing |
| Public scale | — | ✓ §6 staged upgrade |
| Implementation order | bullet follow-ups | ✓ §8 phased sequence |

---

## 11. Verification checklist

- [ ] Markdown links resolve (`docs/skill-format.md`, ADRs, planning docs)
- [ ] Wire examples parse as single-line JSON + trailing `\n`
- [ ] Scope names do not collide with `mesh.skill_request`
- [ ] Precedence rules match the workspace shadowing rules in this document
- [ ] Integration scenarios sketched for phases A–D in `test/integration/`

---

## Appendix A — Types (skeleton reference)

Go types live on `feat/hub-sync-skeleton` in `internal/p2p/hub_sync.go`:

- `HubIndexEntry`, `HubIndexRequest`, `HubIndexResponse`
- `HubSyncResult`, `HubIndexProvider`, `ConflictDetector`, `HubSyncService`

Planned additions (design-only until implemented):

```go
// HubSearchRequest — wire frame for federated search.
type HubSearchRequest struct {
    Type               string `json:"type"` // "search"
    Q                  string `json:"q"`
    Limit              int    `json:"limit,omitempty"`
    RemoteWorkspaceID  string `json:"remote_workspace_id,omitempty"`
}

// HubSearchHit — one ranked search result.
type HubSearchHit struct {
    Name              string  `json:"name"`
    Version           int     `json:"version"`
    Score             float64 `json:"score"`
    Description       string  `json:"description"`
    ContentHash       string  `json:"content_hash"`
    Scope             string  `json:"scope"` // "global" | "workspace"
    RemoteWorkspaceID string  `json:"remote_workspace_id,omitempty"`
}
```
