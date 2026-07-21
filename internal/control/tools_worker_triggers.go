// tools_worker_triggers.go (M4) — MCP tool defs for the per-Worker
// mesh-trigger surface and the per-peer trigger-grant convenience.
//
// All five tools are CWD-gated like every mcplexer__* tool; the
// dispatcher consumes the same store rows the admin service writes.
package control

import (
	"github.com/don-works/mcplexer/internal/gateway"
)

// listWorkerMeshTriggersToolDef declares mcplexer__list_worker_mesh_triggers.
// Returns every trigger row attached to one Worker, including disabled
// rows so the dashboard can render them with a toggle.
func listWorkerMeshTriggersToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_worker_mesh_triggers",
		Description: "List every mesh trigger configured for a worker. Returns the full trigger row including disabled rows. Use to audit how a worker fires off mesh events before adding more.",
		InputSchema: schema(props{
			"worker_id": propStr("Worker ID (required)."),
		}, []string{"worker_id"}),
	}
}

// createWorkerMeshTriggerToolDef declares
// mcplexer__create_worker_mesh_trigger. All match fields are AND'd;
// AllMessages=true is the explicit "any inbound message" escape.
func createWorkerMeshTriggerToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "create_worker_mesh_trigger",
		Description: "Create a mesh trigger for a worker. The worker fires when a matching mesh message arrives (kind/tags/audience/regex/from filter). throttle_seconds bounds re-fires per (trigger, source) pair. max_chain_depth is the reflexive-loop guard. all_messages=true skips the at-least-one-criterion check.",
		InputSchema: schema(props{
			"worker_id":         propStr("Worker ID (required)."),
			"kind_match":        propStr("Exact mesh kind: finding|task|alert|question|result|event|reply. Empty = any."),
			"tag_match":         propStr("Comma-separated tag set; ANY overlap admits. Empty = any."),
			"audience_match":    propStr("Exact audience: '*', session-id, or role. Empty = any."),
			"content_regex":     propStr("Go regexp matched against MeshMessage.Content. Empty = any."),
			"status_from_match": propStr("AND'd transition filter: fire only when a task_event:status_changed message transitioned FROM this exact status. Empty = any. Combine with status_to_match to gate on a specific transition (e.g. doing→review)."),
			"status_to_match":   propStr("AND'd transition filter: fire only when a task transitioned INTO this exact status (e.g. 'review'). Empty = any. Unlike tag_match (OR-semantics) this is AND'd, so it's how you express 'status_changed AND new status = review'."),
			"from_filters":      propArr("Optional array of {peer_id?,agent_name?,role?} objects. ANY match admits. peer_id='self' matches local-origin."),
			"throttle_seconds":  propInt("Minimum seconds between fires for the same (trigger, source). Default 60."),
			"max_chain_depth":   propInt("Reflexive-trigger ceiling: refuse when incoming chain-depth >= this. Default 3, max 10."),
			"enabled":           map[string]any{"type": "boolean", "description": "Default true."},
			"all_messages":      map[string]any{"type": "boolean", "description": "Explicit opt-in to a trigger with no match criteria — fires on every message."},
		}, []string{"worker_id"}),
	}
}

// updateWorkerMeshTriggerToolDef declares
// mcplexer__update_worker_mesh_trigger.
func updateWorkerMeshTriggerToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "update_worker_mesh_trigger",
		Description: "Update a mesh trigger. Empty match fields are treated as 'no change' so PATCH-style requests don't accidentally widen the trigger; set all_messages=true to clear every match criterion at once.",
		InputSchema: schema(props{
			"id":                propStr("Trigger ID (required)."),
			"kind_match":        propStr("New kind filter."),
			"tag_match":         propStr("New tag filter."),
			"audience_match":    propStr("New audience filter."),
			"content_regex":     propStr("New content regex."),
			"status_from_match": propStr("New status_from transition filter (AND'd). Empty = no change unless all_messages=true."),
			"status_to_match":   propStr("New status_to transition filter (AND'd) — e.g. 'review'. Empty = no change unless all_messages=true."),
			"from_filters":      propArr("New from-filter array."),
			"throttle_seconds":  propInt("New throttle window."),
			"max_chain_depth":   propInt("New chain-depth ceiling."),
			"enabled":           map[string]any{"type": "boolean", "description": "Toggle the trigger on/off."},
			"all_messages":      map[string]any{"type": "boolean", "description": "Clear every match criterion at once."},
		}, []string{"id"}),
	}
}

// deleteWorkerMeshTriggerToolDef declares
// mcplexer__delete_worker_mesh_trigger.
func deleteWorkerMeshTriggerToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "delete_worker_mesh_trigger",
		Description: "Delete a mesh trigger by id. Hard delete — the trigger immediately stops firing.",
		InputSchema: schema(props{
			"id": propStr("Trigger ID (required)."),
		}, []string{"id"}),
	}
}

// grantTriggerToPeerToolDef declares mcplexer__grant_trigger_to_peer.
// Convenience wrapper around GrantPeerScope so callers don't need to
// remember the "trigger_worker:<name>" scope string.
func grantTriggerToPeerToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "grant_trigger_to_peer",
		Description: "Grant a paired peer permission to trigger one of this daemon's workers via the mesh. worker_name='*' grants permission for ALL workers (wildcard). Persists a 'trigger_worker:<name>' scope on the peer.",
		InputSchema: schema(props{
			"peer_id":     propStr("libp2p peer ID (required)."),
			"worker_name": propStr("Worker name or '*' for wildcard (required)."),
		}, []string{"peer_id", "worker_name"}),
	}
}

// revokeTriggerGrantToolDef declares mcplexer__revoke_trigger_grant.
func revokeTriggerGrantToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "revoke_trigger_grant",
		Description: "Revoke a previously-granted trigger scope from a paired peer. Idempotent — revoking a non-existent grant returns success.",
		InputSchema: schema(props{
			"peer_id":     propStr("libp2p peer ID (required)."),
			"worker_name": propStr("Worker name or '*' (required)."),
		}, []string{"peer_id", "worker_name"}),
	}
}
