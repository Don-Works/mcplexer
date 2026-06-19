// worker_triggers_handler.go (M4) exposes the per-Worker mesh-trigger
// CRUD + per-peer trigger-grant convenience over HTTP so the PWA hits
// the same admin.Service code path the MCP tools use.
package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// workerTriggersHandler binds the admin service. svc is required; the
// router only registers routes when it's non-nil.
type workerTriggersHandler struct {
	svc *workersadmin.Service
}

// listMeshTriggers serves GET /api/v1/workers/{worker_id}/mesh-triggers.
func (h *workerTriggersHandler) listMeshTriggers(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("worker_id")
	if workerID == "" {
		writeError(w, http.StatusBadRequest, "worker_id is required")
		return
	}
	rows, err := h.svc.ListMeshTriggers(r.Context(), workerID)
	if err != nil {
		writeTriggerErr(w, err, "failed to list mesh triggers")
		return
	}
	if rows == nil {
		rows = []*store.WorkerMeshTrigger{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// createMeshTrigger serves POST /api/v1/workers/{worker_id}/mesh-triggers.
// The path's worker_id wins over the body's worker_id so the dashboard
// can't accidentally route a create at the wrong worker.
func (h *workerTriggersHandler) createMeshTrigger(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("worker_id")
	if workerID == "" {
		writeError(w, http.StatusBadRequest, "worker_id is required")
		return
	}
	var in workersadmin.MeshTriggerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.WorkerID = workerID
	t, err := h.svc.CreateMeshTrigger(r.Context(), in)
	if err != nil {
		writeTriggerErr(w, err, "failed to create mesh trigger")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// updateMeshTrigger serves PATCH /api/v1/workers/{worker_id}/mesh-triggers/{trigger_id}.
func (h *workerTriggersHandler) updateMeshTrigger(w http.ResponseWriter, r *http.Request) {
	triggerID := r.PathValue("trigger_id")
	if triggerID == "" {
		writeError(w, http.StatusBadRequest, "trigger_id is required")
		return
	}
	var in workersadmin.MeshTriggerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.ID = triggerID
	t, err := h.svc.UpdateMeshTrigger(r.Context(), in)
	if err != nil {
		writeTriggerErr(w, err, "failed to update mesh trigger")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// deleteMeshTrigger serves DELETE /api/v1/workers/{worker_id}/mesh-triggers/{trigger_id}.
func (h *workerTriggersHandler) deleteMeshTrigger(w http.ResponseWriter, r *http.Request) {
	triggerID := r.PathValue("trigger_id")
	if triggerID == "" {
		writeError(w, http.StatusBadRequest, "trigger_id is required")
		return
	}
	if err := h.svc.DeleteMeshTrigger(r.Context(), triggerID); err != nil {
		writeTriggerErr(w, err, "failed to delete mesh trigger")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// grantTriggerToPeer serves POST /api/v1/peers/{peer_id}/trigger-grants.
// Body: {"worker_name": "<name|*>"}.
func (h *workerTriggersHandler) grantTriggerToPeer(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("peer_id")
	if peerID == "" {
		writeError(w, http.StatusBadRequest, "peer_id is required")
		return
	}
	var body struct {
		WorkerName string `json:"worker_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	scope, err := h.svc.GrantTriggerToPeer(r.Context(), peerID, body.WorkerName)
	if err != nil {
		writeTriggerErr(w, err, "failed to grant trigger scope")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"peer_id": peerID,
		"scope":   scope,
	})
}

// revokeTriggerGrant serves
// DELETE /api/v1/peers/{peer_id}/trigger-grants/{worker_name}.
func (h *workerTriggersHandler) revokeTriggerGrant(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("peer_id")
	workerName := r.PathValue("worker_name")
	if peerID == "" || workerName == "" {
		writeError(w, http.StatusBadRequest, "peer_id + worker_name required")
		return
	}
	if _, err := h.svc.RevokeTriggerGrant(r.Context(), peerID, workerName); err != nil {
		writeTriggerErr(w, err, "failed to revoke trigger grant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeTriggerErr maps domain errors to clean HTTP responses.
func writeTriggerErr(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, store.ErrWorkerMeshTriggerNotFound):
		writeError(w, http.StatusNotFound, "mesh trigger not found")
	case errors.Is(err, store.ErrWorkerNotFound):
		writeError(w, http.StatusNotFound, "worker not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "peer not found")
	default:
		writeErrorDetail(w, http.StatusBadRequest, fallback, err.Error())
	}
}
