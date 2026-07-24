package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// The daemon sweep depends on *sqlite.DB satisfying the consumer-boundary
// interface; a signature drift must fail here, not at daemon boot.
var _ store.MonitoringRenotifyStore = (*sqlite.DB)(nil)

// incidentStart is the 2026-07-20 incident's shape: something began, kept
// recurring, and nobody was told again for hours.
var incidentStart = time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC)

type renotifyFixture struct {
	db     *sqlite.DB
	ctx    context.Context
	wsID   string
	source *store.LogSource
	seq    int
}

func newRenotifyFixture(t *testing.T) *renotifyFixture {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	host := seedRemoteHost(t, db, ctx, wsID, scopeID)
	source := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "orders",
		Selector: "orders", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, source); err != nil {
		t.Fatalf("create source: %v", err)
	}
	return &renotifyFixture{db: db, ctx: ctx, wsID: wsID, source: source}
}

// seed records one incident observed at each of the given times. Every
// observation goes through RecordMonitoringTriage, so first_seen, last_seen and
// occurrence_count are produced exactly as the live collector produces them.
func (f *renotifyFixture) seed(
	t *testing.T, severity string, observed ...time.Time,
) *store.MonitoringIncident {
	t.Helper()
	f.seq++
	templateID := fmt.Sprintf("tpl-%d", f.seq)
	classKey := fmt.Sprintf("correlation:orders|sync.go:%d", f.seq)
	template := &store.LogTemplate{
		ID: templateID, SourceID: f.source.ID,
		Masked: "order sync tick <n>", Severity: severity,
		FirstSeen: observed[0], LastSeen: observed[0],
		SampleFirst: "order sync tick 1", SampleLast: "order sync tick 1",
	}
	if _, err := f.db.UpsertLogTemplate(f.ctx, template, 1); err != nil {
		t.Fatalf("upsert template: %v", err)
	}
	task := &store.Task{WorkspaceID: f.wsID, Title: "order sync stalled"}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	var incident *store.MonitoringIncident
	for _, at := range observed {
		result, err := f.db.RecordMonitoringTriage(f.ctx, store.MonitoringTriageRecord{
			WorkspaceID: f.wsID, ClassKey: classKey, TaskID: task.ID,
			Disposition: store.MonitoringDispositionActionable, Severity: severity,
			Title: "order sync stalled", SourceID: f.source.ID,
			TemplateIDs: []string{templateID}, Evidence: "observed", ObservedAt: at,
		})
		if err != nil {
			t.Fatalf("record triage at %v: %v", at, err)
		}
		incident = result.Incident
	}
	return incident
}

func (f *renotifyFixture) due(t *testing.T, now time.Time, limit int) []*store.MonitoringRenotifyCandidate {
	t.Helper()
	out, err := f.db.ListMonitoringIncidentsDueForRenotify(f.ctx, f.wsID, now, limit)
	if err != nil {
		t.Fatalf("list due for renotify: %v", err)
	}
	return out
}

func (f *renotifyFixture) markNotified(t *testing.T, id, severity string, at time.Time) {
	t.Helper()
	if err := f.db.MarkMonitoringIncidentNotified(f.ctx, id, severity, at); err != nil {
		t.Fatalf("mark notified: %v", err)
	}
}

// TestRenotifyDueThenQuietAfterMarking is the incident itself: a template
// triaged once, still recurring at a steady severity, never returns to a
// worker — so before this query the persistence policy was never re-evaluated
// and the operator heard nothing for twelve hours.
func TestRenotifyDueThenQuietAfterMarking(t *testing.T) {
	f := newRenotifyFixture(t)
	incident := f.seed(t, store.SeverityError,
		incidentStart, incidentStart.Add(20*time.Minute), incidentStart.Add(40*time.Minute))
	f.markNotified(t, incident.ID, store.SeverityError, incidentStart)

	now := incidentStart.Add(90 * time.Minute)
	due := f.due(t, now, 10)
	if len(due) != 1 {
		t.Fatalf("still-recurring incident must come due: got %d candidates", len(due))
	}
	if due[0].NotificationReason != "persistent_incident" {
		t.Fatalf("reason = %q, want persistent_incident", due[0].NotificationReason)
	}
	if due[0].EffectiveSeverity != store.SeverityError {
		t.Fatalf("effective severity = %q, want error", due[0].EffectiveSeverity)
	}

	// The sweep marks after dispatching; the very next tick must be silent or
	// the operator is paged every five minutes.
	f.markNotified(t, incident.ID, due[0].EffectiveSeverity, now)
	if again := f.due(t, now, 10); len(again) != 0 {
		t.Fatalf("backoff not respected: %d candidates immediately after marking", len(again))
	}
	if soon := f.due(t, now.Add(10*time.Minute), 10); len(soon) != 0 {
		t.Fatalf("backoff not respected 10m later: %d candidates", len(soon))
	}
}

// TestRenotifyIgnoresStoppedRecurringIncident is the other half of the
// contract: "fixed" and "still broken" must be distinguishable. An incident
// whose last_seen has gone stale stops being reminded about, with no worker
// asked to declare it resolved.
func TestRenotifyIgnoresStoppedRecurringIncident(t *testing.T) {
	f := newRenotifyFixture(t)
	incident := f.seed(t, store.SeverityError,
		incidentStart, incidentStart.Add(20*time.Minute), incidentStart.Add(40*time.Minute))
	f.markNotified(t, incident.ID, store.SeverityError, incidentStart)

	// Six hours on, nothing new has been observed for over five of them.
	if due := f.due(t, incidentStart.Add(6*time.Hour), 10); len(due) != 0 {
		t.Fatalf("stopped-recurring incident must go quiet: got %d candidates", len(due))
	}
}

