package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// wireAutoReloadHook installs the callback that fires once per
// automatic downstream reload. Emits an audit row (tool_name =
// "auto_reload", actor_kind = "system") so the dashboard surfaces the
// event, and — when mesh is up — broadcasts a priority=high alert so
// the operator sees "auto-recovered Linear" in the signal tray.
//
// The hook is a thin wrapper around audit/mesh; the manager doesn't
// import either, which keeps the dependency direction one-way (cmd ->
// internal). Failures inside the hook are logged, never returned: a
// dropped audit row must not block recovery itself.
func wireAutoReloadHook(
	mgr *downstream.Manager,
	dsStore store.DownstreamServerStore,
	auditor *audit.Logger,
	meshMgr *mesh.Manager,
) {
	if mgr == nil {
		return
	}
	mgr.SetAutoReloadHook(func(serverID string, snap downstream.ServerHealth) {
		// Resolve a friendly name for the alert text. Fall back to
		// the ID when the store lookup fails.
		serverName := serverID
		if dsStore != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if srv, err := dsStore.GetDownstreamServer(ctx, serverID); err == nil && srv != nil && srv.Name != "" {
				serverName = srv.Name
			}
			cancel()
		}

		emitAutoReloadAudit(auditor, serverID, serverName, snap)
		emitAutoReloadMeshAlert(meshMgr, serverName, snap)

		slog.Info("downstream auto-reloaded",
			"server_id", serverID,
			"server_name", serverName,
			"auto_reloads_24h", snap.AutoReloads24h,
			"last_failure_reason", snap.LastFailureReason,
		)
	})
}

// emitAutoReloadAudit writes the structured audit row for an automatic
// reload. tool_name is the stable key the dashboard / query_audit can
// filter on; the params blob carries the health snapshot so reviewers
// can see why the daemon decided the server was stuck.
func emitAutoReloadAudit(auditor *audit.Logger, serverID, serverName string, snap downstream.ServerHealth) {
	if auditor == nil {
		return
	}
	params := map[string]any{
		"server_id":            serverID,
		"server_name":          serverName,
		"consecutive_failures": snap.ConsecutiveFailures,
		"last_failure_reason":  snap.LastFailureReason,
		"auto_reloads_24h":     snap.AutoReloads24h,
	}
	paramsJSON, _ := json.Marshal(params)
	rec := &store.AuditRecord{
		ID:                 uuid.NewString(),
		Timestamp:          time.Now().UTC(),
		ToolName:           "auto_reload",
		DownstreamServerID: serverID,
		ParamsRedacted:     paramsJSON,
		Status:             "success",
		ActorKind:          "system",
		ActorID:            "downstream-stuck-detector",
		ClientType:         "system",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := auditor.Record(ctx, rec); err != nil {
		slog.Warn("audit auto_reload record failed", "error", err, "server", serverID)
	}
}

// meshSystemWorkspace is the reserved workspace id daemon-internal
// alerts (auto-reload recovery, etc.) are filed under. It is deliberately
// NOT the global namespace (""): the global namespace is read by every
// session on the daemon, so filing system health noise there floods every
// unrelated agent's context on each mesh__receive — the cross-workspace
// chatter that motivated this scoping. A reserved, non-empty id keeps the
// alert out of agent inboxes while the operator dashboard (whose mesh feed
// is workspace-agnostic — see api.meshHandler.status) and the audit row
// emitted alongside still surface every recovery.
const meshSystemWorkspace = "system:gateway"

// emitAutoReloadMeshAlert files a single priority=high mesh alert per
// reload under the reserved system workspace so the operator dashboard
// surfaces the recovery without flooding agent sessions. Best-effort: a
// transient mesh failure must not block the reload itself.
func emitAutoReloadMeshAlert(meshMgr *mesh.Manager, serverName string, snap downstream.ServerHealth) {
	if meshMgr == nil {
		return
	}
	content := fmt.Sprintf(
		"auto-recovered downstream %q after %d consecutive failures: %s",
		serverName, snap.ConsecutiveFailures, snap.LastFailureReason,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := meshMgr.Send(ctx, mesh.SessionMeta{
		SessionID:  "downstream-stuck-detector",
		ClientType: "system",
	}, mesh.SendRequest{
		Kind:        "alert",
		Priority:    "high",
		Content:     content,
		Tags:        "auto_reload,downstream",
		ActorKind:   "system",
		ToWorkspace: meshSystemWorkspace,
	})
	if err != nil {
		slog.Warn("mesh auto_reload alert failed", "error", err, "server", serverName)
	}
}
