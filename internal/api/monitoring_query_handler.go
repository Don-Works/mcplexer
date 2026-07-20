// monitoring_query_handler.go — read/notify REST surface behind the
// Monitoring page: runner status (peer responsibilities), template
// explorer, digest preview, ack, and test notifications. CRUD lives in
// monitoring_handler.go.
//
// The ID-addressed endpoints (POST /monitoring/templates/{id}/ack and
// POST /monitoring/notify with remote_host_id) verify that the named
// object belongs to the workspace the caller is acting in before
// touching it. A cross-workspace lookup is reported with the same
// not-found sentinel the store would emit for a truly absent row, so
// the UI cannot use the error shape to enumerate IDs from a foreign
// workspace.
package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

type monitoringQueryHandler struct {
	store    store.Store
	query    *distill.Query
	notifier distill.Notifier // nil = notify surface disabled
}

// status feeds the UI's peer-responsibilities panel: who this daemon
// is and whether IT is the peer group's monitoring runner.
func (h *monitoringQueryHandler) status(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	body := map[string]any{
		"gateway_hostname": hostname,
		"runner_enabled":   collect.RunnerEnabled(),
		"notify_enabled":   h.notifier != nil,
	}
	// Alert-route health, when a workspace is named. Optional and additive so
	// existing callers are unaffected: notify_enabled says the notifier is
	// WIRED, which a dead webhook does not contradict — the six-day outage had
	// notify_enabled=true throughout. `channels` is what makes the difference
	// between "we can notify" and "notifications are arriving" visible.
	if wsID := workspaceIDParam(r); wsID != "" {
		summary, err := summarizeChannelHealth(r.Context(), h.store, wsID)
		if err != nil {
			writeMonitoringErr(w, err, "summarize channel health")
			return
		}
		body["channels"] = summary
	}
	writeJSON(w, http.StatusOK, body)
}

// templates lists the explorer rows: masked shapes, severity, counts,
// novelty + ack state, joined with window counts and source names.
func (h *monitoringQueryHandler) templates(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	window := 24 * time.Hour
	if v := r.URL.Query().Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			writeError(w, http.StatusBadRequest, "invalid window — use a Go duration like 24h")
			return
		}
		window = d
	}
	ctx := r.Context()
	sources, err := h.store.ListLogSources(ctx, wsID)
	if err != nil {
		writeMonitoringErr(w, err, "list sources")
		return
	}
	ids := make([]string, 0, len(sources))
	names := map[string]string{}
	for _, s := range sources {
		ids = append(ids, s.ID)
		names[s.ID] = s.Name
	}
	since := time.Now().UTC().Add(-window)
	tpls, err := h.store.ListLogTemplates(ctx, ids, since, 500)
	if err != nil {
		writeMonitoringErr(w, err, "list templates")
		return
	}
	counts, err := h.store.CountLinesByTemplate(ctx, ids, since)
	if err != nil {
		writeMonitoringErr(w, err, "count lines")
		return
	}
	type row struct {
		*store.LogTemplate
		SourceName  string `json:"source_name"`
		WindowLines int64  `json:"window_lines"`
		New         bool   `json:"new"`
	}
	out := make([]row, 0, len(tpls))
	for _, t := range tpls {
		out = append(out, row{
			LogTemplate: t,
			SourceName:  names[t.SourceID],
			WindowLines: counts[t.ID],
			New:         !t.Acked && !t.FirstSeen.Before(since),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out, "window": window.String()})
}

// digest renders the same budget-bounded view the log-watch worker
// reads, so the preview IS the real thing.
func (h *monitoringQueryHandler) digest(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	opts := distill.DigestOptions{WorkspaceID: wsID}
	q := r.URL.Query()
	if v := q.Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			writeError(w, http.StatusBadRequest, "invalid window")
			return
		}
		opts.Window = d
	}
	if v := q.Get("budget_tokens"); v != "" {
		var n int
		for _, c := range v {
			if c < '0' || c > '9' {
				writeError(w, http.StatusBadRequest, "invalid budget_tokens")
				return
			}
			n = n*10 + int(c-'0')
		}
		opts.BudgetTokens = n
	}
	if v := q.Get("max_samples"); v != "" {
		var n int
		for _, c := range v {
			if c < '0' || c > '9' {
				writeError(w, http.StatusBadRequest, "invalid max_samples")
				return
			}
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 3 {
			writeError(w, http.StatusBadRequest, "max_samples must be between 1 and 3")
			return
		}
		opts.MaxSamples = n
	}
	if v := q.Get("min_severity"); v != "" {
		opts.MinSeverity = v
	}
	text, err := h.query.Digest(r.Context(), opts)
	if err != nil {
		writeMonitoringErr(w, err, "render digest")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"digest": text, "approx_tokens": len(text) / 4,
	})
}