func TestRenotifyIgnoresIncidentResolvedFixedAfterLastObservation(t *testing.T) {
	f := newRenotifyFixture(t)
	incident := f.seed(t, store.SeverityError,
		incidentStart, incidentStart.Add(20*time.Minute), incidentStart.Add(40*time.Minute))
	f.markNotified(t, incident.ID, store.SeverityError, incidentStart)

	resolvedAt := incidentStart.Add(45 * time.Minute)
	if _, err := f.db.ApplyMonitoringTaskResolution(f.ctx, store.MonitoringResolutionInput{
		WorkspaceID: f.wsID,
		TaskID:      incident.TaskID,
		Outcome:     store.MonitoringOutcomeFixed,
		StatusText:  "done",
		ResolvedAt:  resolvedAt,
	}); err != nil {
		t.Fatalf("resolve incident as fixed: %v", err)
	}

	// The old row is still inside the active window at 90m, but the operator
	// closed it after its last observation. It must not say "still unresolved".
	if due := f.due(t, incidentStart.Add(90*time.Minute), 10); len(due) != 0 {
		t.Fatalf("fixed incident emitted a stale unresolved reminder: %+v", due)
	}
}

func TestRenotifyIgnoresBelowWarnIncidents(t *testing.T) {
	f := newRenotifyFixture(t)
	// Never notified, still recurring: everything except severity says "due".
	f.seed(t, store.SeverityInfo, incidentStart, incidentStart.Add(20*time.Minute))
	if due := f.due(t, incidentStart.Add(30*time.Minute), 10); len(due) != 0 {
		t.Fatalf("info-level noise must never be notifiable: got %d candidates", len(due))
	}
	// A benign disposition is the other floor. It is unreachable through the
	// store API today (RecordMonitoringTriage rejects benign, and the expected
	// signal path hardcodes actionable), so there is no row to construct here
	// without writing raw SQL; both the query and the policy exclude it.
}

// An age boundary may schedule a reminder, but it must not manufacture a
// higher severity than the classifier observed.
func TestRenotifyAgeReminderKeepsClassifierSeverity(t *testing.T) {
	f := newRenotifyFixture(t)
	incident := f.seed(t, store.SeverityWarn, incidentStart, incidentStart.Add(4*time.Hour))
	f.markNotified(t, incident.ID, store.SeverityWarn, incidentStart)

	due := f.due(t, incidentStart.Add(4*time.Hour+10*time.Minute), 10)
	if len(due) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(due))
	}
	if due[0].NotificationReason != "age_escalation" {
		t.Fatalf("reason = %q, want age_escalation", due[0].NotificationReason)
	}
	if due[0].EffectiveSeverity != store.SeverityWarn {
		t.Fatalf("effective severity = %q, want warn (age must not raise severity)",
			due[0].EffectiveSeverity)
	}
	if due[0].Incident.Severity != store.SeverityWarn {
		t.Fatalf("raw classifier severity must be untouched, got %q", due[0].Incident.Severity)
	}
}

// TestRenotifyUnnotifiedIncidentIsRecovered covers the crash window: the
// triage handler dispatched but died before MarkMonitoringIncidentNotified, or
// delivery failed outright. The sweep picks the incident up rather than
// leaving it silent forever.
func TestRenotifyUnnotifiedIncidentIsRecovered(t *testing.T) {
	f := newRenotifyFixture(t)
	f.seed(t, store.SeverityError, incidentStart, incidentStart.Add(20*time.Minute))
	due := f.due(t, incidentStart.Add(30*time.Minute), 10)
	if len(due) != 1 || due[0].NotificationReason != "unnotified_incident" {
		t.Fatalf("never-notified incident must be recovered: %+v", due)
	}
}

func TestRenotifyLimitBoundsTheBatch(t *testing.T) {
	f := newRenotifyFixture(t)
	for i := 0; i < 3; i++ {
		incident := f.seed(t, store.SeverityError,
			incidentStart, incidentStart.Add(20*time.Minute), incidentStart.Add(40*time.Minute))
		f.markNotified(t, incident.ID, store.SeverityError, incidentStart)
	}
	now := incidentStart.Add(90 * time.Minute)
	if all := f.due(t, now, 10); len(all) != 3 {
		t.Fatalf("unbounded sweep should see all 3, got %d", len(all))
	}
	if bounded := f.due(t, now, 1); len(bounded) != 1 {
		t.Fatalf("limit must bound the batch: got %d candidates for limit=1", len(bounded))
	}
}

func TestRenotifyIsScopedToOneWorkspace(t *testing.T) {
	f := newRenotifyFixture(t)
	incident := f.seed(t, store.SeverityError,
		incidentStart, incidentStart.Add(20*time.Minute), incidentStart.Add(40*time.Minute))
	f.markNotified(t, incident.ID, store.SeverityError, incidentStart)
	now := incidentStart.Add(90 * time.Minute)

	other := &store.Workspace{Name: "other-ws", DefaultPolicy: "allow"}
	if err := f.db.CreateWorkspace(f.ctx, other); err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	out, err := f.db.ListMonitoringIncidentsDueForRenotify(f.ctx, other.ID, now, 10)
	if err != nil {
		t.Fatalf("list for second workspace: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("workspace scoping leaked %d incidents", len(out))
	}
	if len(f.due(t, now, 10)) != 1 {
		t.Fatal("original workspace must still see its own incident")
	}
}

func TestRenotifyRejectsEmptyWorkspace(t *testing.T) {
	f := newRenotifyFixture(t)
	if _, err := f.db.ListMonitoringIncidentsDueForRenotify(f.ctx, "  ", time.Now(), 10); err == nil {
		t.Fatal("empty workspace_id must be rejected")
	}
}
