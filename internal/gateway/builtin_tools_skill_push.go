package gateway

import "encoding/json"

// skillPushToolDefinitions returns the built-in MCP tools for the p2p
// registry-skill PUSH flow (outbox/inbox + accept/reject). Registered when
// registryShare is available (the accept step pulls the full skill over the
// /mcplexer/skill-registry/1.0.0 stream). Mirrors mesh__send_secret.
func skillPushToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "mesh__push_skill",
			Description: "Push a registry skill to a paired peer over the mesh. Unlike mesh__skill_hub_pull (which the RECEIVER initiates), this lets the SENDER offer a skill: it ships a metadata-only offer to the peer's inbox; the peer then calls `mesh__accept_skill` to pull the full SKILL.md + bundle from you over /mcplexer/skill-registry/1.0.0 and publish it locally, or `mesh__reject_skill` to discard. You must have granted the peer `mesh.registry_request` and stay reachable until they accept. Default expiry 7d, max 30d.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"to_peer": { "type": "string", "description": "Recipient: paired peer display name (e.g. 'shared-skills') or short ID. Resolved like to_peer in mesh__send." },
					"name":    { "type": "string", "description": "Registry skill name to push (call mcpx__skill_list to see names)." },
					"version": { "type": "integer", "description": "Specific version to offer. Omit or 0 for the latest local version." },
					"expires_in_seconds": { "type": "integer", "description": "Offer expiry. Default 604800 (7d), max 2592000 (30d)." }
				},
				"required": ["to_peer", "name"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Push Skill to Peer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "mesh__list_pending_skills",
			Description: "List pending skill-push offers awaiting accept/reject. Default direction is 'inbound' (offers others pushed to us); pass direction='outbound' to see skills you pushed that the peer has not yet decided on. Returns metadata only (name, version, peer, expiry). Use with `mesh__accept_skill` / `mesh__reject_skill`.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"direction": { "type": "string", "enum": ["inbound", "outbound"], "description": "Default 'inbound'." }
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Pending Skill Offers",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mesh__accept_skill",
			Description: "Accept a pending inbound skill-push offer: pulls the full SKILL.md + bundle from the offering peer over /mcplexer/skill-registry/1.0.0 and publishes it into the local registry as a new version. The sender must be reachable and have granted you `mesh.registry_request`. Marks the offer accepted.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"offer_id": { "type": "string", "description": "From mesh__list_pending_skills." }
				},
				"required": ["offer_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Accept Skill Offer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "mesh__reject_skill",
			Description: "Reject a pending inbound skill-push offer. Nothing is pulled or published; the offer row is kept for audit. Use when the offer is unwanted or was sent in error.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"offer_id": { "type": "string", "description": "From mesh__list_pending_skills." }
				},
				"required": ["offer_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Reject Skill Offer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
