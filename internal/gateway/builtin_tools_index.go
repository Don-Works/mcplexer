package gateway

import "encoding/json"

// indexToolDefinitions returns the full 10-tool `index__*` surface for the
// local codebase indexer: build/status/symbols/deps here, plus the query
// tools (tests_for/summary/recent_changes/map_failure/context/search) from
// indexQueryToolDefinitions. Registered (gated on h.store != nil) in both
// codeModeBuiltinTools and buildAllBuiltinTools so the tools dispatch and are
// slim-surface discoverable.
func indexToolDefinitions() []Tool {
	core := []Tool{
		{
			Name:        "index__build",
			Description: "Build or incrementally refresh the shared local index for this repo root. Enumerates through git/.gitignore, excludes dependency/build/generated/credential paths, extracts Go + TS/JS symbols/imports, and chunks allowed source for citation-ready FTS search. Unchanged content is skipped. `force:true` re-extracts every file in scope without deleting out-of-scope rows. The result reports completeness, chunks, and optional local-embedding backfill; index__status reports freshness.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"paths": {"type": "array", "items": {"type": "string"}, "description": "Restrict the build to these root-relative path prefixes (e.g. [\"internal/gateway\"]). Omit to index the whole repo."},
					"force": {"type": "boolean", "description": "Re-extract every file in scope instead of using freshness shortcuts. Default false (incremental)."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Build Code Index",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__status",
			Description: "Freshness and readiness for the repo-root-shared code index: physical index id, build completeness, indexed vs current git state, dirty-file count, file/symbol/chunk totals, warnings, and optional local-embedding progress. Incomplete builds are stale and retry on the next query. Run index__build after large edits or branch switches.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Code Index Status",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__symbols",
			Description: "Find where a function, method, type, const, class, or component is defined — definition lookup over the code symbol map by name or words (camelCase is word-split, so 'kv set' finds HandleKVSet). Returns file:line hits with signatures; use this instead of grepping the repo. Results reflect the last index__build (check index__status if unsure). For a whole-task, multi-file context pack use index__context instead.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Name or space-separated words to match. Required."},
					"kind": {"type": "string", "enum": ["func", "method", "type", "const", "var", "class", "interface", "enum", "component"], "description": "Restrict to one symbol kind."},
					"exported_only": {"type": "boolean", "description": "Only exported/public symbols."},
					"limit": {"type": "integer", "description": "Default 20, max 100."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["query"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Search Code Symbols",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__deps",
			Description: "File-level import graph. direction=imports: what this file imports (Go: package dirs; TS: resolved files; externals flagged). direction=importers: which files import this one — the blast-radius question before a change. Not a call graph. To find owning tests use index__tests_for.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "Root-relative file path. Required."},
					"direction": {"type": "string", "enum": ["imports", "importers", "both"], "description": "Default imports."},
					"limit": {"type": "integer", "description": "Default 50."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["file"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Code Import Graph",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
	return append(core, indexQueryToolDefinitions()...)
}