func (h *monitoringQueryHandler) ack(w http.ResponseWriter, r *http.Request) {
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = decodeJSON(r, &in)
	owned, err := templateInWorkspace(r.Context(), h.store, r.PathValue("id"), wsID)
	if err != nil {
		writeMonitoringErr(w, err, "resolve template workspace")
		return
	}
	if !owned {
		// Conflate not-found with cross-workspace so the UI cannot use
		// the response shape to enumerate template IDs from a foreign
		// workspace. Mirrors handler_tasks.go:946.
		writeError(w, http.StatusNotFound, store.ErrLogTemplateNotFound.Error())
		return
	}
	if err := h.store.AckLogTemplate(r.Context(), r.PathValue("id"), in.Note); err != nil {
		writeMonitoringErr(w, err, "ack template")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"acked": true})
}

// notify is the UI's send path — used by the "send test notification"
// button (test=true bypasses throttles, stamps [test]) and available
// for manual escalations.
func (h *monitoringQueryHandler) notify(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		writeError(w, http.StatusNotImplemented, "monitoring notify not enabled on this daemon")
		return
	}
	var in struct {
		WorkspaceID  string `json:"workspace_id"`
		Severity     string `json:"severity"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		TaskID       string `json:"task_id"`
		NewIncident  bool   `json:"new_incident"`
		SourceName   string `json:"source_name"`
		RemoteHostID string `json:"remote_host_id"`
		TemplateID   string `json:"template_id"`
		Test         bool   `json:"test"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.WorkspaceID == "" || !store.ValidSeverity(in.Severity) || in.Title == "" {
		writeError(w, http.StatusBadRequest, "workspace_id, severity (info|warn|error|critical) and title are required")
		return
	}
	n := distill.Notification{
		WorkspaceID: in.WorkspaceID, Severity: in.Severity,
		Title: in.Title, Body: in.Body, TaskID: in.TaskID,
		NewIncident: in.NewIncident, SourceName: in.SourceName,
		TemplateID: in.TemplateID, Test: in.Test,
	}
	if in.RemoteHostID != "" {
		host, err := h.store.GetRemoteHost(r.Context(), in.RemoteHostID)
		if err != nil && !errors.Is(err, store.ErrRemoteHostNotFound) {
			writeMonitoringErr(w, err, "resolve remote host")
			return
		}
		if err != nil || host.WorkspaceID != in.WorkspaceID {
			// Conflate not-found with cross-workspace so the UI cannot
			// enumerate remote host IDs in a foreign workspace.
			writeError(w, http.StatusNotFound, store.ErrRemoteHostNotFound.Error())
			return
		}
		n.RemoteHostName, n.RemoteHostAddr = host.Name, host.SSHHost
	}
	// Prefer the outcome-reporting contract. Notify's nil/error return cannot
	// distinguish "delivered" from "released to backoff after six failed
	// attempts", and reported the latter as 200 dispatched:true.
	if rich, ok := h.notifier.(outcomeNotifier); ok {
		outcome, err := rich.NotifyWithOutcome(r.Context(), n)
		if err != nil && outcome.Status == "" {
			// No outcome was produced at all — a genuine internal failure
			// (invalid severity, workspace lookup) rather than a delivery
			// verdict. Those keep reporting as errors.
			writeMonitoringErr(w, err, "dispatch notification")
			return
		}
		writeNotifyOutcome(w, outcome)
		return
	}
	if err := h.notifier.Notify(r.Context(), n); err != nil {
		writeMonitoringErr(w, err, "dispatch notification")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"dispatched": true})
}

// templateInWorkspace resolves a template's owning workspace via its
// parent log_source (templates don't carry workspace_id directly). Missing
// and foreign rows share one response; genuine store failures propagate.
func templateInWorkspace(
	ctx context.Context, s store.Store, templateID, wsID string,
) (bool, error) {
	if templateID == "" || wsID == "" {
		return false, nil
	}
	tpl, err := s.GetLogTemplate(ctx, templateID)
	if errors.Is(err, store.ErrLogTemplateNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	src, err := s.GetLogSource(ctx, tpl.SourceID)
	if errors.Is(err, store.ErrLogSourceNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return src.WorkspaceID == wsID, nil
}
