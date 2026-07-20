package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// resolutionFixture seeds a workspace + source + template, files a canonical
// task, and commits one triage so there is a live incident to resolve.
func resolutionFixture(t *testing.T) (
	context.Context, store.Store, string, *store.LogSource, *store.LogTemplate,
	*store.Task, *store.MonitoringIncident,
) {
	t.Helper()
	ws, source, template, db := seedMonitoringIncidentFixture(t)
	ctx := context.Background()
	task := &store.Task{
		WorkspaceID: ws.ID, Title: "API timeout",
		Meta: `{"logwatch_class":"correlation:api timeout"}`,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	result, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: ws.ID, ClassKey: "correlation:api timeout", TaskID: task.ID,
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityWarn,
		Title: "API timeout", SourceID: source.ID, TemplateIDs: []string{template.ID},
		Evidence: "observed", ObservedAt: template.LastSeen,
	})
	if err != nil {
		t.Fatalf("record triage: %v", err)
	}
	return ctx, db, ws.ID, source, template, task, result.Incident
}

func resolutionStore(t *testing.T, db store.Store) store.MonitoringResolutionStore {
	t.Helper()
	resolutions, ok := db.(store.MonitoringResolutionStore)
	if !ok {
		t.Fatalf("store does not implement MonitoringResolutionStore")
	}
	return resolutions
}

func mustGetTemplate(t *testing.T, ctx context.Context, db store.Store, id string) *store.LogTemplate {
	t.Helper()
	tpl, err := db.GetLogTemplate(ctx, id)
	if err != nil {
		t.Fatalf("get template %s: %v", id, err)
	}
	return tpl
}

// Resolving benign must mute the incident AND stop the novelty wake-up. Both
// halves matter: the first stops the noise, the second stops the model spend.
func TestResolvingBenignMutesIncidentAndStopsNoveltyWakeups(t *testing.T) {
	ctx, db, wsID, source, template, task, incident := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	row, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: store.MonitoringOutcomeBenign,
		StatusText: "wontfix", BySession: "sess-1", ByActor: "user",
		ResolvedAt: template.LastSeen.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("apply resolution: %v", err)
	}
	if row == nil || !row.Suppressing() {
		t.Fatalf("benign resolution must suppress: %+v", row)
	}
	if len(row.AckedTemplateIDs) != 1 || row.AckedTemplateIDs[0] != template.ID {
		t.Fatalf("resolution must record the templates it acked: %+v", row.AckedTemplateIDs)
	}
	if row.DispositionBefore != store.MonitoringDispositionActionable {
		t.Fatalf("prior disposition must be captured for reversal, got %q", row.DispositionBefore)
	}

	got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	// benign is the EXISTING mute hook in monitoringNotificationDue.
	if got.Disposition != store.MonitoringDispositionBenign {
		t.Fatalf("incident disposition = %q, want benign", got.Disposition)
	}
	if tpl := mustGetTemplate(t, ctx, db, template.ID); !tpl.Acked {
		t.Fatalf("benign resolution must ack the incident templates")
	}
	// Acked templates leave the durable pending queue, which is what the
	// log-watch pre-execute gate reads: no pending template, no model wake-up.
	pending, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after benign resolution: len=%d err=%v", len(pending), err)
	}
}

// "fixed" must NOT silence anything. Conflating it with benign is how a real
// regression gets swallowed.
func TestResolvingFixedDoesNotSilenceLaterRecurrence(t *testing.T) {
	ctx, db, wsID, source, template, task, incident := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	row, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: store.MonitoringOutcomeFixed,
		StatusText: "done", BySession: "sess-1", ByActor: "user",
		ResolvedAt: template.LastSeen.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("apply resolution: %v", err)
	}
	if row.Suppressing() {
		t.Fatalf("fixed must never suppress: %+v", row)
	}
	got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if got.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("fixed must leave disposition alone, got %q", got.Disposition)
	}
	if tpl := mustGetTemplate(t, ctx, db, template.ID); tpl.Acked {
		t.Fatalf("fixed must not ack templates")
	}
	if _, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0); err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// A later recurrence must still be able to notify.
	if err := db.MarkMonitoringIncidentNotified(ctx, incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	later := template.LastSeen.Add(48 * time.Hour)
	again, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: wsID, ClassKey: incident.ClassKey, TaskID: task.ID,
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityError,
		Title: "API timeout", SourceID: source.ID, TemplateIDs: []string{template.ID},
		Evidence: "back again", ObservedAt: later,
	})
	if err != nil {
		t.Fatalf("recurrence triage: %v", err)
	}
	if !again.ShouldNotify {
		t.Fatalf("recurrence after a fixed resolution must notify: %+v", again)
	}
}

// A recurrence landing on a suppressed incident must break the suppression
// rather than be swallowed by it — and the break must be recorded.
func TestRecurrenceBreaksBenignSuppressionAndSurfaces(t *testing.T) {
	ctx, db, wsID, source, template, task, incident := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	if err := db.MarkMonitoringIncidentNotified(ctx, incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	if _, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: store.MonitoringOutcomeBenign,
		StatusText: "wontfix", ResolvedAt: template.LastSeen.Add(time.Hour),
	}); err != nil {
		t.Fatalf("apply resolution: %v", err)
	}

	later := template.LastSeen.Add(2 * time.Hour)
	again, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: wsID, ClassKey: incident.ClassKey, TaskID: task.ID,
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityError,
		Title: "API timeout", SourceID: source.ID, TemplateIDs: []string{template.ID},
		Evidence: "recurred", ObservedAt: later,
	})
	if err != nil {
		t.Fatalf("recurrence triage: %v", err)
	}
	if !again.ShouldNotify {
		t.Fatalf("a recurrence must surface even after a benign resolution: %+v", again)
	}
	if again.Incident.Disposition == store.MonitoringDispositionBenign {
		t.Fatalf("suppression must be lifted by a recurrence")
	}
	if tpl := mustGetTemplate(t, ctx, db, template.ID); tpl.Acked {
		t.Fatalf("recurrence must un-ack the templates the suppression acked")
	}
	// The lift is auditable, not silent.
	rows, err := resolutions.ListMonitoringResolutions(ctx, wsID, false, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list resolutions: len=%d err=%v", len(rows), err)
	}
	if rows[0].ClearedAt == nil || rows[0].ClearedReason != store.MonitoringClearReasonRecurrence {
		t.Fatalf("recurrence must record why the suppression was lifted: %+v", rows[0])
	}
	// And it is no longer listed as suppressing.
	live, err := db.(store.MonitoringResolutionStore).ListMonitoringResolutions(ctx, wsID, true, 0)
	if err != nil || len(live) != 0 {
		t.Fatalf("cleared resolution must not be listed as suppressing: len=%d err=%v", len(live), err)
	}
	_ = source
}

