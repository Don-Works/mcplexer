// handler_monitoring.go — dispatch for the workspace-scoped
// monitoring.* namespace. Reads go through distill.Query / the store;
// monitoring__notify goes through the escalate dispatcher (envelope +
// secret resolution + throttles all daemon-side).
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

func (h *handler) dispatchMonitoringTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.monitoringQry == nil {
		return marshalErrorResult("monitoring subsystem not enabled on this daemon"), nil, true
	}
	switch name {
	case "monitoring__hosts":
		return h.handleMonitoringList(ctx, raw, "hosts"), nil, true
	case "monitoring__sources":
		return h.handleMonitoringList(ctx, raw, "sources"), nil, true
	case "monitoring__channels":
		return h.handleMonitoringList(ctx, raw, "channels"), nil, true
	case "monitoring__stats":
		return h.handleMonitoringStats(ctx, raw), nil, true
	case "monitoring__digest":
		return h.handleMonitoringDigest(ctx, raw), nil, true
	case "monitoring__search":
		return h.handleMonitoringSearch(ctx, raw), nil, true
	case "monitoring__raw":
		return h.handleMonitoringRaw(ctx, raw), nil, true
	case "monitoring__ack":
		return h.handleMonitoringAck(ctx, raw), nil, true
	case "monitoring__commit_triage":
		return h.handleMonitoringCommitTriage(ctx, raw), nil, true
	case "monitoring__triage_effect":
		return h.handleMonitoringTriageEffect(ctx, raw), nil, true
	case "monitoring__notify":
		return h.handleMonitoringNotify(ctx, raw), nil, true
	case "monitoring__baselines":
		return h.handleMonitoringBaselines(ctx, raw), nil, true
	case "monitoring__suppressions":
		return h.handleMonitoringSuppressions(ctx, raw), nil, true
	case "monitoring__unsuppress":
		return h.handleMonitoringUnsuppress(ctx, raw), nil, true
	}
	return nil, nil, false
}

func monitoringJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return marshalErrorResult("marshal: " + err.Error())
	}
	return marshalToolResult(string(b))
}

func (h *handler) handleMonitoringList(ctx context.Context, raw json.RawMessage, what string) json.RawMessage {
	var args struct {
		WorkspaceID string `json:"workspace_id"`
	}
	_ = json.Unmarshal(raw, &args)
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	switch what {
	case "hosts":
		hosts, err := h.store.ListRemoteHosts(ctx, wsID)
		if err != nil {
			return marshalErrorResult(err.Error())
		}
		return monitoringJSON(map[string]any{"remote_hosts": hosts})
	case "sources":
		sources, err := h.store.ListLogSources(ctx, wsID)
		if err != nil {
			return marshalErrorResult(err.Error())
		}
		return monitoringJSON(map[string]any{"log_sources": sources})
	default:
		channels, err := h.store.ListMonitoringChannels(ctx, wsID)
		if err != nil {
			return marshalErrorResult(err.Error())
		}
		// Redact config: kind + floor + enabled are all a reader needs.
		type chView struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			MinSeverity string `json:"min_severity"`
			Enabled     bool   `json:"enabled"`
		}
		out := make([]chView, 0, len(channels))
		for _, c := range channels {
			out = append(out, chView{c.ID, c.Name, c.Kind, c.MinSeverity, c.Enabled})
		}
		return monitoringJSON(map[string]any{"channels": out})
	}
}

func parseWindow(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid window %q — use a Go duration like 10m", s)
	}
	return d, nil
}

func (h *handler) handleMonitoringStats(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		Window      string   `json:"window"`
		SourceIDs   []string `json:"source_ids"`
		WorkspaceID string   `json:"workspace_id"`
	}
	_ = json.Unmarshal(raw, &args)
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	window, err := parseWindow(args.Window, 10*time.Minute)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	st, err := h.monitoringQry.Stats(ctx, wsID, args.SourceIDs, window)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	return monitoringJSON(st)
}

