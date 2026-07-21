// monitoring_anomaly_wire.go — daemon wiring for the distiller's incident seam.
//
// The distiller (internal/logwatch/distill) fires anomaly and rate-spike alerts
// but knows nothing about tasks or the incident ledger. This ensurer is the seam
// that gives a fired alert a canonical incident + task so its notification is
// linkable. It reuses the EXISTING machinery on purpose:
//
//   - task election by meta.logwatch_class — the same idempotent key the absence
//     evaluator (baselineTaskEnsurer) and the worker triage path use, so an
//     anomaly task, an absence task and a triage task for one class converge on
//     ONE row.
//   - store.RecordMonitoringTriage — the same create-or-reuse-by-class incident
//     writer the worker path calls, so an anomaly-filed incident and a later
//     worker triage of the same template roll into one incident with a growing
//     occurrence ledger, not a sibling per alert.
//
// Nothing here consults a model: the disposition is looked up, not judged.
package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

var _ distill.IncidentEnsurer = (*anomalyIncidentEnsurer)(nil)

// anomalyIncidentEnsurer files a distiller anomaly as a canonical incident + task.
type anomalyIncidentEnsurer struct {
	store store.Store
	tasks *tasks.Service
	now   func() time.Time
}

// newAnomalyIncidentEnsurer returns nil when a dependency is missing so a caller
// can wire it unconditionally.
func newAnomalyIncidentEnsurer(st store.Store, tasksSvc *tasks.Service) *anomalyIncidentEnsurer {
	if st == nil || tasksSvc == nil {
		return nil
	}
	return &anomalyIncidentEnsurer{store: st, tasks: tasksSvc, now: time.Now}
}

// EnsureIncident elects (or reuses) the canonical task, then records the incident
// through RecordMonitoringTriage. An input with no templates elects a task only
// (the rate-spike case): the incident ledger is template-keyed.
func (e *anomalyIncidentEnsurer) EnsureIncident(
	ctx context.Context, in distill.IncidentInput,
) (*distill.IncidentRef, error) {
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ClassKey) == "" {
		return nil, errors.New("anomaly incident: workspace_id and class_key required")
	}
	taskID, err := e.ensureTask(ctx, in)
	if err != nil {
		return nil, err
	}
	// Widen to workspace visibility so the incident task replicates to mirrored
	// peers. Best-effort: a widen failure must not drop the alert or its link.
	if pubErr := e.tasks.PublishSystemTask(ctx, taskID); pubErr != nil {
		slog.Warn("monitoring: incident task not widened for replication",
			"task", taskID, "class", in.ClassKey, "error", pubErr)
	}
	if len(in.TemplateIDs) == 0 {
		return &distill.IncidentRef{TaskID: taskID}, nil
	}
	result, err := e.store.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: in.WorkspaceID, ClassKey: in.ClassKey, TaskID: taskID,
		Disposition: e.disposition(ctx, in.WorkspaceID, in.ClassKey),
		Severity:    in.Severity, Title: in.Title, SourceID: in.SourceID,
		TemplateIDs: in.TemplateIDs, Evidence: in.Body, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		return nil, err
	}
	return &distill.IncidentRef{
		TaskID: taskID, IncidentID: result.Incident.ID, NewIncident: result.NewIncident,
	}, nil
}

// disposition keeps an anomaly from ever DOWNGRADING a durable verdict. A class
// already triaged non-benign keeps that disposition; a benign class re-opens as
// uncertain (RecordMonitoringTriage breaks the benign suppression when a
// non-benign value lands — the recurrence case); an unseen class starts uncertain,
// a fired-but-unclassified error shape awaiting triage.
func (e *anomalyIncidentEnsurer) disposition(ctx context.Context, workspaceID, classKey string) string {
	inc, err := e.store.GetMonitoringIncidentByClass(ctx, workspaceID, classKey)
	if err == nil && inc != nil &&
		store.ValidMonitoringDisposition(inc.Disposition) &&
		inc.Disposition != store.MonitoringDispositionBenign {
		return inc.Disposition
	}
	return store.MonitoringDispositionUncertain
}

// ensureTask returns the canonical task id for a class, creating it on first
// sight and reopening it if a prior episode resolved it.
func (e *anomalyIncidentEnsurer) ensureTask(ctx context.Context, in distill.IncidentInput) (string, error) {
	if existing, err := e.taskByClass(ctx, in.WorkspaceID, in.ClassKey); err != nil {
		return "", err
	} else if existing != nil {
		return e.reopenIfClosed(ctx, existing)
	}
	task, err := e.tasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: in.WorkspaceID, Title: in.Title, Description: in.Body,
		Status: "open", Priority: baselineTaskPriority(in.Severity),
		Tags: []string{"logwatch", "incident", "anomaly"},
		Meta: `{"logwatch_class":` + jsonQuote(in.ClassKey) +
			`,"logwatch_kind":"anomaly"}`,
		SourceKind: store.TaskSourceAgent, ActorKind: "system",
	})
	if err == nil {
		return task.ID, nil
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		return "", err
	}
	// A concurrent writer won the unique class index; adopt its row.
	adopted, err := e.taskByClass(ctx, in.WorkspaceID, in.ClassKey)
	if err != nil {
		return "", err
	}
	if adopted == nil {
		return "", errors.New("anomaly canonical task won uniqueness race but could not be read")
	}
	return e.reopenIfClosed(ctx, adopted)
}

// taskByClass returns the OLDEST task carrying this class marker, so repeated
// lookups are stable even if a duplicate ever slipped in.
func (e *anomalyIncidentEnsurer) taskByClass(
	ctx context.Context, workspaceID, classKey string,
) (*store.Task, error) {
	rows, err := e.tasks.List(ctx, store.TaskFilter{
		WorkspaceID: workspaceID,
		MetaMatch:   map[string]string{"logwatch_class": classKey},
		Limit:       10,
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return &rows[0], nil
}

// reopenIfClosed makes a recurrence visible: a class whose task was resolved and
// then fired again is not "done". Mirrors the worker triage path's reopen.
func (e *anomalyIncidentEnsurer) reopenIfClosed(ctx context.Context, task *store.Task) (string, error) {
	if task.ClosedAt == nil {
		return task.ID, nil
	}
	open, terminal := "open", false
	if _, err := e.tasks.Update(ctx, task.WorkspaceID, task.ID, tasks.UpdatePatch{
		Status: &open, Terminal: &terminal, ActorKind: "system",
	}); err != nil {
		return "", err
	}
	return task.ID, nil
}

// MarkNotified stamps the incident's notification clock so the daemon renotify
// sweep does not immediately re-fire a just-dispatched incident. Blank id
// (task-only) is a no-op.
func (e *anomalyIncidentEnsurer) MarkNotified(
	ctx context.Context, incidentID, severity string, at time.Time,
) error {
	if strings.TrimSpace(incidentID) == "" {
		return nil
	}
	return e.store.MarkMonitoringIncidentNotified(ctx, incidentID, severity, at)
}

// CloseIncident resolves the canonical task for a class on recovery.
func (e *anomalyIncidentEnsurer) CloseIncident(
	ctx context.Context, workspaceID, classKey, note string,
) error {
	existing, err := e.taskByClass(ctx, workspaceID, classKey)
	if err != nil || existing == nil {
		return err
	}
	if existing.ClosedAt != nil {
		return nil
	}
	done, description := "done", note
	_, err = e.tasks.Update(ctx, workspaceID, existing.ID, tasks.UpdatePatch{
		Status: &done, Description: &description, ActorKind: "system",
	})
	return err
}
