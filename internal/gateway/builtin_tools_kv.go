package gateway

import "encoding/json"

func kvToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "kv__set",
			Description: "Persist a JSON value under a key for this workspace so it survives across mcpx__execute_code calls. Each execute_code call runs in a fresh sandbox, so in-memory variables are lost between calls — use kv to build an expensive dataset once and rehydrate it later with kv__get instead of recomputing it. Scratch storage (default 120-minute TTL, pinnable), not a durable store: promote real conclusions with memory__save. Caps: 1 MiB per value, 256 keys and 16 MiB per workspace.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string", "description": "Stable key, e.g. customers-2026 or run-123. Required."},
					"value": {"description": "Any JSON value (object, array, string, number, boolean). Stored verbatim and returned as-is by kv__get."},
					"ttl_minutes": {"type": "integer", "description": "Default 120. Set 0 with pinned=true for no expiry."},
					"pinned": {"type": "boolean", "description": "When true, no TTL is applied unless ttl_minutes is positive."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["key", "value"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Set KV State",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "kv__get",
			Description: "Read a value previously stored with kv__set. Returns the value verbatim (auto-unwrapped to a JS value in the sandbox), or null when the key is absent or expired — so `const data = kv.get({key}) || rebuild()` works.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string"},
					"workspace_id": {"type": "string"}
				},
				"required": ["key"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Get KV State",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "kv__list",
			Description: "List stored keys for the current workspace (metadata only, no values), newest first. Filter by key prefix. Returns total_bytes so you can see how close you are to the per-workspace cap.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"prefix": {"type": "string", "description": "Only keys starting with this prefix."},
					"include_expired": {"type": "boolean"},
					"limit": {"type": "integer", "description": "Default 100, max 500."},
					"offset": {"type": "integer"},
					"workspace_id": {"type": "string"}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List KV State",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "kv__delete",
			Description: "Delete a stored key. Idempotent: deleting an absent key returns ok with deleted=false rather than erroring.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string"},
					"workspace_id": {"type": "string"}
				},
				"required": ["key"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Delete KV State",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