// Suppression must be listable and reversible, and reversal must restore
// exactly what suppression changed.
func TestSuppressionIsListableAndReversible(t *testing.T) {
	ctx, db, wsID, source, template, task, incident := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	if err := db.MarkMonitoringIncidentNotified(ctx, incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	if _, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: store.MonitoringOutcomeBenign,
		StatusText: "wontfix", BySession: "sess-9", ByActor: "user",
		ResolvedAt: template.LastSeen.Add(time.Hour),
	}); err != nil {
		t.Fatalf("apply resolution: %v", err)
	}

	live, err := resolutions.ListMonitoringResolutions(ctx, wsID, true, 0)
	if err != nil || len(live) != 1 {
		t.Fatalf("suppression must be listable: len=%d err=%v", len(live), err)
	}
	if live[0].ResolvedBySession != "sess-9" || live[0].StatusText != "wontfix" {
		t.Fatalf("suppression must be attributable: %+v", live[0])
	}
	if live[0].ClassKey != incident.ClassKey || live[0].IncidentTitle == "" {
		t.Fatalf("suppression listing must project the incident: %+v", live[0])
	}

	cleared, err := resolutions.ClearMonitoringResolution(ctx, wsID, incident.ID, "operator disagreed", "sess-10")
	if err != nil || cleared == nil {
		t.Fatalf("clear suppression: row=%+v err=%v", cleared, err)
	}
	got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if got.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("clear must restore the prior disposition, got %q", got.Disposition)
	}
	if got.LastNotifiedAt != nil {
		t.Fatalf("clear must reset notification state so the next occurrence speaks")
	}
	if tpl := mustGetTemplate(t, ctx, db, template.ID); tpl.Acked {
		t.Fatalf("clear must un-ack the templates the suppression acked")
	}
	// Re-queued for the worker, not left permanently invisible.
	pending, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("clear must re-queue the templates: len=%d err=%v", len(pending), err)
	}
	// Idempotent second clear.
	again, err := resolutions.ClearMonitoringResolution(ctx, wsID, incident.ID, "again", "sess-10")
	if err != nil || again != nil {
		t.Fatalf("second clear must be a no-op: row=%+v err=%v", again, err)
	}
}

// A suppression must never un-ack a template the operator acked themselves.
func TestClearDoesNotUnackOperatorAcks(t *testing.T) {
	ctx, db, wsID, _, template, task, incident := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	if err := db.AckLogTemplate(ctx, template.ID, "known noisy, operator ack"); err != nil {
		t.Fatalf("operator ack: %v", err)
	}
	row, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: store.MonitoringOutcomeBenign,
		StatusText: "wontfix", ResolvedAt: template.LastSeen.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("apply resolution: %v", err)
	}
	if len(row.AckedTemplateIDs) != 0 {
		t.Fatalf("an already-acked template must not be claimed by the resolution: %+v", row.AckedTemplateIDs)
	}
	if _, err := resolutions.ClearMonitoringResolution(ctx, wsID, incident.ID, "manual", "sess-1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if tpl := mustGetTemplate(t, ctx, db, template.ID); !tpl.Acked {
		t.Fatalf("clearing our suppression must not undo the operator's own ack")
	}
}

// An unlinked or legacy task must flow through the feedback path harmlessly.
func TestResolutionOnUnlinkedTaskIsNoOp(t *testing.T) {
	ctx, db, wsID, _, _, _, _ := resolutionFixture(t)
	resolutions := resolutionStore(t, db)

	orphan := &store.Task{WorkspaceID: wsID, Title: "hand-written task"}
	if err := db.CreateTask(ctx, orphan); err != nil {
		t.Fatalf("create task: %v", err)
	}
	row, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: orphan.ID, Outcome: store.MonitoringOutcomeBenign,
		StatusText: "cancelled",
	})
	if err != nil {
		t.Fatalf("unlinked task must not error: %v", err)
	}
	if row != nil {
		t.Fatalf("unlinked task must produce no resolution: %+v", row)
	}
	cleared, err := resolutions.ClearMonitoringResolutionForTask(ctx, wsID, orphan.ID, "task_reopened", "")
	if err != nil || cleared != nil {
		t.Fatalf("clearing an unlinked task must be a no-op: row=%+v err=%v", cleared, err)
	}
}

func TestApplyMonitoringTaskResolutionRejectsUnknownOutcome(t *testing.T) {
	ctx, db, wsID, _, _, task, _ := resolutionFixture(t)
	resolutions := resolutionStore(t, db)
	if _, err := resolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: wsID, TaskID: task.ID, Outcome: "silenced-forever",
	}); err == nil {
		t.Fatalf("an unknown outcome must be rejected rather than guessed")
	}
}
