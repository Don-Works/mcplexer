package gateway

import "encoding/json"

// skillShareToolDefinitions returns the built-in MCP tools for the M2.7
// peer-to-peer skill share mesh. Each tool routes through the standard
// install pipeline (signature verify + capability review + 100 MB cap),
// so receiving a shared skill carries the same security guarantees as a
// registry install.
func skillShareToolDefinitions() []Tool {
	return []Tool{offerSkillToolDef(), requestSkillToolDef()}
}

// offerSkillToolDef defines the mesh__offer_skill tool. Agents call this
// to push a SkillOffer over the /mcplexer/skill/1.0.0 libp2p protocol.
func offerSkillToolDef() Tool {
	return Tool{
		Name: "mesh__offer_skill",
		Description: "Offer one of your installed legacy .mcskill bundle skills (executable chain, see skills-coherence task) to a paired peer. " +
			"Sends a SkillOffer message (name, version, signer, manifest, " +
			"sha256, size) over the /mcplexer/skill/1.0.0 libp2p protocol. " +
			"The receiving peer's user/agent decides whether to call back " +
			"with mesh__request_skill. Requires the peer to be paired and " +
			"both sides to have the p2p build tag. " +
			"NOTE: this is separate from the skill registry (mcpx__skill_*); OfferSkill looks up installed_skills, not registry entries.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"peer_id": {
					"type": "string",
					"description": "The libp2p peer ID of the paired peer to offer the skill to."
				},
				"skill_name": {
					"type": "string",
					"description": "The machine-readable name of an installed skill (matches the manifest name field)."
				}
			},
			"required": ["peer_id", "skill_name"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Offer Skill to Paired Peer",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

// requestSkillToolDef defines the mesh__request_skill tool. Calls block
// until install completes or the user declines — agents `await` naturally.
func requestSkillToolDef() Tool {
	return Tool{
		Name: "mesh__request_skill",
		Description: "Request a skill bundle from a paired peer over the " +
			"/mcplexer/skill/1.0.0 libp2p protocol. The bundle is fetched, " +
			"its signature is verified, the capability review runs (same as " +
			"a registry install), and the skill is installed on success. " +
			"Blocks until install completes or the user declines.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"peer_id": {
					"type": "string",
					"description": "The libp2p peer ID of the paired peer to fetch from."
				},
				"skill_name": {
					"type": "string",
					"description": "The machine-readable name of the skill to fetch."
				},
				"version": {
					"type": "string",
					"description": "Optional specific version (semver). Empty for the latest the peer offers."
				}
			},
			"required": ["peer_id", "skill_name"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Request Skill from Paired Peer",
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}
