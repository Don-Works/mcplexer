package gateway

import "encoding/json"

func dataToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "data__ingest",
			Description: "Ingest rows, JSONL/CSV text, or documents into a workspace-scoped scratch dataset. Returns collection metadata and inferred schema. Use for temporary API payloads, logs, tables, and RAG scratch sets; durable conclusions should be promoted separately with memory__save. Defaults to a 24h TTL unless pinned.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Stable collection name, e.g. issues or run-123. Required."},
					"kind": {"type": "string", "enum": ["table", "docs"], "description": "Default table."},
					"rows": {"type": "array", "items": {}, "description": "Array of objects or values to query as a table."},
					"documents": {"type": "array", "items": {"type": "object"}, "description": "Document objects with text/content/body plus optional metadata."},
					"text": {"type": "string", "description": "CSV or JSONL text to ingest when rows/documents are not provided."},
					"tags": {"type": "array", "items": {"type": "string"}},
					"ttl_minutes": {"type": "integer", "description": "Default 1440. Set 0 with pinned=true for no expiry."},
					"pinned": {"type": "boolean", "description": "When true, no TTL is applied unless ttl_minutes is positive."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Ingest Scratch Data",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__list",
			Description: "List visible scratch data collections in the current workspace. Filter by tags. Expired or dropped collections are hidden by default.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"tags": {"type": "array", "items": {"type": "string"}},
					"include_expired": {"type": "boolean"},
					"limit": {"type": "integer"},
					"offset": {"type": "integer"},
					"workspace_id": {"type": "string"}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Scratch Data",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__describe",
			Description: "Describe one scratch collection: schema, counts, tags, TTL, provenance, and timestamps.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string"},
					"workspace_id": {"type": "string"}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Describe Scratch Data",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__query",
			Description: "Run a bounded SELECT query against one scratch collection. The query executes in an isolated in-memory SQLite database containing only the collection rows. Use table `data`; if the collection name is a SQL identifier, that name is also available as a view.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string"},
					"sql": {"type": "string", "description": "Single SELECT/WITH query. No semicolons or writes."},
					"limit": {"type": "integer", "description": "Default 100, max 500."},
					"workspace_id": {"type": "string"}
				},
				"required": ["name", "sql"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Query Scratch Data",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__search",
			Description: "Search one scratch collection with the FTS5 retrieval floor. Semantic search is staged for a later embedding-backed path; the MVP always returns lexical ranked hits.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string"},
					"query": {"type": "string"},
					"limit": {"type": "integer", "description": "Default 10, max 50."},
					"semantic": {"type": "boolean", "description": "Accepted for forward compatibility; currently lexical FTS5."},
					"workspace_id": {"type": "string"}
				},
				"required": ["name", "query"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Search Scratch Data",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__drop",
			Description: "Drop a scratch collection and purge its item/FTS payloads. Use this at the end of a run or when TTL is not enough.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string"},
					"workspace_id": {"type": "string"}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Drop Scratch Data",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "data__harvest_harness_context",
			Description: "Discover allowlisted harness context files (Codex AGENTS/instructions files, Cursor rules), normalize them as documents, and ingest them into a data workbench collection. Returns a manifest with skipped/excluded files. Idempotent by collection name: re-harvesting replaces that workspace collection.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Collection name. Default harness-context."},
					"harnesses": {"type": "array", "items": {"type": "string", "enum": ["codex", "cursor", "all"]}, "description": "Harnesses to harvest. Default [\"codex\", \"cursor\"]. Use \"all\" for both."},
					"workspace_id": {"type": "string", "description": "Override current workspace. Required when no session workspace is resolved."},
					"home_dir": {"type": "string", "description": "Optional admin/test override for the home directory. Defaults to the current user home."},
					"work_dir": {"type": "string", "description": "Optional admin/test override for the workspace root. Defaults to the current workspace root."},
					"max_files": {"type": "integer", "description": "Max files per harvest (default 200)."},
					"max_bytes_per_file": {"type": "integer", "description": "Max bytes per file (default 262144)."},
					"max_total_bytes": {"type": "integer", "description": "Max total bytes ingested (default 4194304)."},
					"ttl_minutes": {"type": "integer", "description": "Default 1440. Set 0 with pinned=true for no expiry."},
					"pinned": {"type": "boolean", "description": "When true, no TTL is applied unless ttl_minutes is positive."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Harvest Harness Context",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
