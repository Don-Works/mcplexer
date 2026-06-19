package gateway

import "encoding/json"

// hubSyncToolDefinitions returns the mesh tool definitions for the p2p
// hub sync surface. These tools let agents pull a skill-registry index
// from a designated hub peer and selectively fetch missing entries.
func hubSyncToolDefinitions() []Tool {
	return []Tool{hubIndexToolDef(), hubSearchToolDef(), hubPullToolDef()}
}

func hubIndexToolDef() Tool {
	return Tool{
		Name: "mesh__skill_hub_index",
		Description: "Pull the skill-registry index from a paired hub peer. " +
			"Returns a list of (name, version, content_hash, description) entries " +
			"the hub is willing to share. Compare content hashes against your local " +
			"registry to determine what to pull. Requires the peer to be paired and " +
			"the mesh.registry_request scope to be granted on both sides.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"peer_id": {
					"type": "string",
					"description": "The libp2p peer ID of the hub peer."
				}
			},
			"required": ["peer_id"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Pull Skill Hub Index",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

func hubSearchToolDef() Tool {
	return Tool{
		Name: "mesh__skill_hub_search",
		Description: "Search a paired hub peer's skill registry by natural-language intent. " +
			"Returns ranked metadata-only hits with name, version, score, content hash, " +
			"and description; it does not fetch skill bodies or bundles. Use " +
			"mesh__skill_hub_pull afterward to import a selected skill locally. " +
			"Requires the peer to be paired and the mesh.registry_request scope to " +
			"be granted on both sides.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"peer_id": {
					"type": "string",
					"description": "The libp2p peer ID or paired peer name of the hub peer."
				},
				"query": {
					"type": "string",
					"description": "Natural-language description of what you want the remote skill to do."
				},
				"q": {
					"type": "string",
					"description": "Alias for query."
				},
				"limit": {
					"type": "integer",
					"description": "Max hits to return (default 5, max 20)."
				}
			},
			"required": ["peer_id"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Search Skill Hub",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

func hubPullToolDef() Tool {
	return Tool{
		Name: "mesh__skill_hub_pull",
		Description: "Pull a specific skill entry from a paired hub peer's " +
			"registry. The entry's body (SKILL.md) and optional bundle are " +
			"fetched over /mcplexer/skill-registry/1.0.0 and published into " +
			"the local registry. Content-hash dedup prevents duplicate imports. " +
			"Call mesh__skill_hub_index first to discover available entries.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"peer_id": {
					"type": "string",
					"description": "The libp2p peer ID of the hub peer."
				},
				"name": {
					"type": "string",
					"description": "Skill name to pull from the hub."
				},
				"version": {
					"type": "integer",
					"description": "Specific version to pull. 0 = latest."
				}
			},
			"required": ["peer_id", "name"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Pull Skill from Hub",
			ReadOnlyHint:    boolPtr(false),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}
