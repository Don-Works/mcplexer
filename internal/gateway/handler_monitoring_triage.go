package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

type monitoringCommitArgs struct {
	Disposition    string   `json:"disposition"`
	Severity       string   `json:"severity"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	TemplateIDs    []string `json:"template_ids"`
	CorrelationKey string   `json:"correlation_key"`
	SourceName     string   `json:"source_name"`
	RemoteHostID   string   `json:"remote_host_id"`
	WorkspaceID    string   `json:"workspace_id"`
}

// handleMonitoringCommitTriage is the single atomic-ish domain operation the
// worker needs. Database uniqueness elects one canonical task; the incident +
// occurrence write is transactional; notification precedes the final triage
// receipt so delivery failures remain retryable and cannot produce a false
// successful worker run.
func (h *handler) handleMonitoringCommitTriage(ctx context.Context, raw json.RawMessage) json.RawMessage {
	if h.tasksSvc == nil {
		return marshalErrorResult("tasks subsystem not enabled on this daemon")
	}
	var args monitoringCommitArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	args.Disposition = strings.TrimSpace(strings.ToLower(args.Disposition))
	args.Severity = strings.TrimSpace(strings.ToLower(args.Severity))
	args.Title = strings.TrimSpace(args.Title)
	args.Body = strings.TrimSpace(args.Body)
	args.CorrelationKey = strings.TrimSpace(args.CorrelationKey)
	if !store.ValidMonitoringDisposition(args.Disposition) {
		return marshalErrorResult("disposition must be actionable|uncertain|evidence-gap|benign")
	}
	if !store.ValidSeverity(args.Severity) {
		return marshalErrorResult("severity must be info|warn|error|critical")
	}
	if len(args.CorrelationKey) > 500 {
		return marshalErrorResult("correlation_key is too long")
	}
	ids := uniqueMonitoringIDs(args.TemplateIDs)
	if len(ids) == 0 {
		return marshalErrorResult("template_ids is required")
	}
	if len(ids) > 50 {
		return marshalErrorResult("template_ids is capped at 50 per incident")
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	templates, observedAt, err := h.monitoringTemplatesForCommit(ctx, wsID, ids)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	requestedSeverity := args.Severity
	args.Severity = monitoringBoundedTriageSeverity(args.Severity, templates)
	// Replace novelty placeholders with an evidence signature so Chat and the
	// canonical task are operator-usable even when the model copies the
	// digest's "new error-class log template" framing.
	if args.Disposition != store.MonitoringDispositionBenign {
		sample, masked := monitoringTemplateEvidence(templates)
		args.Title = distill.ImproveMonitoringTitle(args.Title, args.Body, sample, masked)
	}
	runID := monitoringRunID(ctx)
	if args.Disposition == store.MonitoringDispositionBenign {
		note := args.Body
		if note == "" {
			note = "known benign / no operational action"
		}
		if err := h.store.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
			WorkspaceID: wsID, TemplateIDs: ids,
			Disposition: args.Disposition, Note: note,
			RunID: runID, CompletedAt: time.Now().UTC(),
		}); err != nil {
			return marshalErrorResult(err.Error())
		}
		return monitoringJSON(map[string]any{
			"committed": true, "disposition": args.Disposition,
			"acked_templates": ids, "run_id": runID,
		})
	}
	if args.Title == "" || args.Body == "" {
		return marshalErrorResult("title and body are required for non-benign triage")
	}
	if len(args.Title) > 500 || len(args.Body) > 12000 {
		return marshalErrorResult("title/body exceed monitoring triage bounds (500/12000 characters)")
	}
	var notificationHost *store.RemoteHost
	if args.RemoteHostID != "" {
		host, owned, hostErr := h.monitoringHostInWorkspace(ctx, args.RemoteHostID, wsID)
		if hostErr != nil {
			return marshalErrorResult("remote_host_id: " + hostErr.Error())
		}
		if !owned {
			return marshalErrorResult("remote_host_id: " + store.ErrRemoteHostNotFound.Error())
		}
		notificationHost = host
	}

	classKey, mappedIncident, err := h.monitoringClassForTemplates(ctx, wsID, ids, args.CorrelationKey)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	task, taskCreated, err := h.ensureMonitoringCanonicalTask(ctx, wsID, classKey, ids, args, mappedIncident)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	result, err := h.store.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: wsID, ClassKey: classKey, TaskID: task.ID,
		Disposition: args.Disposition, Severity: args.Severity,
		Title: args.Title, SourceID: templates[0].SourceID,
		TemplateIDs: ids, Evidence: args.Body, ObservedAt: observedAt,
	})
	if err != nil {
		return marshalErrorResult(err.Error())
	}

	// The incident ledger is canonical. Notes are a concise human-facing
	// projection, written only for genuinely new later episodes.
	if result.NewOccurrence && !result.NewIncident && !taskCreated {
		note := fmt.Sprintf("Monitoring occurrence %s (%s, %s)\n\n%s",
			result.Occurrence.OccurrenceKey, args.Severity, observedAt.Format(time.RFC3339), args.Body)
		_, _ = h.tasksSvc.AppendNote(ctx, wsID, task.ID, truncateMonitoringGateway(note, 6000),
			h.monitoringSessionID(), store.TaskSourceAgent, tasks.MutationContext{
				ActorKind: "worker", SessionID: h.monitoringSessionID(),
				WorkspacePath: h.routingClientRoot(ctx),
			})
	}

	notified := false
	if result.ShouldNotify {
		if h.monitoringNtf == nil {
			return marshalErrorResult("incident recorded but monitoring notify dispatcher is not enabled; triage remains pending")
		}
		// Age escalation only counts if it survives channel delivery: the
		// dispatcher drops anything ranked below channel.MinSeverity, which
		// defaults to "error". Dispatching the raw classifier severity meant an
		// aged-up warn was computed correctly and then silently discarded at the
		// floor, so a sustained incident escalated on paper and never reached
		// the operator.
		severity := monitoringEffectiveSeverity(result, args.Severity)
		n := distill.Notification{
			WorkspaceID: wsID, Severity: severity,
			Title: args.Title, Body: truncateMonitoringGateway(args.Body, 4000),
			TaskID: task.ID, NewIncident: result.NewIncident,
			SourceName: strings.TrimSpace(args.SourceName), TemplateID: ids[0],
		}
		if n.SourceName == "" {
			if src, getErr := h.store.GetLogSource(ctx, templates[0].SourceID); getErr == nil {
				n.SourceName = src.Name
			}
		}
		if notificationHost != nil {
			n.RemoteHostName, n.RemoteHostAddr = notificationHost.Name, notificationHost.SSHHost
		}
		if err := h.monitoringNtf.Notify(ctx, n); err != nil {
			return marshalErrorResult("incident recorded but notification failed; triage remains pending: " + err.Error())
		}
		if err := h.store.MarkMonitoringIncidentNotified(ctx, result.Incident.ID, severity, time.Now().UTC()); err != nil {
			return marshalErrorResult("notification sent but notification state failed to commit: " + err.Error())
		}
		notified = true
	}
	if err := h.store.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
		WorkspaceID: wsID, IncidentID: result.Incident.ID,
		TemplateIDs: ids, Disposition: args.Disposition,
		RunID: runID, CompletedAt: time.Now().UTC(),
	}); err != nil {
		return marshalErrorResult("incident recorded but triage completion failed: " + err.Error())
	}
	return monitoringJSON(map[string]any{
		"committed": true, "class_key": classKey,
		"task_id": task.ID, "task_created": taskCreated,
		"incident_id":             result.Incident.ID,
		"occurrence_id":           result.Occurrence.ID,
		"occurrence_key":          result.Occurrence.OccurrenceKey,
		"new_incident":            result.NewIncident,
		"new_occurrence":          result.NewOccurrence,
		"occurrence_count":        result.Incident.OccurrenceCount,
		"notification_dispatched": notified,
		"notification_reason":     result.NotificationReason,
		"severity":                args.Severity,
		"requested_severity":      requestedSeverity,
		"run_id":                  runID,
	})
}

func monitoringTemplateEvidence(templates []*store.LogTemplate) (sample, masked string) {
	for _, template := range templates {
		if template == nil {
			continue
		}
		if masked == "" && template.Masked != "" {
			masked = template.Masked
		}
		if sample == "" {
			sample = template.SampleLast
			if sample == "" {
				sample = template.SampleFirst
			}
		}
		if sample != "" && masked != "" {
			break
		}
	}
	return sample, masked
}

// monitoringBoundedTriageSeverity prevents a worker judgement from outranking
// the deterministic classifier evidence it was given. A worker may lower a
// severity (for example, when an ERROR line is a harmless handled condition),
// but promoting an INFO/WARN line to CRITICAL requires a verified impact signal
// that this commit shape does not carry. Service-health outages are covered by
// the separate expected-signal and uptime paths.
func monitoringBoundedTriageSeverity(requested string, templates []*store.LogTemplate) string {
	capSeverity := ""
	for _, template := range templates {
		if template == nil || !store.ValidSeverity(template.Severity) {
			continue
		}
		if capSeverity == "" ||
			store.SeverityRank(template.Severity) > store.SeverityRank(capSeverity) {
			capSeverity = template.Severity
		}
	}
	if capSeverity != "" &&
		store.SeverityRank(requested) > store.SeverityRank(capSeverity) {
		return capSeverity
	}
	return requested
}

// monitoringEffectiveSeverity picks the severity a notification is dispatched
// and recorded with. The store owns deterministic notification policy and its
// EffectiveSeverity is authoritative. Callers and store implementations
// predating that field leave it empty; fall back to the bounded classifier
// severity rather than dispatching an empty one.
func monitoringEffectiveSeverity(result *store.MonitoringTriageResult, fallback string) string {
	if result == nil {
		return fallback
	}
	if effective := strings.TrimSpace(result.EffectiveSeverity); effective != "" {
		return effective
	}
	return fallback
}

func (h *handler) handleMonitoringTriageEffect(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args struct {
		RunID       string `json:"run_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return marshalErrorResult(err.Error())
	}
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc)
	}
	runID := strings.TrimSpace(args.RunID)
	if runID == "" {
		runID = monitoringRunID(ctx)
	}
	if runID == "" {
		return marshalErrorResult("run_id required outside a worker run")
	}
	committed, err := h.store.HasMonitoringTriageReceipt(ctx, wsID, runID)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	return monitoringJSON(map[string]any{"run_id": runID, "committed": committed})
}

