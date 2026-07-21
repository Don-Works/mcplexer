package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func seedMonitoringIncidentFixture(t *testing.T) (*store.Workspace, *store.LogSource, *store.LogTemplate, store.Store) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	host := seedRemoteHost(t, db, ctx, wsID, scopeID)
	source := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "api",
		Selector: "api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, source); err != nil {
		t.Fatalf("create source: %v", err)
	}
	seen := time.Date(2026, 7, 17, 10, 2, 0, 0, time.UTC)
	template := &store.LogTemplate{
		ID: "tpl-timeout", SourceID: source.ID,
		Masked: "request timeout after <duration>", Severity: store.SeverityWarn,
		FirstSeen: seen, LastSeen: seen, SampleFirst: "request timeout after 5s",
		SampleLast: "request timeout after 5s",
	}
	if isNew, err := db.UpsertLogTemplate(ctx, template, 3); err != nil || !isNew {
		t.Fatalf("upsert template: new=%v err=%v", isNew, err)
	}
	return &store.Workspace{ID: wsID}, source, template, db
}

func TestMonitoringTriageIsIdempotentAndRecordsAutomaticOccurrences(t *testing.T) {
	ws, source, template, db := seedMonitoringIncidentFixture(t)
	ctx := context.Background()
	task := &store.Task{
		WorkspaceID: ws.ID, Title: "API timeout", Meta: `{"logwatch_class":"correlation:api|app.go:42"}`,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	pending, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("initial pending templates: len=%d err=%v", len(pending), err)
	}
	record := store.MonitoringTriageRecord{
		WorkspaceID: ws.ID, ClassKey: "correlation:api|app.go:42", TaskID: task.ID,
		Disposition: store.MonitoringDispositionUncertain, Severity: store.SeverityWarn,
		Title: "API timeout", SourceID: source.ID, TemplateIDs: []string{template.ID},
		Evidence: "Observed evidence only", ObservedAt: template.LastSeen,
	}
	first, err := db.RecordMonitoringTriage(ctx, record)
	if err != nil {
		t.Fatalf("record first triage: %v", err)
	}
	if !first.NewIncident || !first.NewOccurrence || !first.ShouldNotify || first.Incident.TaskID != task.ID {
		t.Fatalf("first triage result: %+v", first)
	}
	second, err := db.RecordMonitoringTriage(ctx, record)
	if err != nil {
		t.Fatalf("retry triage: %v", err)
	}
	if second.NewIncident || second.NewOccurrence || !second.ShouldNotify || second.Incident.OccurrenceCount != 1 {
		t.Fatalf("retry must reuse incident/occurrence while notification is pending: %+v", second)
	}
	if err := db.MarkMonitoringIncidentNotified(ctx, first.Incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	third, err := db.RecordMonitoringTriage(ctx, record)
	if err != nil || third.ShouldNotify {
		t.Fatalf("notified retry must be silent: result=%+v err=%v", third, err)
	}
	if err := db.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
		WorkspaceID: ws.ID, IncidentID: first.Incident.ID, TemplateIDs: []string{template.ID},
		Disposition: store.MonitoringDispositionUncertain, RunID: "run-1",
		CompletedAt: template.LastSeen,
	}); err != nil {
		t.Fatalf("complete triage: %v", err)
	}
	if ok, err := db.HasMonitoringTriageReceipt(ctx, ws.ID, "run-1"); err != nil || !ok {
		t.Fatalf("triage receipt: ok=%v err=%v", ok, err)
	}
	pending, err = db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed template still pending: len=%d err=%v", len(pending), err)
	}

	// Same-class repeat observations update the incident in deterministic
	// buckets without reopening AI triage or creating another task.
	template.LastSeen = template.LastSeen.Add(2 * time.Minute) // same 15m bucket
	if isNew, err := db.UpsertLogTemplate(ctx, template, 4); err != nil || isNew {
		t.Fatalf("repeat template: new=%v err=%v", isNew, err)
	}
	incident, err := db.GetMonitoringIncidentByClass(ctx, ws.ID, record.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if incident.OccurrenceCount != 1 || incident.EventCount != 5 {
		t.Fatalf("same bucket counters = occurrences:%d events:%d, want 1/5", incident.OccurrenceCount, incident.EventCount)
	}
	pending, _ = db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if len(pending) != 0 {
		t.Fatal("same-severity mapped repeat must not wake AI")
	}

	template.LastSeen = template.LastSeen.Add(20 * time.Minute) // next bucket
	if _, err := db.UpsertLogTemplate(ctx, template, 2); err != nil {
		t.Fatalf("next occurrence bucket: %v", err)
	}
	incident, _ = db.GetMonitoringIncidentByClass(ctx, ws.ID, record.ClassKey)
	if incident.OccurrenceCount != 2 || incident.EventCount != 7 {
		t.Fatalf("next bucket counters = occurrences:%d events:%d, want 2/7", incident.OccurrenceCount, incident.EventCount)
	}
}

