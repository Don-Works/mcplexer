package mesh

import (
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// buildAuditRecord assembles one worker_trigger.mesh audit row. decision
// is one of "fired" | "throttled" | "denied" | "loop_guard"; reason
// gives a short slug for downstream filtering ("peer_missing_trigger_scope",
// "chain_depth_exceeded", etc.).
//
// The record format mirrors the runner's worker_run.* emissions: actor
// is the worker, params carry the structured decision context.
func buildAuditRecord(
	t *store.WorkerMeshTrigger, msg *store.MeshMessage,
	srcPeer string, depth int, decision, reason string, now time.Time,
) *store.AuditRecord {
	params := map[string]any{
		"trigger_id":        t.ID,
		"worker_id":         t.WorkerID,
		"msg_id":            msg.ID,
		"msg_kind":          msg.Kind,
		"msg_tags":          msg.Tags,
		"msg_audience":      msg.Audience,
		"source_peer":       srcPeer,
		"source_agent_name": msg.AgentName,
		"chain_depth":       depth,
		"decision":          decision,
	}
	if reason != "" {
		params["reason"] = reason
	}
	raw, _ := json.Marshal(params)
	status := "success"
	if decision == "denied" || decision == "loop_guard" {
		status = "blocked"
	}
	return &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "worker_trigger.mesh",
		Status:         status,
		ParamsRedacted: raw,
		ActorKind:      "worker",
		ActorID:        t.WorkerID,
		CreatedAt:      now,
	}
}