func (h *handler) monitoringTemplatesForCommit(
	ctx context.Context, workspaceID string, ids []string,
) ([]*store.LogTemplate, time.Time, error) {
	templates := make([]*store.LogTemplate, 0, len(ids))
	var observedAt time.Time
	for _, id := range ids {
		owned, err := h.monitoringTemplateInWorkspace(ctx, id, workspaceID)
		if err != nil {
			return nil, time.Time{}, err
		}
		if !owned {
			return nil, time.Time{}, store.ErrLogTemplateNotFound
		}
		tpl, err := h.store.GetLogTemplate(ctx, id)
		if err != nil {
			return nil, time.Time{}, err
		}
		templates = append(templates, tpl)
		if tpl.LastSeen.After(observedAt) {
			observedAt = tpl.LastSeen
		}
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return templates, observedAt, nil
}

func (h *handler) ensureMonitoringCanonicalTask(
	ctx context.Context, workspaceID, classKey string, ids []string,
	args monitoringCommitArgs, mapped *store.MonitoringIncident,
) (*store.Task, bool, error) {
	if mapped != nil && mapped.TaskID != "" {
		if task, err := h.tasksSvc.Get(ctx, workspaceID, mapped.TaskID); err == nil {
			updated, updateErr := h.updateMonitoringTaskMeta(ctx, task, classKey, ids, args)
			return updated, false, updateErr
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, false, fmt.Errorf("read canonical monitoring task: %w", err)
		}
	}
	if task, err := h.findMonitoringTaskByClass(ctx, workspaceID, classKey); err != nil {
		return nil, false, err
	} else if task != nil {
		updated, err := h.updateMonitoringTaskMeta(ctx, task, classKey, ids, args)
		return updated, false, err
	}
	// Upgrade the oldest legacy template/correlation match before creating a
	// new row. This makes the rollout converge existing fleets instead of
	// abandoning their current canonical task.
	if legacy, err := h.findLegacyMonitoringTask(ctx, workspaceID, ids, args.CorrelationKey); err != nil {
		return nil, false, err
	} else if legacy != nil {
		updated, err := h.updateMonitoringTaskMeta(ctx, legacy, classKey, ids, args)
		return updated, false, err
	}
	meta := monitoringTaskMeta("", classKey, ids, args.CorrelationKey)
	metaJSON, _ := json.Marshal(meta)
	task, err := h.tasksSvc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspaceID, Title: args.Title,
		Description: args.Body, Status: "open", Priority: monitoringTaskPriority(args.Severity),
		Tags: []string{"logwatch", "incident"}, Meta: string(metaJSON),
		SourceKind: store.TaskSourceAgent, SourceSessionID: h.monitoringSessionID(),
		CreatedBySessionID: h.monitoringSessionID(), ActorKind: "worker",
		WorkspacePath: h.routingClientRoot(ctx),
	})
	if err == nil {
		return task, true, nil
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		return nil, false, fmt.Errorf("create canonical monitoring task: %w", err)
	}
	// A concurrent commit won the unique (workspace, logwatch_class) index.
	// Read and reuse it; this is the only recovery path, never a second create.
	task, findErr := h.findMonitoringTaskByClass(ctx, workspaceID, classKey)
	if findErr != nil {
		return nil, false, findErr
	}
	if task == nil {
		return nil, false, errors.New("canonical monitoring task won uniqueness race but could not be read")
	}
	updated, updateErr := h.updateMonitoringTaskMeta(ctx, task, classKey, ids, args)
	return updated, false, updateErr
}

