# Memory subsystem

mcplexer's memory layer is the cross-harness, cross-machine, cross-team
fact + note store. This doc is the short-form reference: what's there,
what's reachable, and how the layers compose.

For the running narrative + tuning notes, recall the
`memory-feature-design` memory (`memory__recall` with that name).
For deep schema rationale, the migration headers
(`058_memory.sql`, `076_memory_entities.sql`, `077_memory_recall_events.sql`)
are the canonical design docs.

## The five axes

A single memory row participates in five orthogonal axes of retrieval:

| Axis | Substrate | Surface |
|--|--|--|
| **Lexical** | FTS5 + porter stemmer | `memory__recall` / `POST /memory/search` |
| **Semantic** | sqlite-vec vec0 KNN (when an embedder is configured) | same — FTS5 + vec are RRF-fused at k=60 |
| **Structural (cued)** | `memory_entities` join table — "memories ABOUT entity X" | `memory__recall_about`, `GET /memory/entities/{kind}/{id}/related`, `/memory/about/:kind/:id` |
| **Co-occurrence** | self-join on entity links → "what else this is linked to" | `memory__related_entities`, AR1 |
| **Adjacency (semantic)** | seed memories about X → vec-neighbours → their entities | `memory__spreading_activation`, AR2 |

Plus a sixth, **opt-in**, axis:

| Axis | Substrate | Surface |
|--|--|--|
| **Learned co-recall** | async log of recall surfacings, scored by rank-proximity | `memory__co_recalled` / `memory__suggestions`, AR4+AR5 |

## Tool surface

All MCP tools live under the `memory__` prefix. They're dispatched
through `internal/gateway/handler_memory.go` and bound in the trust
classifier as built-ins (no per-call approval gate).

```
memory__save              # write a memory; optional entities=[{kind,id,role?}]
memory__recall            # FTS5 + vec0 RRF; optional entities + entities_any
memory__recall_about      # "everything ABOUT this entity"
memory__list              # paginated browse
memory__list_entities     # distinct entities ranked by memory_count
memory__related_entities  # co-occurring entities (AR1)
memory__spreading_activation  # semantic neighbour-walk (AR2)
memory__co_recalled       # memories that co-surfaced with this one (AR4)
memory__suggestions       # unified bundle: co_recall + related_entity + semantic (AR5)
memory__link_entity / __unlink_entity
memory__forget / __forget_by_source
memory__offer_memory / __request_memory   # peer-to-peer over libp2p
```

**Return shapes for programmatic use (execute_code / workers):**
`memory.list`, `memory.recall`, `memory.list_entities` (and recall_about etc.) return JSON objects (auto-unwrapped to JS objects in `mcpx__execute_code`):
- list: `{count: N, memories: [{id, name, kind, content, tags[], updated_at, scope}, ...]}`
- recall: `{count: N, hits: [{id, name, ..., score, source}, ...]}`
- list_entities: `{count: N, entities: [{kind, id, memory_count, last_linked_at}, ...]}`
This ensures `res.count`, `res.memories[0].id` etc work in worker snippets. (Previously these emitted prose "Listing N..." text, causing apparent 0 rows in JS.)

## Memory across harnesses, CLIs, and delegated workers

The memory surface is exposed uniformly for every supported MCP harness and
worker path (Codex/OpenAI, OpenCode, Grok CLI, MiniMax/GLM via opencode_cli,
Claude-compatible, direct vs server-prefixed). There is no per-harness
registration difference:

- Direct harnesses (Claude Code, Codex, native OpenCode) see the canonical
  4-tool slim surface and call `mcpx__search_tools` + `mcpx__execute_code`.
- Server-prefixed harnesses (Grok CLI and similar) see aliased single-segment
  names (`search_tools`, `execute_code` etc.) under the server prefix (e.g.
  `mcplexer__search_tools`); the gateway normalises on receipt.
- Delegated workers (any provider, any `model_provider`) are locked to the
  strict two-tool surface (`mcpx__search_tools` + `mcpx__execute_code`) by the
  worker dispatcher and receive the gateway `WorkerPreamble` (see
  `internal/gateway/preamble.md` and `WorkerPreamble` test). The preamble
  directs: "Your top-level tool surface is exactly two tools... Everything else
  ... memory ... is reachable from inside an `execute_code` snippet." and
  "Anything you want to persist across runs belongs in the `memory` namespace."

Discovery and invocation are identical in all cases: search first (pass
namespaces or queries containing "memory"), then `execute_code` with a JS
snippet. Inside the snippet the namespaces are objects with verb methods:
`memory.save({...})`, `memory.recall({query:...})`, `memory.list({...})`,
`memory.get({id})` etc. (search results list the canonical `memory__*` tool
names; the JS binding strips to the action under the namespace). The same
pattern applies to `task.*`, `mesh.*`, `skill.*`, `secret.*`, `mcpx.*`.

