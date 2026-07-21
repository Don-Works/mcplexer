package control

import "github.com/don-works/mcplexer/internal/gateway"

func skillRegistryAdminToolDefs() []gateway.Tool {
	return []gateway.Tool{
		{
			Name:        "publish_skill_registry",
			Description: "Publish an exact global or workspace-scoped SKILL.md, with optional bundle and source provenance, through registry validation and versioning. Omit workspace_id for global scope.",
			InputSchema: schema(props{
				"name":            propStr("Optional expected skill name; frontmatter remains authoritative"),
				"body":            propStr("SKILL.md text; mutually exclusive with body_b64"),
				"body_b64":        propStr("Base64 SKILL.md text; mutually exclusive with body"),
				"bundle_b64":      propStr("Optional base64 tar.gz containing the matching SKILL.md and assets (max 25 MiB decoded)"),
				"parent_version":  propInt("Optional parent version for edit lineage"),
				"author":          propStr("Author label (default: admin)"),
				"workspace_id":    propStr("Exact workspace ID; omit for true global scope"),
				"source_path":     propStr("Optional source path provenance"),
				"source_type":     propStr("Optional source type override (for example path, bundle, or git)"),
				"metadata_extras": propObj("Optional provenance metadata merged with parsed frontmatter"),
			}, nil),
		},
		{
			Name:        "audit_skill_registry",
			Description: "Run a deterministic read-only audit of all global and workspace skill heads for integrity, drift, duplication, and inert includes.",
			InputSchema: schema(props{
				"include_info": map[string]any{"type": "boolean", "description": "Include informational findings"},
				"max_issues":   propInt("Maximum findings returned (default 500, hard cap 2000)"),
			}, nil),
		},
	}
}