func TestMonitoringSeverityEscalationReopensTriage(t *testing.T) {
	ws, source, template, db := seedMonitoringIncidentFixture(t)
	ctx := context.Background()
	task := &store.Task{WorkspaceID: ws.ID, Title: "timeout", Meta: `{"logwatch_class":"template:tpl-timeout"}`}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	result, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: ws.ID, ClassKey: "template:tpl-timeout", TaskID: task.ID,
		Disposition: store.MonitoringDispositionUncertain, Severity: store.SeverityWarn,
		Title: "timeout", TemplateIDs: []string{template.ID}, ObservedAt: template.LastSeen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMonitoringIncidentNotified(ctx, result.Incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
		WorkspaceID: ws.ID, IncidentID: result.Incident.ID, TemplateIDs: []string{template.ID},
		Disposition: store.MonitoringDispositionUncertain, RunID: "run-warn", CompletedAt: template.LastSeen,
	}); err != nil {
		t.Fatal(err)
	}
	template.Severity = store.SeverityError
	template.LastSeen = template.LastSeen.Add(time.Minute)
	if _, err := db.UpsertLogTemplate(ctx, template, 1); err != nil {
		t.Fatal(err)
	}
	pending, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("severity escalation pending=%d err=%v", len(pending), err)
	}
	escalated, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: ws.ID, ClassKey: "template:tpl-timeout", TaskID: task.ID,
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityError,
		Title: "timeout impact verified", TemplateIDs: []string{template.ID}, ObservedAt: template.LastSeen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !escalated.ShouldNotify || escalated.NotificationReason != "severity_escalation" {
		t.Fatalf("severity escalation notification: %+v", escalated)
	}
}

func TestMonitoringTaskClassUniqueIndexAndLegacyMetaSafety(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	first := &store.Task{WorkspaceID: wsID, Title: "one", Meta: `{"logwatch_class":"template:x"}`}
	if err := db.CreateTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	duplicate := &store.Task{WorkspaceID: wsID, Title: "two", Meta: `{"logwatch_class":"template:x"}`}
	if err := db.CreateTask(ctx, duplicate); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate class err=%v, want ErrAlreadyExists", err)
	}
	// Empty/frontmatter legacy metadata must not make the expression index
	// throw "malformed JSON" for unrelated task creation.
	legacy := &store.Task{WorkspaceID: wsID, Title: "legacy", Meta: "status_kind: open"}
	if err := db.CreateTask(ctx, legacy); err != nil {
		t.Fatalf("legacy non-JSON task meta: %v", err)
	}
}

func TestMonitoringTriageClaimsRequireEveryRenderedTemplate(t *testing.T) {
	ws, source, first, db := seedMonitoringIncidentFixture(t)
	ctx := context.Background()
	second := &store.LogTemplate{
		ID: "tpl-db-pool", SourceID: source.ID,
		Masked: "database pool exhausted after <duration>", Severity: store.SeverityError,
		FirstSeen: first.FirstSeen, LastSeen: first.LastSeen,
		SampleFirst: "database pool exhausted after 30s",
		SampleLast:  "database pool exhausted after 30s",
	}
	if isNew, err := db.UpsertLogTemplate(ctx, second, 2); err != nil || !isNew {
		t.Fatalf("upsert second template: new=%v err=%v", isNew, err)
	}

	scope := &store.AuthScope{Name: "monitoring-claims", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create worker scope: %v", err)
	}
	worker := newWorker(ws.ID, scope.ID, "monitoring-claims")
	if err := db.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	run := &store.WorkerRun{WorkerID: worker.ID, WorkspaceID: ws.ID}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create worker run: %v", err)
	}

	ids := []string{first.ID, second.ID}
	if err := db.ClaimMonitoringTriageTemplates(ctx, ws.ID, run.ID, ids, first.LastSeen); err != nil {
		t.Fatalf("claim rendered templates: %v", err)
	}
	if complete, err := db.HasMonitoringTriageReceipt(ctx, ws.ID, run.ID); err != nil || complete {
		t.Fatalf("fresh claims complete=%v err=%v, want false", complete, err)
	}
	if err := db.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
		WorkspaceID: ws.ID, TemplateIDs: []string{first.ID},
		Disposition: store.MonitoringDispositionBenign, RunID: run.ID,
		CompletedAt: first.LastSeen,
	}); err != nil {
		t.Fatalf("complete first claim: %v", err)
	}
	if complete, err := db.HasMonitoringTriageReceipt(ctx, ws.ID, run.ID); err != nil || complete {
		t.Fatalf("partial claims complete=%v err=%v, want false", complete, err)
	}
	if err := db.CompleteMonitoringTriage(ctx, store.MonitoringTriageCompletion{
		WorkspaceID: ws.ID, TemplateIDs: []string{second.ID},
		Disposition: store.MonitoringDispositionUncertain, RunID: run.ID,
		CompletedAt: second.LastSeen,
	}); err != nil {
		t.Fatalf("complete second claim: %v", err)
	}
	if complete, err := db.HasMonitoringTriageReceipt(ctx, ws.ID, run.ID); err != nil || !complete {
		t.Fatalf("all claims complete=%v err=%v, want true", complete, err)
	}
}