Worker allowlists (see `defaultDelegationTools` / `defaultDelegationToolsReview`
in `internal/workers/admin/delegation.go`) include `memory__save`/`recall`/`list`
(and core task tools) for execute-mode workers by default; review mode drops
mutating memory writes. Workers can also pick up memory guidance or domain facts
via attached skill bodies (the runner's `composeSystemPrompt` appends skill
bodies after the preamble using the `---` separator — "skill/body pickup").

No secrets or host credentials are injected into worker prompts or memory
calls; memory content lives in the gateway store under explicit workspace or
global scope with full audit.

See also CLAUDE.md (MCP harness compatibility + memory note), the
token-preservation-delegation docs/skill, and `internal/gateway/worker_surface_test.go`.

## REST surface

Everything reachable from the PWA is also reachable via REST
(`/api/v1/memory/*`). Use a bearer token from `~/.mcplexer/api-key`.

```
GET    /api/v1/memory                              # list (filters in querystring)
GET    /api/v1/memory/count
GET    /api/v1/memory/stats                        # brain-stats aggregate
POST   /api/v1/memory                              # create — body.entities supported
POST   /api/v1/memory/search                       # entities + entities_any supported
POST   /api/v1/memory/forget-by-source

GET    /api/v1/memory/{id}                         # one row
POST   /api/v1/memory/{id}/invalidate
POST   /api/v1/memory/{id}/pin       /unpin
DELETE /api/v1/memory/{id}

GET    /api/v1/memory/entities                     # distinct entities, ranked
GET    /api/v1/memory/entities/{kind}/{id}/related # AR1
GET    /api/v1/memory/entities/{kind}/{id}/spreading # AR2

GET    /api/v1/memory/{id}/entities                # links for one memory
POST   /api/v1/memory/{id}/entities                # add a link
DELETE /api/v1/memory/{id}/entities                # remove a link

GET    /api/v1/memory/{id}/co-recalled             # AR4
GET    /api/v1/memory/{id}/suggestions             # AR5

GET    /api/v1/memory/offers
POST   /api/v1/memory/offers/{id}/accept   /decline
```

## UI

The PWA serves the same data at `http://localhost:13333`:

```
/memory                            landing — brain stats + top entities
/memory/all                        list + filters + URL-backed drawer
/memory/about/:kind/:id            "everything about" pivot
/memory/shared                     incoming peer offers
/memory/consolidation              explicit per-workspace consolidator installs
```

The detail drawer surfaces:

- **About** section — entity-link chips (click pivots to `/memory/about/:kind/:id`, X unlinks)
- **You might also remember** section — AR5 suggestions, each labelled with `source` + `reason`

## Recall tracking — opt-in posture

`memory__co_recalled` and the `co_recall` axis of `memory__suggestions`
require a populated `memory_recall_events` table. Logging is **off by
default**. To enable:

1. Edit `~/Library/LaunchAgents/com.mcplexer.daemon.plist` and add to
   `EnvironmentVariables`:
   ```xml
   <key>MCPLEXER_RECALL_TRACKING</key>
   <string>1</string>
   ```
2. `launchctl kickstart -k gui/$UID/com.mcplexer.daemon`

When enabled, the recall path async-logs the top-10 hits of each call
through a buffered channel. Drops on overflow are counted
(`Service.recallDroppedCt`) but never block recall.

## Cross-peer transfer

Memory share rides the `/mcplexer/memory/1.0.0` libp2p protocol
(`internal/p2p/memory_share_*.go`). Wire envelopes:

- `MemoryOffer` — thin descriptor (name, kind, preview, tags, embed_model,
  + `entities_preview` top-5 of the entity links)
- `MemoryRequest` — request the full payload by `remote_id`
- `MemoryPayload` — full content + tags + metadata + entity links
  (peer-local kinds stripped by `FilterEntitiesForMesh`)

Peer-local entity kinds (currently `place`, `event`) are dropped on the
sender side so we never fabricate a `place:/Users/example/foo` link on a
different machine.

## Files at a glance

| Concern | Files |
|--|--|
| Schema | `internal/store/sqlite/migrations/058_memory.sql` / `076_memory_entities.sql` / `077_memory_recall_events.sql` |
| Store interface | `internal/store/store.go` (MemoryStore), `internal/store/models.go` |
| Sqlite impl | `internal/store/sqlite/memory.go`, `memory_query.go`, `memory_entities.go`, `memory_associative.go`, `memory_recall.go` |
| Service facade | `internal/memory/registry.go` |
| MCP tools | `internal/gateway/builtin_tools_memory.go` (schemas), `handler_memory.go` (handlers) |
| REST | `internal/api/memory_handler.go`, `memory_entities_handler.go` |
| Mesh wire | `internal/p2p/memory_share_*.go`, `cmd/mcplexer/memory_share_wire.go` |
| UI | `web/src/pages/memory/`, `web/src/api/memory.ts` |