func (h *handler) handleMonitoringDigest(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		Window       string   `json:"window"`
		BudgetTokens int      `json:"budget_tokens"`
		MaxSamples   int      `json:"max_samples"`
		MinSeverity  string   `json:"min_severity"`
		PendingOnly  bool     `json:"pending_only"`
		SourceIDs    []string `json:"source_ids"`
		WorkspaceID  string   `json:"workspace_id"`
	}
	_ = json.Unmarshal(raw, &args)
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	window, err := parseWindow(args.Window, 15*time.Minute)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	digest, err := h.monitoringQry.Digest(ctx, distill.DigestOptions{
		WorkspaceID: wsID, SourceIDs: args.SourceIDs, Window: window,
		BudgetTokens: args.BudgetTokens, MinSeverity: args.MinSeverity,
		MaxSamples: args.MaxSamples, PendingOnly: args.PendingOnly,
	})
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	// Bind the exact templates rendered by a pending digest to this worker run.
	// The post-execute effect check can then fail closed when a batched triage
	// only commits some of the classes the model actually saw. Templates omitted
	// by the token budget remain pending for a later run and are not claimed.
	if args.PendingOnly {
		if runID := monitoringRunID(ctx); runID != "" {
			ids := monitoringDigestTemplateIDs(digest)
			if len(ids) > 0 {
				if err := h.store.ClaimMonitoringTriageTemplates(
					ctx, wsID, runID, ids, time.Now().UTC(),
				); err != nil {
					return marshalErrorResult(err.Error())
				}
			}
		}
	}
	return marshalToolResult(digest)
}

func monitoringDigestTemplateIDs(digest string) []string {
	var ids []string
	for _, line := range strings.Split(digest, "\n") {
		line = strings.TrimSpace(line)
		if id, ok := strings.CutPrefix(line, "template_id:"); ok {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return uniqueMonitoringIDs(ids)
}

func (h *handler) handleMonitoringSearch(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		SourceID    string `json:"source_id"`
		Q           string `json:"q"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	if args.SourceID == "" || args.Q == "" {
		return marshalErrorResult("source_id and q are required")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc)
	}
	owned, err := h.monitoringSourceInWorkspace(ctx, args.SourceID, wsID)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	if !owned {
		return marshalErrorResult(store.ErrLogSourceNotFound.Error())
	}
	lines, err := h.store.SearchLogLines(ctx, args.SourceID, args.Q, args.Limit)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	return monitoringJSON(map[string]any{"lines": lines, "count": len(lines)})
}

func (h *handler) handleMonitoringRaw(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		TemplateID  string `json:"template_id"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	if args.TemplateID == "" {
		return marshalErrorResult("template_id is required")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc)
	}
	owned, err := h.monitoringTemplateInWorkspace(ctx, args.TemplateID, wsID)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	if !owned {
		return marshalErrorResult(store.ErrLogTemplateNotFound.Error())
	}
	lines, err := h.store.ListLogLinesByTemplate(ctx, args.TemplateID, args.Limit)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	return monitoringJSON(map[string]any{"lines": lines, "count": len(lines)})
}

func (h *handler) handleMonitoringAck(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		TemplateID  string `json:"template_id"`
		Note        string `json:"note"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	if args.TemplateID == "" {
		return marshalErrorResult("template_id is required")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	owned, err := h.monitoringTemplateInWorkspace(ctx, args.TemplateID, wsID)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	if !owned {
		return marshalErrorResult(store.ErrLogTemplateNotFound.Error())
	}
	if err := h.store.AckLogTemplate(ctx, args.TemplateID, args.Note); err != nil {
		return marshalErrorResult(err.Error())
	}
	return marshalToolResult(`{"acked": true}`)
}

func (h *handler) handleMonitoringNotify(ctx context.Context, raw json.RawMessage) json.RawMessage {
	if h.monitoringNtf == nil {
		return marshalErrorResult("monitoring notify dispatcher not enabled on this daemon")
	}
	var args struct {
		Severity     string `json:"severity"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		TaskID       string `json:"task_id"`
		NewIncident  bool   `json:"new_incident"`
		SourceName   string `json:"source_name"`
		RemoteHostID string `json:"remote_host_id"`
		TemplateID   string `json:"template_id"`
		WorkspaceID  string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	if !store.ValidSeverity(args.Severity) {
		return marshalErrorResult("severity must be info|warn|error|critical")
	}
	if args.Title == "" {
		return marshalErrorResult("title is required")
	}
	n := distill.Notification{
		WorkspaceID: wsID, Severity: args.Severity,
		Title: args.Title, Body: args.Body, TaskID: args.TaskID,
		NewIncident: args.NewIncident, SourceName: args.SourceName, TemplateID: args.TemplateID,
	}
	if args.RemoteHostID != "" {
		host, owned, err := h.monitoringHostInWorkspace(ctx, args.RemoteHostID, wsID)
		if err != nil {
			return marshalErrorResult("remote_host_id: " + err.Error())
		}
		if !owned {
			return marshalErrorResult("remote_host_id: " + store.ErrRemoteHostNotFound.Error())
		}
		n.RemoteHostName, n.RemoteHostAddr = host.Name, host.SSHHost
	}
	if err := h.monitoringNtf.Notify(ctx, n); err != nil {
		return marshalErrorResult(err.Error())
	}
	return marshalToolResult(`{"dispatched": true}`)
}
