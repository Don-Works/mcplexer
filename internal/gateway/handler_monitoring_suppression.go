// handler_monitoring_suppression.go — the operator's view of, and undo button
// for, everything task resolution has muted.
//
// Requirement this satisfies: a suppression that cannot be enumerated is
// indistinguishable from a bug, and a suppression that cannot be undone is a
// permanently silenced alert. Both tools below are deterministic reads/writes
// over migration 147's receipt table; neither invokes a model.
package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// monitoringResolutions returns the resolution-feedback store, or nil on a
// daemon whose store predates migration 147.
func (h *handler) monitoringResolutions() store.MonitoringResolutionStore {
	if h.store == nil {
		return nil
	}
	resolutions, _ := h.store.(store.MonitoringResolutionStore)
	return resolutions
}

func (h *handler) handleMonitoringSuppressions(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		IncludeCleared bool   `json:"include_cleared"`
		Limit          int    `json:"limit"`
		WorkspaceID    string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	resolutions := h.monitoringResolutions()
	if resolutions == nil {
		return marshalErrorResult("monitoring resolution feedback is not available on this daemon")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc)
	}
	rows, err := resolutions.ListMonitoringResolutions(ctx, wsID, !args.IncludeCleared, args.Limit)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	suppressed := 0
	for _, r := range rows {
		if r.Suppressing() {
			suppressed++
		}
		entry := map[string]any{
			"incident_id": r.IncidentID, "task_id": r.TaskID,
			"class_key": r.ClassKey, "title": r.IncidentTitle,
			"outcome": r.Outcome, "suppressing": r.Suppressing(),
			"severity": r.Severity, "disposition": r.Disposition,
			"resolved_as": r.StatusText, "resolved_at": r.ResolvedAt,
			"resolved_by_session": r.ResolvedBySession,
			"resolved_by_actor":   r.ResolvedByActor,
			"acked_templates":     r.AckedTemplateIDs,
			"task_status":         r.TaskStatus, "task_closed": r.TaskClosed,
		}
		if !r.IncidentLastSeen.IsZero() {
			entry["incident_last_seen"] = r.IncidentLastSeen
		}
		if r.ClearedAt != nil {
			entry["cleared_at"] = r.ClearedAt
			entry["cleared_reason"] = r.ClearedReason
		}
		// A live suppression whose canonical task is no longer closed is
		// inconsistent: somebody reopened the task by a path that did not run
		// the feedback hook. Surface it rather than letting it mute silently.
		if r.Suppressing() && !r.TaskClosed {
			entry["stale"] = true
			entry["stale_reason"] = "canonical task is open but the suppression is still live; run monitoring__unsuppress"
		}
		out = append(out, entry)
	}
	return monitoringJSON(map[string]any{
		"workspace_id": wsID, "count": len(out),
		"suppressing": suppressed, "resolutions": out,
	})
}

func (h *handler) handleMonitoringUnsuppress(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		IncidentID  string `json:"incident_id"`
		Reason      string `json:"reason"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	args.IncidentID = strings.TrimSpace(args.IncidentID)
	if args.IncidentID == "" {
		return marshalErrorResult("incident_id is required")
	}
	resolutions := h.monitoringResolutions()
	if resolutions == nil {
		return marshalErrorResult("monitoring resolution feedback is not available on this daemon")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		reason = store.MonitoringClearReasonManual
	}
	row, err := resolutions.ClearMonitoringResolution(ctx, wsID, args.IncidentID, reason, h.monitoringSessionID())
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	if row == nil {
		// Idempotent: nothing was suppressed, which is the desired end state.
		return monitoringJSON(map[string]any{
			"cleared": false, "incident_id": args.IncidentID,
			"detail": "no live resolution for this incident in this workspace",
		})
	}
	return monitoringJSON(map[string]any{
		"cleared": true, "incident_id": row.IncidentID, "task_id": row.TaskID,
		"outcome": row.Outcome, "reason": reason,
		"restored_disposition": row.DispositionBefore,
		"unacked_templates":    row.AckedTemplateIDs,
	})
}
