package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// feedbackFixture wires a real service over a real store, seeds a monitoring
// incident, and files its canonical task the way commit_triage would.
func feedbackFixture(t *testing.T) (
	context.Context, *tasks.Service, *sqlite.DB, string,
	*store.LogSource, *store.LogTemplate, *store.Task, *store.MonitoringIncident,
) {
	t.Helper()
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	scope := &store.AuthScope{Name: "logwatch-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	host := &store.RemoteHost{
		WorkspaceID: wsID, Name: "example-node", SSHUser: "logwatch",
		SSHHost: "203.0.113.10", AuthScopeID: scope.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, host); err != nil {
		t.Fatalf("create remote host: %v", err)
	}
	source := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "api",
		Selector: "api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, source); err != nil {
		t.Fatalf("create log source: %v", err)
	}
	seen := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	template := &store.LogTemplate{
		ID: "tpl-restart", SourceID: source.ID,
		Masked: "container restarted", Severity: store.SeverityWarn,
		FirstSeen: seen, LastSeen: seen,
		SampleFirst: "container restarted", SampleLast: "container restarted",
	}
	if _, err := db.UpsertLogTemplate(ctx, template, 2); err != nil {
		t.Fatalf("upsert template: %v", err)
	}
	task, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "container restarting", Description: "evidence",
		Status: "open", Tags: []string{"logwatch", "incident"},
		Meta: `{"logwatch_class":"correlation:container restarting"}`,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	result, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: wsID, ClassKey: "correlation:container restarting", TaskID: task.ID,
		Disposition: store.MonitoringDispositionActionable, Severity: store.SeverityWarn,
		Title: "container restarting", SourceID: source.ID,
		TemplateIDs: []string{template.ID}, Evidence: "observed", ObservedAt: seen,
	})
	if err != nil {
		t.Fatalf("record triage: %v", err)
	}
	return ctx, svc, db, wsID, source, template, task, result.Incident
}

func closeTask(t *testing.T, ctx context.Context, svc *tasks.Service, wsID, id, status string) {
	t.Helper()
	if _, err := svc.Update(ctx, wsID, id, tasks.UpdatePatch{
		Status: &status, UpdatedBySessionID: "sess-op", ActorKind: "user",
	}); err != nil {
		t.Fatalf("close task as %s: %v", status, err)
	}
}

// The mapping is the whole judgement: a cancelled-kind status means "not a
// problem" and suppresses; a done-kind status means "fixed" and suppresses
// nothing. Driven through the real Update path, not the store directly.
func TestTaskResolutionFeedsBackToIncident(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          string
		wantDisposition string
		wantAcked       bool
		wantSuppressing bool
	}{
		{"wontfix suppresses", "cancelled", store.MonitoringDispositionBenign, true, true},
		{"fixed does not suppress", "done", store.MonitoringDispositionActionable, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, svc, db, wsID, _, template, task, incident := feedbackFixture(t)
			closeTask(t, ctx, svc, wsID, task.ID, tc.status)

			got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
			if err != nil {
				t.Fatalf("get incident: %v", err)
			}
			if got.Disposition != tc.wantDisposition {
				t.Fatalf("disposition = %q, want %q", got.Disposition, tc.wantDisposition)
			}
			tpl, err := db.GetLogTemplate(ctx, template.ID)
			if err != nil {
				t.Fatalf("get template: %v", err)
			}
			if tpl.Acked != tc.wantAcked {
				t.Fatalf("template acked = %v, want %v", tpl.Acked, tc.wantAcked)
			}
			live, err := db.ListMonitoringResolutions(ctx, wsID, true, 0)
			if err != nil {
				t.Fatalf("list resolutions: %v", err)
			}
			if (len(live) == 1) != tc.wantSuppressing {
				t.Fatalf("suppressing rows = %d, want suppressing=%v", len(live), tc.wantSuppressing)
			}
		})
	}
}

// Reopening the task is the explicit "this is live again" signal and must
// fully reverse the suppression.
func TestReopeningTaskLiftsSuppression(t *testing.T) {
	ctx, svc, db, wsID, source, template, task, incident := feedbackFixture(t)
	if err := db.MarkMonitoringIncidentNotified(ctx, incident.ID, store.SeverityWarn, template.LastSeen); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
	closeTask(t, ctx, svc, wsID, task.ID, "cancelled")

	open := "open"
	if _, err := svc.Update(ctx, wsID, task.ID, tasks.UpdatePatch{
		Status: &open, UpdatedBySessionID: "sess-op", ActorKind: "user",
	}); err != nil {
		t.Fatalf("reopen task: %v", err)
	}

	got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if got.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("reopen must restore the prior disposition, got %q", got.Disposition)
	}
	if got.LastNotifiedAt != nil {
		t.Fatalf("reopen must clear notification state so the next occurrence alerts")
	}
	tpl, err := db.GetLogTemplate(ctx, template.ID)
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if tpl.Acked {
		t.Fatalf("reopen must un-ack the templates the suppression acked")
	}
	pending, err := db.ListPendingLogTemplates(ctx, []string{source.ID}, 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("reopen must re-queue the template: len=%d err=%v", len(pending), err)
	}
	live, err := db.ListMonitoringResolutions(ctx, wsID, true, 0)
	if err != nil || len(live) != 0 {
		t.Fatalf("reopen must leave nothing suppressed: len=%d err=%v", len(live), err)
	}
}

// Closing an ordinary task must not touch monitoring and must not fail.
func TestClosingUnlinkedTaskIsHarmless(t *testing.T) {
	ctx, svc, db, wsID, _, template, _, incident := feedbackFixture(t)
	plain, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "hand-written", Status: "open",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	closeTask(t, ctx, svc, wsID, plain.ID, "cancelled")

	got, err := db.GetMonitoringIncidentByClass(ctx, wsID, incident.ClassKey)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if got.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("an unrelated task closure must not change any incident")
	}
	tpl, err := db.GetLogTemplate(ctx, template.ID)
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if tpl.Acked {
		t.Fatalf("an unrelated task closure must not ack anything")
	}
}

// A logwatch task whose incident row no longer exists (legacy fleet, migrated
// data) must close cleanly rather than erroring on the feedback path.
func TestClosingLogwatchTaskWithoutIncidentIsHarmless(t *testing.T) {
	ctx, svc, _, wsID, _, _, _, _ := feedbackFixture(t)
	legacy, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "legacy logwatch task", Status: "open",
		Tags: []string{"logwatch"}, Meta: `{"logwatch_class":"template:tpl-gone"}`,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	done := "done"
	if _, err := svc.Update(ctx, wsID, legacy.ID, tasks.UpdatePatch{
		Status: &done, UpdatedBySessionID: "sess-op",
	}); err != nil {
		t.Fatalf("closing a legacy logwatch task must not fail: %v", err)
	}
}
