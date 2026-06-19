package gateway

import "encoding/json"

// brainToolDefinitions returns the agent-facing brain__* tools — the read +
// note-write surface over the canonical Markdown brain (M0-M7). Reads project
// the derived SQLite index (workspace tree, task/memory lists, per-record
// detail, three-tier frecency search); the single write tool persists a
// free-form note through the same outbound Serializer the dashboard editor and
// the dual-write engine use, so a note authored here is byte-identical to one
// written in VSCode or by an agent tool.
//
// Available from every CWD: the brain is data, not gateway configuration.
// When the brain subsystem is disabled (no Editor wired) every tool returns a
// clear "brain subsystem is not enabled" tool-level error rather than failing.
func brainToolDefinitions() []Tool {
	tools := []Tool{
		{
			Name: "brain__tree",
			Description: "Browse the brain's workspace tree: every workspace with its parent " +
				"(client/org tier) and live task + memory counts. Use this first to discover " +
				"which workspaces exist before listing or searching. Counts are best-effort " +
				"live totals from the derived index — treat them as approximate, not authoritative.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "Brain Tree",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__list",
			Description: "List the records (tasks and/or memories) in one workspace, newest first. " +
				"Pass kind='task' or kind='memory' to narrow; omit kind to list both. For memories, " +
				"an empty or 'global' workspace returns the global (workspace-agnostic) records; " +
				"otherwise pass the workspace id from brain__tree.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["task", "memory"], "description": "Record kind to list. Omit to list both."},
					"workspace": {"type": "string", "description": "Workspace id (for memories, empty='global'). Defaults to the session workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "Brain List",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__get",
			Description: "Fetch one brain record by kind + id, including its verbatim on-disk " +
				".md (the exact text an agent reads). Use after brain__search or brain__list to " +
				"pull the full body + provenance of a hit.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["task", "memory"], "description": "Record kind. Required."},
					"id": {"type": "string", "description": "Record id (ULID). Required."}
				},
				"required": ["kind", "id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "Brain Get",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__search",
			Description: "Three-tier frecency intellisense over the brain index: exact-prefix, " +
				"token-boundary, then fuzzy substring — recently-touched records float up within " +
				"a tier. kind narrows to task|memory (empty = both). workspace scopes the search " +
				"(empty = all workspaces). NOTE: on a large index (>=10k records) the fuzzy tier " +
				"is dropped for latency; the result's fuzzy_off flag reports when that happened, " +
				"so substring-only matches silently disappear at scale.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"q": {"type": "string", "description": "Raw search text (NOT an FTS expression). Required."},
					"kind": {"type": "string", "enum": ["task", "memory", ""], "description": "Record kind (empty=both). Default empty."},
					"workspace": {"type": "string", "description": "Workspace scope (empty=all). Default empty."},
					"limit": {"type": "integer", "description": "Max results (default 20, max 100)."}
				},
				"required": ["q"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "Brain Search",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__write_note",
			Description: "Write a free-form Markdown note into the brain (the Notion-like " +
				"capture path for non-technical knowledge). Persists through the canonical " +
				"outbound Serializer: a .md file is written + indexed + autocommitted, identical " +
				"to a note authored in VSCode. Pick a short descriptive name (slug). workspace " +
				"'global' (default) makes the note workspace-agnostic. Set pinned=true to keep the " +
				"consolidator from auto-pruning it. Returns the saved note's id. On a concurrent " +
				"on-disk edit the write conflicts and is diverted to a .conflict sidecar — retry " +
				"after reconciling.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Short slug for the note. Required."},
					"content": {"type": "string", "description": "Markdown body. Required."},
					"workspace": {"type": "string", "description": "Workspace id ('global' for workspace-agnostic). Default 'global'."},
					"tags": {"type": "array", "items": {"type": "string"}, "description": "Optional free-form tags."},
					"pinned": {"type": "boolean", "description": "Pin to prevent auto-consolidation. Default false."}
				},
				"required": ["name", "content"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Brain Write Note",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name: "brain__list_people",
			Description: "List CRM people records, newest first. People are " +
				"workspace-scoped contacts in the brain (name, company, email, role, " +
				"phone, tags, notes + entity links). Defaults to the CRM workspace; " +
				"pass workspace to list another workspace you can read. Use before " +
				"brain__get_person to discover ids.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace": {"type": "string", "description": "Workspace id or name. Default 'crm'."},
					"limit": {"type": "integer", "description": "Max results (default 100, max 1000)."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "List People",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__get_person",
			Description: "Fetch one CRM person by id, including the verbatim on-disk " +
				".md (notes body) + entity links. Use after brain__list_people to pull " +
				"the full record.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Person id (ULID). Required."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:        "Get Person",
				ReadOnlyHint: boolPtr(true),
			}),
		},
		{
			Name: "brain__write_person",
			Description: "Write or update a CRM person. Pass id to update an existing " +
				"record; omit it to create. name is required and must be unique within " +
				"the workspace. Defaults to the CRM workspace; pass workspace to write " +
				"another workspace you can write. Persists through the canonical outbound " +
				"Serializer: a workspaces/<workspace>/crm/people/<name>.md file is written " +
				"+ indexed + autocommitted. entities links the person to an org/deal/task/etc. " +
				"On a concurrent on-disk edit the write conflicts and is diverted to a " +
				".conflict sidecar — retry after reconciling.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace": {"type": "string", "description": "Workspace id or name. Default 'crm'; omitted updates keep the existing workspace."},
					"name": {"type": "string", "description": "Full name — unique within the workspace. Required."},
					"email": {"type": "string", "description": "Email address."},
					"phone": {"type": "string", "description": "Phone number."},
					"company": {"type": "string", "description": "Company / organisation."},
					"role": {"type": "string", "description": "Job title / role."},
					"notes": {"type": "string", "description": "Free-form markdown notes."},
					"tags": {"type": "array", "items": {"type": "string"}, "description": "Optional free-form tags."},
					"pinned": {"type": "boolean", "description": "Pin to prevent auto-consolidation. Default false."},
					"entities": {
						"type": "array",
						"description": "Links to other entities (org, deal, task, ...).",
						"items": {
							"type": "object",
							"properties": {
								"kind": {"type": "string"},
								"id": {"type": "string"},
								"role": {"type": "string"}
							},
							"required": ["kind", "id"]
						}
					},
					"id": {"type": "string", "description": "Existing person id to update. Omit to create."},
					"if_hash": {"type": "string", "description": "CAS token (last on-disk hash) for conflict-safe updates."}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Write Person",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name: "brain__delete_person",
			Description: "Delete a CRM person by id: soft-deletes the indexed record and " +
				"removes its workspaces/<workspace>/crm/people/<name>.md file " +
				"(autocommitted). Idempotent — a missing record is not an error.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Person id to delete. Required."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Delete Person",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
	return tools
}
