package gateway

import (
	"encoding/json"
)

// skillRegistryToolDefinitions returns the universal mcpx__skill_*
// tools — visible from every CWD, not admin-gated. They're the agent's
// front door to the skills registry: ask in natural language, fetch
// the full body, contribute new versions, browse the catalog.
//
// Admin operations (delete, set @stable tag) live in internal/control
// behind the mcplexer__ namespace and are CWD-gated automatically.
func skillRegistryToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "mcpx__skill_search",
			Description: "Search the mcplexer skills registry by intent. Pass a natural-language description of what you're trying to do (\"I need to extract text from a PDF\") and get the top-K matching skills back, ranked by relevance. Returns {query, count, hits:[{name, version, description, score, scope}], hint}. In code mode read result.hits — never Object.keys on a raw string. Then call mcpx__skill_get to fetch the full SKILL.md body.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "Natural-language description of what you're trying to accomplish."
					},
					"limit": {
						"type": "integer",
						"description": "Max results to return (default 5, max 20)."
					}
				},
				"required": ["query"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Search Skills",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_get",
			Description: "Fetch the full SKILL.md body of one skill from the registry. Pass a name (and optional version: an integer, \"latest\", or \"stable\"). Returns {name, version, author, body, bundle_sha256?, bundle_b64?} — read result.body for the verbatim markdown (frontmatter + instructions). Use mcpx__skill_search first to discover skills by intent. When include_bundle=true and the skill was published with a tar.gz bundle (scripts/, reference/, etc.), bundle_b64 carries the tar.gz bytes — use mcpx__skill_install to extract it.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Skill name (lowercase + hyphens)."
					},
					"version": {
						"description": "Version: positive integer, \"latest\" (default), or \"stable\".",
						"oneOf": [
							{"type": "string"},
							{"type": "integer"}
						]
					},
					"include_bundle": {
						"type": "boolean",
						"description": "When true and a bundle is attached, return its tar.gz bytes as bundle_b64 (≤25 MiB). Default false to keep responses small."
					}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Get Skill",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_install",
			Description: "Extract a bundled skill from the registry onto the local disk. Pass name + optional version + optional dest directory (defaults to ~/.claude/skills/<name>/). The bundle's tar.gz is unpacked under dest, preserving the original directory layout (SKILL.md + scripts/ + reference/). Refuses to overwrite an existing directory unless overwrite=true. Returns the resolved dest path and the list of extracted files. Errors when the skill has no bundle attached — text-only skills don't need installing.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Skill name."},
					"version": {
						"description": "Version: positive integer, \"latest\" (default), or \"stable\".",
						"oneOf": [{"type": "string"}, {"type": "integer"}]
					},
					"dest": {
						"type": "string",
						"description": "Destination directory. Defaults to ~/.claude/skills/<name>/. Absolute or ~-expandable paths only."
					},
					"overwrite": {
						"type": "boolean",
						"description": "Replace dest if it already exists. Default false."
					}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Install Skill Bundle",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_export",
			Description: "Export one registry skill as a signed/versioned sync package for hub push or peer transfer. Returns JSON containing the SKILL.md body, metadata, provenance, deterministic sha256 signature, and optional tar.gz bundle payload when include_bundle=true. The local registry remains canonical; this is a read-only package creation step.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Skill name."},
					"version": {
						"description": "Version: positive integer, \"latest\" (default), or \"stable\".",
						"oneOf": [{"type": "string"}, {"type": "integer"}]
					},
					"include_bundle": {
						"type": "boolean",
						"description": "Include the base64 tar.gz bundle payload when present. Default false."
					}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Export Skill Package",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_import",
			Description: "Dry-run or commit a signed skill sync package into the local registry. By default this is a dry-run/diff and does not mutate state. Pass commit=true to approve the pull and publish a new local registry version with provenance metadata. Package can be an object or package_json string from mcpx__skill_export.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"package": {"type": "object", "description": "Sync package object returned by mcpx__skill_export."},
					"package_json": {"type": "string", "description": "Sync package JSON returned by mcpx__skill_export."},
					"dry_run": {"type": "boolean", "description": "Force dry-run. Default true unless commit=true."},
					"commit": {"type": "boolean", "description": "Explicit approval gate. Must be true before local registry mutation."},
					"scope": {
						"type": "string",
						"enum": ["auto", "workspace", "global"],
						"description": "Import target scope. \"auto\" defaults to current workspace if available, else global."
					},
					"author_hint": {"type": "string", "description": "Optional author/provenance hint for the local version."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Import Skill Package",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_publish",
			Description: "Publish a SKILL.md to the registry. Easiest local-disk path: pass source_path pointing at a local SKILL.md file or skill directory; relative paths resolve from the current client root, and directories are bundled with sidecar files automatically. For inline publishing, pass body or body_b64 (base64 avoids fragile JS string escaping). The SKILL.md MUST include YAML frontmatter with `name` and `description` (≤1024 chars, lead with \"Use when…\"); optional name argument, when provided, must match frontmatter. Idempotent on content hash — re-publishing identical content returns action=\"deduped\". Editing and re-publishing creates v2, v3, etc. Use parent_version to record edit lineage. Skills can be pinned to the current workspace or published globally — pass scope=\"workspace\" or scope=\"global\" (default \"auto\" picks workspace when the session has one, else global). For manually bundled sidecars, pass bundle_b64 — a base64-encoded tar.gz of the skill directory (≤25 MiB). The publisher verifies bundled SKILL.md matches body.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Optional skill name safety check; when provided, must match the frontmatter name field."
					},
					"body": {
						"type": "string",
						"description": "Full SKILL.md content including frontmatter (≤64 KB). Use body_b64 or source_path to avoid fragile JS escaping."
					},
					"body_b64": {
						"type": "string",
						"description": "Optional base64-encoded SKILL.md body. Mutually exclusive with body and source_path."
					},
					"parent_version": {
						"type": "integer",
						"description": "Optional: which prior version this edit was based on (for diff/lineage)."
					},
					"author_hint": {
						"type": "string",
						"description": "Optional: free-form identity hint shown in the registry (e.g. \"orchestrator-agent\")."
					},
					"scope": {
						"type": "string",
						"enum": ["auto", "workspace", "global"],
						"description": "Visibility scope. \"workspace\" pins to the current workspace; \"global\" makes it visible everywhere; \"auto\" (default) picks workspace if the session is in one, else global."
					},
					"bundle_b64": {
						"type": "string",
						"description": "Optional: base64-encoded tar.gz of the full skill directory (SKILL.md + scripts/ + reference/). Max 25 MiB after decode. The SKILL.md inside must equal body verbatim. Mutually exclusive with source_path."
					},
					"source_path": {
						"type": "string",
						"description": "Optional local path to a SKILL.md file or skill directory. Relative paths resolve from the current client root. Directory inputs attach a bundle automatically; file inputs publish text only. Mutually exclusive with body, body_b64, and bundle_b64."
					}
				},
				"anyOf": [
					{"required": ["body"]},
					{"required": ["body_b64"]},
					{"required": ["source_path"]}
				]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Publish Skill",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_list",
			Description: "List skills in the registry. With no arguments: returns the head version of every skill, ordered by name. With name: returns the version history of that skill in descending version order, including author and published_at for each revision. Useful for browsing the catalog or finding rollback targets.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Optional: list version history of one skill instead of all heads."
					},
					"limit": {
						"type": "integer",
						"description": "Optional cap on rows returned (default unlimited)."
					}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Skills",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_inventory",
			Description: "Browse the full local skill inventory across all sources: the registry, local ~/.claude/skills directories, and workspace-local .claude/skills. Returns normalized rows with name, description, version, source kind (registry/local-dir), scope, managed status, bundle metadata, and parse errors. Supports lexical search over name and description. Use this to see every skill the machine knows about, including unmanaged local dirs not yet imported into the registry.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"q": {
						"type": "string",
						"description": "Optional: lexical search filter over skill names and descriptions."
					},
					"limit": {
						"type": "integer",
						"description": "Max rows (default 50, max 200)."
					},
					"source_dirs": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Optional: additional local directories to scan for SKILL.md files."
					}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Skill Inventory",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_diff",
			Description: "Diff two versions of a skill from the registry. Returns unified body/frontmatter diffs plus a bundle file tree (added/modified/removed) when both versions have tar.gz bundles attached. Defaults: old_version=1, new_version=latest. Use mcpx__skill_list(name) first to see revision history and authorship.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Skill name."
					},
					"old_version": {
						"description": "Baseline version: positive integer or \"latest\". Default 1.",
						"oneOf": [{"type": "string"}, {"type": "integer"}]
					},
					"new_version": {
						"description": "Target version: positive integer or \"latest\" (default).",
						"oneOf": [{"type": "string"}, {"type": "integer"}]
					}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Diff Skill Versions",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__skill_push",
			Description: "Export a skill from the registry with signed/versioned metadata for hub sync. Returns the full skill (body + optional bundle) along with metadata (name, version, content_hash, description, author, bundle_sha256, published_at, source_type). Call this before mesh__skill_hub_pull to get the package to send to a hub peer.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Skill name to export."
					},
					"version": {
						"description": "Version: positive integer, \"latest\" (default), or \"stable\".",
						"oneOf": [{"type": "string"}, {"type": "integer"}]
					},
					"include_bundle": {
						"type": "boolean",
						"description": "Include the tar.gz bundle when present. Default true."
					}
				},
				"required": ["name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Push Skill to Hub",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "mcpx__skill_pull",
			Description: "Import a skill from a hub peer into the local registry. dry_run=true returns a diff without mutating state — shows whether the skill is new, an update, or a conflict. When dry_run=false, the skill is published and returns the action (created/deduped). Requires explicit approval before writing — agents must call with dry_run=true first.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {
						"type": "string",
						"description": "Skill name to import."
					},
					"version": {
						"type": "integer",
						"description": "Version to pull. 0 = latest."
					},
					"dry_run": {
						"type": "boolean",
						"description": "Preview the import without writing. Default true."
					},
					"body": {
						"type": "string",
						"description": "SKILL.md body from the hub peer."
					},
					"bundle_b64": {
						"type": "string",
						"description": "Optional: base64-encoded tar.gz bundle."
					},
					"content_hash": {
						"type": "string",
						"description": "Content hash from the hub peer for dedup."
					}
				},
				"required": ["name", "body"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Pull Skill from Hub",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			}),
		},
	}
}