func (h *handler) findMonitoringTaskByClass(ctx context.Context, workspaceID, classKey string) (*store.Task, error) {
	rows, err := h.tasksSvc.List(ctx, store.TaskFilter{
		WorkspaceID: workspaceID, MetaMatch: map[string]string{"logwatch_class": classKey}, Limit: 10,
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return &rows[0], nil
}

func (h *handler) findLegacyMonitoringTask(
	ctx context.Context, workspaceID string, ids []string, correlationKey string,
) (*store.Task, error) {
	seen := map[string]store.Task{}
	filters := []store.TaskFilter{
		{WorkspaceID: workspaceID, Tags: []string{"logwatch"}, MetaIn: map[string][]string{"logwatch_template": ids}, Limit: 100},
		{WorkspaceID: workspaceID, Tags: []string{"logwatch"}, MetaIn: map[string][]string{"logwatch_templates": ids}, Limit: 100},
	}
	if correlationKey != "" {
		filters = append(filters, store.TaskFilter{WorkspaceID: workspaceID, Tags: []string{"logwatch"}, MetaIn: map[string][]string{"logwatch_correlation": {correlationKey}}, Limit: 100})
	}
	for _, filter := range filters {
		rows, err := h.tasksSvc.List(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			seen[row.ID] = row
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	rows := make([]store.Task, 0, len(seen))
	for _, row := range seen {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return &rows[0], nil
}

func (h *handler) updateMonitoringTaskMeta(
	ctx context.Context, task *store.Task, classKey string, ids []string,
	args monitoringCommitArgs,
) (*store.Task, error) {
	meta := monitoringTaskMeta(task.Meta, classKey, ids, args.CorrelationKey)
	metaJSON, _ := json.Marshal(meta)
	metaText := string(metaJSON)
	patch := tasks.UpdatePatch{
		Meta: &metaText, UpdatedBySessionID: h.monitoringSessionID(),
		ActorKind: "worker", WorkspacePath: h.routingClientRoot(ctx),
	}
	wantPriority := monitoringTaskPriority(args.Severity)
	if monitoringPriorityRank(wantPriority) > monitoringPriorityRank(task.Priority) {
		patch.Priority = &wantPriority
	}
	// The deterministic anomaly path deliberately files immediately, before a
	// model is consulted, so its first title/body can only be generic. Once the
	// worker commits self-contained evidence, upgrade that placeholder in place
	// instead of leaving the operator with "new error-class template" forever.
	// Preserve any already-human task verbatim; it may contain operator edits.
	if genericMonitoringTask(task) {
		if title := strings.TrimSpace(args.Title); title != "" {
			patch.Title = &title
		}
		if body := strings.TrimSpace(args.Body); body != "" {
			patch.Description = &body
		}
	}
	// Recurrence after resolution. Reaching here means fresh triage is landing
	// on the class that this task IS, so a closed task is stale: the problem
	// came back. Reopening is what makes a recurrence surface instead of
	// silently mutating a closed row that nobody will ever look at again —
	// the previous behaviour patched Meta and Priority only and never touched
	// status, so a regression after "fixed" was invisible by construction.
	//
	// Reopening also drives the feedback loop in reverse: the tasks service
	// fires its reopen hook, which lifts any suppression this task's earlier
	// resolution applied (restores the disposition, un-acks and re-queues the
	// templates, and clears last_notified_at so the triage that follows this
	// call is guaranteed to notify rather than being eaten by the backoff).
	//
	// This cannot churn: an already-triaged template only re-enters the
	// pending queue on a genuine severity increase (see UpsertLogTemplate),
	// so ordinary repeats of a resolved class never reach this path.
	reopened := false
	if task.ClosedAt != nil {
		open, terminal := "open", false
		patch.Status, patch.Terminal = &open, &terminal
		reopened = true
	}
	if !reopened && task.Meta == metaText && patch.Priority == nil &&
		patch.Title == nil && patch.Description == nil {
		return task, nil
	}
	return h.tasksSvc.Update(ctx, task.WorkspaceID, task.ID, patch)
}

func genericMonitoringTask(task *store.Task) bool {
	if task == nil {
		return false
	}
	return distill.IsGenericMonitoringTitle(task.Title)
}

func monitoringTaskMeta(raw, classKey string, ids []string, correlationKey string) map[string]any {
	meta := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		normalized, err := tasks.MetaToJSON(raw)
		if err == nil {
			_ = json.Unmarshal([]byte(normalized), &meta)
		}
	}
	all := append([]string{}, ids...)
	switch value := meta["logwatch_templates"].(type) {
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				all = append(all, text)
			}
		}
	case []string:
		all = append(all, value...)
	case string:
		all = append(all, value)
	}
	if value, ok := meta["logwatch_template"].(string); ok {
		all = append(all, value)
	}
	all = uniqueMonitoringIDs(all)
	meta["logwatch_class"] = classKey
	meta["logwatch_template"] = all[0]
	meta["logwatch_templates"] = all
	if correlationKey != "" {
		meta["logwatch_correlation"] = correlationKey
	}
	return meta
}

func uniqueMonitoringIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func monitoringTaskPriority(severity string) string {
	switch severity {
	case store.SeverityCritical:
		return "critical"
	case store.SeverityError, store.SeverityWarn:
		return "high"
	default:
		return "normal"
	}
}

func monitoringPriorityRank(priority string) int {
	switch priority {
	case "critical":
		return 3
	case "high":
		return 2
	case "normal":
		return 1
	case "low":
		return 0
	default:
		return -1
	}
}

func monitoringRunID(ctx context.Context) string {
	if runCtx, ok := runner.WorkerRunCtxFromContext(ctx); ok {
		return strings.TrimSpace(runCtx.RunID)
	}
	return ""
}

func (h *handler) monitoringSessionID() string {
	if h.sessions == nil {
		return ""
	}
	return h.sessions.sessionID()
}

func truncateMonitoringGateway(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
