package gateway

import "encoding/json"

// skillRefinementToolDefinitions returns the universal mcpx + skill__*
// tools for the W3 refinement loop. They're the agent's only legitimate
// channel for "this skill could be better" feedback — proposals NEVER
// mutate SKILL.md directly, even when quorum hits.
//
// Sits alongside skillRegistryToolDefinitions() so the catalog stays
// organised: registry = read/write the skills themselves; refinement
// = the proposal pipeline that gates changes.
func skillRefinementToolDefinitions() []Tool {
	return []Tool{
		{
			Name: "skill__propose_refinement",
			Description: "Submit a refinement proposal for a registry skill you just used. " +
				"Pass `skill` (name; the version is auto-resolved from the latest registry " +
				"entry the session can see), `friction` (what was annoying or what failed " +
				"— concrete, e.g. \"ffmpeg fails on h.265 + audio dropped\"), " +
				"`suggested_change` (proposed prose diff or rewrite — keep it small + " +
				"actionable), and `rationale` (one line: why this change). " +
				"Proposals are NEVER applied automatically. The mesh-quorum aggregator " +
				"counts similar proposals (same skill + fuzzy friction match); when the " +
				"count crosses the threshold (default 3), the freshest one transitions to " +
				"`candidate` and emits a mesh finding. " +
				"Once candidate (or promoted via dashboard), call skill__adopt_refinement with the proposal_id to publish the suggested_change as a new registry version (records parent_version lineage). " +
				"Returns {proposal_id, status, quorum_count}. Proposals are retained for review and adoption.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill": {
						"type": "string",
						"description": "Skill name (lowercase + hyphens). Must match a name in the registry."
					},
					"friction": {
						"type": "string",
						"description": "What was annoying / what failed. Be specific — \"step 3 says X but does Y\" is far more useful than \"could be better\". The fuzzy-match aggregator uses the first ~50 chars as a grouping key."
					},
					"suggested_change": {
						"type": "string",
						"description": "Proposed diff or rewrite. Prose is fine; keep it small + actionable."
					},
					"rationale": {
						"type": "string",
						"description": "One line: why this change. Future reviewers see this before the suggested_change body."
					}
				},
				"required": ["skill", "friction", "suggested_change", "rationale"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Propose Skill Refinement",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name: "skill__adopt_refinement",
			Description: "Adopt a candidate (or dashboard-promoted) refinement proposal into the registry as a new version. " +
				"Pass `proposal_id`. The proposal's suggested_change becomes the body of the new version; parent_version is set to the proposal's pinned skill_version for lineage. " +
				"Requires workspace write scope. On success the proposal is marked applied and the new version is returned. " +
				"This is the agent-callable half of the refinement loop (propose → quorum candidate → adopt → published version). Dashboard 'promote' records a human decision only; adopt performs the publish step.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"proposal_id": {
						"type": "string",
						"description": "ID of a candidate or promoted refinement proposal to turn into a new registry version."
					}
				},
				"required": ["proposal_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Adopt Skill Refinement",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
