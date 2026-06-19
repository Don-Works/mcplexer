// handlers_worker_triggers.go (M4) — wires the mesh-trigger MCP tools
// into admin.Service. Same JSON-in / JSON-out shape as the rest of
// handlers_workers.go.
package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// handleListWorkerMeshTriggers serves mcplexer__list_worker_mesh_triggers.
func handleListWorkerMeshTriggers(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	rows, err := svc.ListMeshTriggers(ctx, in.WorkerID)
	if err != nil {
		return mapTriggerErr(err)
	}
	if rows == nil {
		rows = []*store.WorkerMeshTrigger{}
	}
	return mustJSONResult(map[string]any{"triggers": rows})
}

// handleCreateWorkerMeshTrigger serves
// mcplexer__create_worker_mesh_trigger.
func handleCreateWorkerMeshTrigger(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.MeshTriggerInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	t, err := svc.CreateMeshTrigger(ctx, in)
	if err != nil {
		return mapTriggerErr(err)
	}
	return mustJSONResult(t)
}

// handleUpdateWorkerMeshTrigger serves
// mcplexer__update_worker_mesh_trigger.
func handleUpdateWorkerMeshTrigger(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in admin.MeshTriggerInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	t, err := svc.UpdateMeshTrigger(ctx, in)
	if err != nil {
		return mapTriggerErr(err)
	}
	return mustJSONResult(t)
}

// handleDeleteWorkerMeshTrigger serves
// mcplexer__delete_worker_mesh_trigger.
func handleDeleteWorkerMeshTrigger(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if err := svc.DeleteMeshTrigger(ctx, in.ID); err != nil {
		return mapTriggerErr(err)
	}
	return mustJSONResult(map[string]bool{"deleted": true})
}

// handleGrantTriggerToPeer serves mcplexer__grant_trigger_to_peer.
func handleGrantTriggerToPeer(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		PeerID     string `json:"peer_id"`
		WorkerName string `json:"worker_name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	scope, err := svc.GrantTriggerToPeer(ctx, in.PeerID, in.WorkerName)
	if err != nil {
		return mapTriggerErr(err)
	}
	return mustJSONResult(map[string]string{
		"peer_id": in.PeerID,
		"scope":   scope,
	})
}

// handleRevokeTriggerGrant serves mcplexer__revoke_trigger_grant.
func handleRevokeTriggerGrant(
	ctx context.Context, svc *admin.Service, args json.RawMessage,
) json.RawMessage {
	var in struct {
		PeerID     string `json:"peer_id"`
		WorkerName string `json:"worker_name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	scope, err := svc.RevokeTriggerGrant(ctx, in.PeerID, in.WorkerName)
	if err != nil {
		return mapTriggerErr(err)
	}
	return mustJSONResult(map[string]string{
		"peer_id": in.PeerID,
		"scope":   scope,
	})
}

// mapTriggerErr maps store + admin sentinel errors to friendly results.
func mapTriggerErr(err error) json.RawMessage {
	switch {
	case errors.Is(err, store.ErrWorkerMeshTriggerNotFound):
		return errorResult("worker mesh trigger not found")
	case errors.Is(err, store.ErrWorkerNotFound):
		return errorResult("worker not found")
	case errors.Is(err, store.ErrNotFound):
		return errorResult("peer not found")
	}
	return errorResult(err.Error())
}
