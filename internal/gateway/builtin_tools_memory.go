package gateway

import "encoding/json"

// allMemoryToolDefinitions returns the universal memory__* tools — the
// agent-facing surface of the memory subsystem (migration 058).
// Available from every CWD: memory is data, not configuration, and the
// admin-only operations (browse all-workspace, force-delete by source,
// manage peer offers) live in internal/control under mcplexer__memory_*.
// Callers should go through memoryToolDefinitions (builtin_tools_memory_caps.go),
// which drops the associative tools whose backing capability isn't live.
func allMemoryToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "memory__save",
			Description: "Save / remember / write-down / record / persist a memory the agent (or its user) should be able to recall later. Two kinds: `note` (longer markdown body; no uniqueness; default) and `fact` (atomic key/value; updates atomically supersede the prior active row in the same scope+name bucket). Pick a short, descriptive `name` (e.g. \"preferred-editor\", \"flight-itinerary\") — optional: omitted names are auto-derived from the content's leading words plus a content hash (same content → same name, so re-saves are idempotent; explicit names are still better for facts you'll update later). `tags` are free-form labels for filtering. `scope` is one of `auto` (default: workspace if the session has one, else global), `workspace`, or `global`. `entities` (optional) link this memory to one or more objects it is ABOUT — each `{kind, id, role?}` ties it to a task / person / place / peer / agent / org / skill / artifact / event / workspace. Reserved kinds: task, person, place, peer, agent, org, skill, artifact, event, workspace. ID shapes by kind: task=ULID, peer=libp2p PeerID, agent='<peer>:<agent_name>', person=email or '@handle', artifact=URL or 'gh:owner/repo#N', place=path/slug, workspace=ULID. Role defaults to 'subject'; use 'mentioned' for passing references and 'derived_from' when the memory was extracted from this entity. `entities` also accepts the shorthand forms `[\"task:01ABC\", \"person:a@example.com\"]`, a single `{kind, id}` object, or a single \"kind:id\" string. Returns the saved memory's id.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Short slug identifying this memory. Optional — auto-derived from content when omitted."},
					"content": {"type": "string", "description": "The memory body (markdown for notes; the value for facts)."},
					"kind": {"type": "string", "description": "Memory kind (default: note). Canonical values are 'note' and 'fact'; common synonyms are mapped server-side — decision/preference/project_fact/setting → fact, anti-pattern/lesson/observation → note. An unmappable value returns a did-you-mean error rather than silently dropping the save."},
					"tags": {"type": "array", "items": {"type": "string"}, "description": "Optional tags for later filtering."},
					"scope": {"type": "string", "enum": ["auto", "workspace", "global"], "description": "Visibility scope (default: auto)."},
					"pinned": {"type": "boolean", "description": "Pin this memory so the consolidator won't auto-prune it."},
					"entities": {
						"type": "array",
						"description": "Optional entity links: this memory is ABOUT each of these objects.",
						"items": {
							"type": "object",
							"properties": {
								"kind": {"type": "string", "description": "Entity kind: task|person|place|peer|agent|org|skill|artifact|event|workspace or your own vocab."},
								"id":   {"type": "string", "description": "Entity identifier: ULID, email, URL, path, slug — depends on kind."},
								"role": {"type": "string", "enum": ["subject", "mentioned", "derived_from"], "description": "Optional link role; default 'subject'."}
							},
							"required": ["kind", "id"]
						}
					}
				},
				"required": ["content"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Save Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__recall",
			Description: "Search memories by natural language. Returns up to `limit` ranked hits (default 10) drawn from the current workspace + global scope. When an embedding provider is configured, FTS5 + vector results are fused with reciprocal-rank-fusion. When it isn't, FTS5 keyword search runs alone — still useful. Pass `tags` to require every tag on each hit (AND). Pass `entities` to narrow by what the memory is ABOUT (AND across entries) — `entities_any` is the OR variant. Pass an empty query plus tags/entities/source filters to browse rather than search. Returns `{count, summary, hits[]}` — READ `count` TO DECIDE WHETHER ANYTHING MATCHED, never `hits.length`: the sandbox's compact() helper columnarises `hits` into `{_cols,_rows}` once there are 3+ results, so `hits.length` is undefined exactly when the result set is largest.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Natural language search query. Empty = newest memories first."},
					"limit": {"type": "integer", "description": "Max results (default 10, max 50)."},
					"kind": {"type": "string", "enum": ["fact", "note"], "description": "Filter to one kind."},
					"tags": {"type": "array", "items": {"type": "string"}, "description": "Require every tag to be present."},
					"include_invalid": {"type": "boolean", "description": "Include rows that have been superseded (default false)."},
					"valid_at": {"type": "string", "description": "RFC3339 timestamp for a bi-temporal as-of view: return what was believed at this instant (rows whose validity window covered it), e.g. 2026-01-15T09:00:00Z. Omit for current beliefs only. A row superseded after this instant is still returned because it was valid then. Note: with an embedding provider configured, as-of recall may UNDER-return historical rows on the vector arm (the vector index reflects current rows); use memory__list with valid_at for an exhaustive as-of view."},
					"entities": {
						"type": "array",
						"description": "AND across links: every {kind,id} must be linked to the memory. Role is optional (matches any role when omitted).",
						"items": {
							"type": "object",
							"properties": {
								"kind": {"type": "string"},
								"id":   {"type": "string"},
								"role": {"type": "string", "enum": ["subject", "mentioned", "derived_from"]}
							},
							"required": ["kind", "id"]
						}
					},
					"entities_any": {
						"type": "array",
						"description": "OR across links: at least one {kind,id} must be linked.",
						"items": {
							"type": "object",
							"properties": {
								"kind": {"type": "string"},
								"id":   {"type": "string"},
								"role": {"type": "string", "enum": ["subject", "mentioned", "derived_from"]}
							},
							"required": ["kind", "id"]
						}
					}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Recall Memory",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__list",
			Description: "Browse memories without searching. Same scope + filter semantics as memory__recall, but ordered by updated_at DESC. Useful for \"show me everything tagged onboarding\" or pagination via offset. Pass `scope` to narrow to one side of the workspace/global divide — the memory consolidator uses this for its two-pass mode (global pass + workspace pass) so consolidated notes are written back to the right scope.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["fact", "note"], "description": "Filter to one kind."},
					"tags": {"type": "array", "items": {"type": "string"}, "description": "Require every tag to be present."},
					"limit": {"type": "integer", "description": "Max rows (default 50, max 200)."},
					"offset": {"type": "integer", "description": "Skip this many rows for pagination."},
					"include_invalid": {"type": "boolean", "description": "Include superseded rows."},
					"valid_at": {"type": "string", "description": "RFC3339 timestamp for a bi-temporal as-of view: return what was believed at this instant (rows whose validity window covered it), e.g. 2026-01-15T09:00:00Z. Omit for current beliefs only. A row superseded after this instant is still returned because it was valid then."},
					"scope": {"type": "string", "enum": ["any", "workspace_only", "global_only"], "description": "Narrow visibility. \"any\" (default) = workspaces ∪ global, matches memory__recall. \"workspace_only\" = only the session's workspace, excludes global. \"global_only\" = only workspace_id IS NULL rows. Used by the consolidator's scope-preserving two-pass mode."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Memories",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__recall_about",
			Description: "Recall every memory ABOUT a specific entity — the 'tell me everything about Alice' surface. Pass `kind`+`id` for the entity (e.g. {kind:'task', id:'01KSG…'} or {kind:'person', id:'alice@example.com'}). Optionally narrow with `query` (FTS5/vector search within the matched set), `memory_kind` (fact|note), `tags`, `role` (subject|mentioned|derived_from — empty matches any), and `limit` (default 20, max 100). Returns the same ranked-hit shape as memory__recall but scoped to memories linked to that entity.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind":        {"type": "string", "description": "Entity kind (e.g. task, person, place, peer, agent, org, skill, artifact, event, workspace)."},
					"id":          {"type": "string", "description": "Entity identifier (ULID/email/URL/slug/path — depends on kind)."},
					"role":        {"type": "string", "enum": ["subject", "mentioned", "derived_from"], "description": "Optional: only memories linked to this entity with this role."},
					"query":       {"type": "string", "description": "Optional natural-language search within the entity-linked subset."},
					"limit":       {"type": "integer", "description": "Max results (default 20, max 100)."},
					"memory_kind": {"type": "string", "enum": ["fact", "note"], "description": "Optional: only fact or only note memories."},
					"tags":        {"type": "array", "items": {"type": "string"}, "description": "Require every tag to be present."}
				},
				"required": ["kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Recall Memories About Entity",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__list_entities",
			Description: "List the distinct entities (objects memories are ABOUT) in this workspace + global scope, ranked by memory count DESC then last-linked DESC. Use to discover what entities your memory store talks about — feeds the entity-picker autocomplete + 'Top entities' dashboard tile. Pass `kind` to scope to one entity kind (e.g. only persons).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind":   {"type": "string", "description": "Optional: only entities of this kind."},
					"limit":  {"type": "integer", "description": "Max rows (default 50, max 200)."},
					"offset": {"type": "integer", "description": "Skip this many rows for pagination."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Entities",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__related_entities",
			Description: "Return entities that CO-LINK with the named entity in at least one memory — the 'what else is this related to' surface. Different from memory__list_entities (which surfaces all entities, ranked by memory count) and memory__recall_about (which returns memories about one entity). Use to discover the graph neighbourhood: 'what other entities co-occur with task:T in the same memories'. Ranked by shared_count DESC.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind":  {"type": "string", "description": "Entity kind of the query node."},
					"id":    {"type": "string", "description": "Entity identifier of the query node."},
					"limit": {"type": "integer", "description": "Max results (default 20, max 100)."}
				},
				"required": ["kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Related Entities",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__spreading_activation",
			Description: "Spreading activation: given an entity, find vec-neighbours of the memories about it and return the entities those neighbours are about — answering 'what feels conceptually adjacent to this entity even without an explicit link'. Different from memory__related_entities (which surfaces structurally co-linked entities). Requires an embedding provider to be configured — returns an empty set otherwise. The shared_count field on each result is repurposed as a score-proxy (higher = more strongly activated).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind":  {"type": "string", "description": "Entity kind of the query node."},
					"id":    {"type": "string", "description": "Entity identifier of the query node."},
					"limit": {"type": "integer", "description": "Max entities to return (default 10, max 50)."}
				},
				"required": ["kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Spreading Activation",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__co_recalled",
			Description: "Return memories that frequently co-surface with the given memory in the recall log (AR4 — learned associative recall). Requires MCPLEXER_RECALL_TRACKING=1 on the daemon AND prior recall activity to have produced events. Score weights co-occurrence by rank-proximity (memories adjacent at the top of result sets score higher). Empty when tracking is off or no signal yet.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"memory_id": {"type": "string", "description": "Source memory id."},
					"limit":     {"type": "integer", "description": "Max results (default 10, max 50)."}
				},
				"required": ["memory_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Co-Recalled Memories",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__suggestions",
			Description: "Return a unified 'you might also remember' bundle for a memory (AR5). Composes three signals: co-recall (AR4 — empty when tracking is off), related-entity (076), and semantic vec-neighbours (when an embedder is configured). Deduplicates across sources; each result carries a `source` + `reason` field so the caller can explain why it surfaced. The proactive-surfacing surface — agents can call this without knowing what to search for.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"memory_id": {"type": "string", "description": "Source memory id."},
					"limit":     {"type": "integer", "description": "Max results (default 12, max 50)."}
				},
				"required": ["memory_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Memory Suggestions",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__link_entity",
			Description: "Add an 'about X' link to an existing memory. Idempotent on (memory_id, kind, id, role) — re-linking is a no-op. Use this to enrich a memory you saved earlier (or one imported from a peer) with entity context.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"memory_id": {"type": "string", "description": "Memory id (returned by memory__save)."},
					"kind":      {"type": "string", "description": "Entity kind."},
					"id":        {"type": "string", "description": "Entity identifier."},
					"role":      {"type": "string", "enum": ["subject", "mentioned", "derived_from"], "description": "Optional role; default 'subject'."}
				},
				"required": ["memory_id", "kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Link Entity to Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__unlink_entity",
			Description: "Remove an 'about X' link from a memory. When `role` is omitted, every role flavour for the (memory_id, kind, id) triple is removed. Idempotent. The memory itself stays intact — only the link row is deleted.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"memory_id": {"type": "string", "description": "Memory id."},
					"kind":      {"type": "string", "description": "Entity kind."},
					"id":        {"type": "string", "description": "Entity identifier."},
					"role":      {"type": "string", "enum": ["subject", "mentioned", "derived_from"], "description": "Optional role; empty removes every role flavour for this triple."}
				},
				"required": ["memory_id", "kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Unlink Entity from Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__get",
			Description: "Fetch one memory by id. Returns the full content (no preview truncation) plus metadata — useful after memory__list / memory__recall surfaces an id you want the whole body of. Returns an error when the id is not found or has been forgotten.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Memory id (ULID, returned by memory__save / memory__list / memory__recall)."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Get Memory",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__invalidate",
			Description: "Mark a memory as superseded — sets t_valid_end to now and (optionally) records the new active row's id. The bi-temporal trail is preserved (the row is NOT deleted), but the entry is excluded from default queries. The consolidator's primary tool: collapse a cluster of near-duplicate notes into a single richer note + invalidate the originals pointing at it. NEVER use memory__forget for this — invalidation is reversible and auditable, soft-delete loses provenance.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Memory id to invalidate."},
					"superseded_by_id": {"type": "string", "description": "Optional id of the replacement memory. Pass when consolidating: the new consolidated note's id."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Invalidate Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__pin",
			Description: "Pin a memory so the consolidator won't auto-prune or consolidate it. The explicit \"this matters more\" affordance — pinned memories are excluded from sleep-time compaction and surface with a star in the UI. Idempotent: re-pinning is a no-op.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Memory id to pin."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Pin Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__unpin",
			Description: "Unpin a memory, restoring it to normal consolidator-eligible state. Idempotent.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Memory id to unpin."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Unpin Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__forget",
			Description: "Soft-delete one memory by id. The row stays in audit history but is excluded from default queries; the embedding row is dropped so KNN doesn't surface it. Use memory__forget_by_source for the \"purge a poisoned session\" pattern instead of calling this in a loop.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Memory id (returned by memory__save)."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Forget Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__forget_by_source",
			Description: "Hard-purge every memory whose source_session_id matches. The forensic redaction tool: use when you've identified a session/tool-call that wrote bad data (a hallucinated fact, a prompt-injection-induced summary, a leaked secret). Returns the count of rows deleted. The matching embedding rows are dropped too.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"source_session_id": {"type": "string", "description": "Session id whose memories should be purged."}
				},
				"required": ["source_session_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Forget Memory by Source",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__offer_memory",
			Description: "Offer one of your local memories to a paired libp2p peer. Sends a thin descriptor (name, kind, preview, tags) over the /mcplexer/memory/1.0.0 protocol; the receiver decides asynchronously whether to call memory__request_memory back. The peer must be paired AND have been granted the `mesh.memory_request` scope (use mesh__grant_peer_scope on their side). The receiver sees the offer in their dashboard's /memory/shared view; full content transfer only happens on accept.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"peer_id": {"type": "string", "description": "libp2p peer id of the paired remote machine."},
					"memory_id": {"type": "string", "description": "Local memory id (returned by memory__save) to offer."}
				},
				"required": ["peer_id", "memory_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Offer Memory to Peer",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "memory__request_memory",
			Description: "Pull a memory from a paired libp2p peer that previously offered it. Sends a request over /mcplexer/memory/1.0.0 referencing the offerer's remote memory id; receives the full payload (content + tags + metadata + optional embedding) and writes it locally with provenance set (source_kind=peer, origin_peer_id populated). Returns the new local memory id. The peer must be paired AND on your `mesh.memory_request`-granted side.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"peer_id": {"type": "string", "description": "libp2p peer id of the paired remote machine."},
					"remote_id": {"type": "string", "description": "The offerer's memory id (the id the offer descriptor carried)."}
				},
				"required": ["peer_id", "remote_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Request Memory from Peer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "memory__import_harness",
			Description: "Import memory files from harness-native memory systems (Claude Code, MiMoCode, etc.) into the mcplexer memory store. This is the migration bridge: run once to ingest existing harness memory, then use mcplexer memory going forward. Idempotent — re-running skips already-imported files. Pass `harness` to import from a specific harness only (claude-code, mimocode); omit to import from all.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"harness": {"type": "string", "enum": ["claude-code", "mimocode"], "description": "Import from a specific harness only. Omit to import from all."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Import Harness Memory",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "memory__sync_status",
			Description: "Check the status of the harness memory sync scanner. Returns whether the scanner is running, how many files were imported/skipped on the last scan, and the configured scan interval. Use to verify that the memory unification pipeline is active.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Memory Sync Status",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
