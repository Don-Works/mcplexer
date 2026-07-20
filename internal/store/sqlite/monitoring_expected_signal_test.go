package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// expectedSignalStore is the slice of *sqlite.DB these tests drive. The
// expected-signal methods are deliberately NOT part of store.Store yet (that
// would force every mock in the tree to grow eight methods), so the test
// composes the two interfaces here.
type expectedSignalStore interface {
	store.Store
	store.MonitoringExpectedSignalStore
}

type expectedSignalFixture struct {
	db     expectedSignalStore
	wsID   string
	source *store.LogSource
	task   *store.Task
	rule   *store.MonitoringExpectedSignal
	ctx    context.Context
}

// seedExpectedSignalFixture builds a workspace + host + source + canonical task
// + a 6h always-on rule that has already proven it can see its signal.
func seedExpectedSignalFixture(t *testing.T) *expectedSignalFixture {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	host := seedRemoteHost(t, db, ctx, wsID, scopeID)
	source := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "orders-worker",
		Selector: "orders-worker", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, source); err != nil {
		t.Fatalf("create source: %v", err)
	}
	task := &store.Task{WorkspaceID: wsID, Title: "orders ingest stopped"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	created := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	rule := &store.MonitoringExpectedSignal{
		WorkspaceID: wsID, SourceID: source.ID, Name: "orders ingested",
		MatchSubstring: "order ingested", MinCount: 1,
		WindowSeconds: int64((6 * time.Hour).Seconds()),
		Severity:      store.SeverityError, Enabled: true,
		RequireSourceLiveness: true, CreatedAt: created,
	}
	if err := db.CreateMonitoringExpectedSignal(ctx, rule); err != nil {
		t.Fatalf("create expected signal: %v", err)
	}
	return &expectedSignalFixture{db: db, wsID: wsID, source: source, task: task, rule: rule, ctx: ctx}
}

// seedLines inserts retained lines under one template so ObserveExpectedSignal
// has something to scan. severity feeds the min_severity floor.
func (f *expectedSignalFixture) seedLines(t *testing.T, templateID, severity string, at time.Time, lines ...string) {
	t.Helper()
	tpl := &store.LogTemplate{
		ID: templateID, SourceID: f.source.ID, Masked: templateID,
		Severity: severity, FirstSeen: at, LastSeen: at,
		SampleFirst: lines[0], SampleLast: lines[len(lines)-1],
	}
	if _, err := f.db.UpsertLogTemplate(f.ctx, tpl, int64(len(lines))); err != nil {
		t.Fatalf("upsert template %s: %v", templateID, err)
	}
	rows := make([]store.LogLine, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, store.LogLine{
			SourceID: f.source.ID, TemplateID: templateID, TS: at, Line: line,
		})
	}
	if err := f.db.InsertLogLines(f.ctx, rows); err != nil {
		t.Fatalf("insert log lines: %v", err)
	}
}

// evaluate runs the real pipeline: observe → pure evaluate → record.
func (f *expectedSignalFixture) evaluate(t *testing.T, now time.Time) *store.ExpectedSignalResult {
	t.Helper()
	rule, err := f.db.GetMonitoringExpectedSignal(f.ctx, f.rule.ID)
	if err != nil {
		t.Fatalf("get rule: %v", err)
	}
	obs, health, err := f.db.ObserveExpectedSignal(f.ctx, rule, now)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	loc, err := rule.Location()
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	decision := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule: *rule, Observed: obs, Health: health, Now: now, Location: loc,
	})
	taskID := ""
	if decision.Raise {
		taskID = f.task.ID
	}
	result, err := f.db.RecordExpectedSignalOutcome(f.ctx, store.ExpectedSignalRecord{
		RuleID: rule.ID, TaskID: taskID, Decision: decision, ObservedAt: now,
	})
	if err != nil {
		t.Fatalf("record outcome: %v", err)
	}
	return result
}

func TestExpectedSignalCRUDRoundTrip(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	got, err := f.db.GetMonitoringExpectedSignal(f.ctx, f.rule.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "orders ingested" || got.MinCount != 1 || got.Timezone != "UTC" {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if got.ActiveDaysMask != 127 || got.ActiveEndMinute != 1440 || got.MaxConsecutiveFailures != 3 {
		t.Fatalf("schedule/health defaults wrong: %+v", got)
	}
	if !got.RequireSourceLiveness || !got.Enabled {
		t.Fatalf("bool round-trip wrong: %+v", got)
	}
	rows, err := f.db.ListMonitoringExpectedSignals(f.ctx, f.wsID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: len=%d err=%v", len(rows), err)
	}
	enabled, err := f.db.ListEnabledMonitoringExpectedSignals(f.ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("list enabled: len=%d err=%v", len(enabled), err)
	}
	got.MatchSubstring = "orders committed"
	got.MinCount = 5
	if err := f.db.UpdateMonitoringExpectedSignal(f.ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := f.db.GetMonitoringExpectedSignal(f.ctx, f.rule.ID)
	if err != nil || after.MinCount != 5 || after.MatchSubstring != "orders committed" {
		t.Fatalf("update round-trip: %+v err=%v", after, err)
	}
	if err := f.db.DeleteMonitoringExpectedSignal(f.ctx, f.rule.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.db.GetMonitoringExpectedSignal(f.ctx, f.rule.ID); !errors.Is(err, store.ErrMonitoringExpectedSignalNotFound) {
		t.Fatalf("get after delete err = %v, want ErrMonitoringExpectedSignalNotFound", err)
	}
	if !errors.Is(store.ErrMonitoringExpectedSignalNotFound, store.ErrNotFound) {
		t.Fatal("sentinel must wrap store.ErrNotFound")
	}
	if err := f.db.DeleteMonitoringExpectedSignal(f.ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing err = %v, want ErrNotFound", err)
	}
}

// TestObserveExpectedSignalCounts is the SQL contract: matching runs over raw
// retained lines (case-insensitively), TotalLines counts every line from the
// source regardless of pattern, and min_severity floors on template severity.
func TestObserveExpectedSignalCounts(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	f.seedLines(t, "tpl-order", store.SeverityInfo, now.Add(-2*time.Hour),
		"Order Ingested id=1", "order ingested id=2")
	f.seedLines(t, "tpl-heartbeat", store.SeverityInfo, now.Add(-time.Hour),
		"heartbeat ok", "heartbeat ok")
	f.seedLines(t, "tpl-stale", store.SeverityInfo, now.Add(-30*time.Hour),
		"order ingested id=0")

	rule, err := f.db.GetMonitoringExpectedSignal(f.ctx, f.rule.ID)
	if err != nil {
		t.Fatalf("get rule: %v", err)
	}
	obs, health, err := f.db.ObserveExpectedSignal(f.ctx, rule, now)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.MatchCount != 2 {
		t.Fatalf("match count = %d, want 2 (case-insensitive, in-window only)", obs.MatchCount)
	}
	if obs.TotalLines != 4 {
		t.Fatalf("total lines = %d, want 4 (all in-window lines regardless of pattern)", obs.TotalLines)
	}
	if obs.LastMatchAt == nil || !obs.LastMatchAt.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("last match = %v, want %v", obs.LastMatchAt, now.Add(-2*time.Hour))
	}
	if !health.Enabled || health.ConsecutiveFailures != 0 {
		t.Fatalf("health = %+v, want enabled with no failures", health)
	}

	// A severity floor excludes the info-level order lines entirely.
	rule.MinSeverity = store.SeverityError
	if err := f.db.UpdateMonitoringExpectedSignal(f.ctx, rule); err != nil {
		t.Fatalf("update min severity: %v", err)
	}
	obs, _, err = f.db.ObserveExpectedSignal(f.ctx, rule, now)
	if err != nil {
		t.Fatalf("observe with severity floor: %v", err)
	}
	if obs.MatchCount != 0 {
		t.Fatalf("match count with error floor = %d, want 0", obs.MatchCount)
	}
	if obs.TotalLines != 4 {
		t.Fatalf("total lines must ignore the match filter, got %d", obs.TotalLines)
	}
}

// TestExpectedSignalAbsenceConvergesOnOneIncident is the anti-storm guarantee:
// ticking every few minutes for hours must produce ONE incident with a growing
// occurrence ledger, never N incidents.
func TestExpectedSignalAbsenceConvergesOnOneIncident(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	start := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	// Prove the rule can see its signal, then let it go quiet.
	f.seedLines(t, "tpl-order", store.SeverityInfo, start, "order ingested id=1")
	f.seedLines(t, "tpl-heartbeat", store.SeverityInfo, start.Add(9*time.Hour), "heartbeat ok")

	healthy := f.evaluate(t, start.Add(time.Minute))
	if healthy.Incident != nil || healthy.Rule.LastOutcome != string(store.OutcomeSignalHealthy) {
		t.Fatalf("first evaluation must be healthy: %+v", healthy.Rule)
	}
	if healthy.Rule.LastSignalAt == nil {
		t.Fatal("healthy evaluation must latch last_signal_at")
	}

	// 9h later the order lines have aged out of the 6h window; heartbeats keep
	// the source demonstrably alive, so this is a real absence.
	first := f.evaluate(t, start.Add(9*time.Hour))
	if !first.NewIncident || !first.NewOccurrence || !first.ShouldNotify {
		t.Fatalf("first absence must raise and notify: %+v", first)
	}
	if first.Incident.ClassKey != f.rule.AbsenceClassKey() {
		t.Fatalf("class key = %q, want %q", first.Incident.ClassKey, f.rule.AbsenceClassKey())
	}
	if first.Incident.TaskID != f.task.ID {
		t.Fatalf("incident must bind the canonical task, got %q", first.Incident.TaskID)
	}
	if first.Rule.ActiveIncidentID != first.Incident.ID {
		t.Fatalf("recovery latch not set: %+v", first.Rule)
	}

	// Same 15-minute bucket: merges into the existing occurrence.
	same := f.evaluate(t, start.Add(9*time.Hour).Add(5*time.Minute))
	if same.NewIncident || same.NewOccurrence {
		t.Fatalf("same bucket must merge: %+v", same)
	}
	if same.Incident.ID != first.Incident.ID {
		t.Fatalf("incident identity drifted: %s -> %s", first.Incident.ID, same.Incident.ID)
	}

	// A later bucket: new occurrence on the SAME incident.
	later := f.evaluate(t, start.Add(9*time.Hour).Add(40*time.Minute))
	if later.NewIncident || !later.NewOccurrence {
		t.Fatalf("later bucket must add an occurrence to the same incident: %+v", later)
	}
	if later.Incident.ID != first.Incident.ID || later.Incident.OccurrenceCount != 2 {
		t.Fatalf("expected one incident with 2 occurrences, got id=%s count=%d",
			later.Incident.ID, later.Incident.OccurrenceCount)
	}
}

// TestExpectedSignalRecoveryClearsIncident: when the signal returns, the
// absence stops firing and the recovery latch clears.
func TestExpectedSignalRecoveryClearsIncident(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	start := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	f.seedLines(t, "tpl-order", store.SeverityInfo, start, "order ingested id=1")
	f.seedLines(t, "tpl-heartbeat", store.SeverityInfo, start.Add(9*time.Hour), "heartbeat ok")
	f.evaluate(t, start.Add(time.Minute))

	raised := f.evaluate(t, start.Add(9*time.Hour))
	if !raised.NewIncident {
		t.Fatalf("expected an absence incident: %+v", raised)
	}

	// The signal returns.
	recoveredAt := start.Add(10 * time.Hour)
	f.seedLines(t, "tpl-order-2", store.SeverityInfo, recoveredAt, "order ingested id=2")
	recovered := f.evaluate(t, recoveredAt.Add(time.Minute))
	if recovered.Incident != nil || recovered.ShouldNotify {
		t.Fatalf("recovery must not raise or notify: %+v", recovered)
	}
	if !recovered.Recovered {
		t.Fatal("recovery must be reported so the caller can close the canonical task")
	}
	if recovered.Rule.ActiveIncidentID != "" {
		t.Fatalf("recovery must clear the latch, got %q", recovered.Rule.ActiveIncidentID)
	}
	if recovered.Rule.LastRecoveredAt == nil || recovered.Rule.LastOutcome != string(store.OutcomeSignalHealthy) {
		t.Fatalf("recovery state not persisted: %+v", recovered.Rule)
	}
	// A second healthy tick is idempotent — no incident, no repeat recovery.
	again := f.evaluate(t, recoveredAt.Add(2*time.Minute))
	if again.Recovered || again.Incident != nil {
		t.Fatalf("steady healthy state must be inert: %+v", again)
	}
}

// TestExpectedSignalCollectionIncidentIsSeparate: broken collection must
// produce a collection-health incident under its OWN class key, never a false
// "the signal stopped" alert merged into the absence incident.
func TestExpectedSignalCollectionIncidentIsSeparate(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	start := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	f.seedLines(t, "tpl-order", store.SeverityInfo, start, "order ingested id=1")
	f.seedLines(t, "tpl-heartbeat", store.SeverityInfo, start.Add(9*time.Hour), "heartbeat ok")
	f.evaluate(t, start.Add(time.Minute))

	absence := f.evaluate(t, start.Add(9*time.Hour))
	if absence.Incident.ClassKey != f.rule.AbsenceClassKey() {
		t.Fatalf("expected an absence class, got %q", absence.Incident.ClassKey)
	}

	// The SSH pull breaks. The truth is now "we cannot see", not "no orders".
	if err := f.db.SetLogSourceFailures(f.ctx, f.source.ID, 4); err != nil {
		t.Fatalf("set failures: %v", err)
	}
	collection := f.evaluate(t, start.Add(10*time.Hour))
	if collection.Incident == nil || collection.Incident.ClassKey != f.rule.CollectionClassKey() {
		t.Fatalf("expected a collection class incident, got %+v", collection.Incident)
	}
	if collection.Incident.ID == absence.Incident.ID {
		t.Fatal("collection health must not merge into the absence incident")
	}
	if !collection.NewIncident || !collection.ShouldNotify {
		t.Fatalf("a new collection incident must notify: %+v", collection)
	}
	if collection.Rule.LastOutcome != string(store.OutcomeSignalCollection) {
		t.Fatalf("last outcome = %q, want collection", collection.Rule.LastOutcome)
	}
	if collection.Occurrence == nil || collection.Occurrence.SourceID != f.source.ID {
		t.Fatalf("collection occurrence must carry the source: %+v", collection.Occurrence)
	}
	// Both incidents survive independently in the ledger.
	if _, err := f.db.GetMonitoringIncidentByClass(f.ctx, f.wsID, f.rule.AbsenceClassKey()); err != nil {
		t.Fatalf("absence incident must still exist: %v", err)
	}
}

// TestExpectedSignalNeverSeenDoesNotFire is the bootstrap guard at DB level: a
// freshly installed rule whose signal has never appeared stays silent even
// though the source is chatty and collection is healthy.
func TestExpectedSignalNeverSeenDoesNotFire(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	f.seedLines(t, "tpl-noise", store.SeverityInfo, now.Add(-time.Hour), "heartbeat ok", "heartbeat ok")

	result := f.evaluate(t, now)
	if result.Incident != nil || result.ShouldNotify {
		t.Fatalf("a never-seen signal must not fire: %+v", result)
	}
	if result.Rule.LastOutcome != string(store.OutcomeSignalAwaitingFirst) {
		t.Fatalf("last outcome = %q, want awaiting_first_signal", result.Rule.LastOutcome)
	}
	if result.Rule.LastSignalAt != nil {
		t.Fatalf("last_signal_at must stay unset, got %v", result.Rule.LastSignalAt)
	}
}

// TestExpectedSignalSilentSourceIsCollectionNotAbsence: pulls report success
// but the source emits nothing at all. We cannot distinguish "no orders" from
// "lost the stream", so the honest output is a collection incident.
func TestExpectedSignalSilentSourceIsCollectionNotAbsence(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	start := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	f.seedLines(t, "tpl-order", store.SeverityInfo, start, "order ingested id=1")
	f.evaluate(t, start.Add(time.Minute))

	result := f.evaluate(t, start.Add(9*time.Hour))
	if result.Incident == nil || result.Incident.ClassKey != f.rule.CollectionClassKey() {
		t.Fatalf("a totally silent source must raise collection, got %+v", result.Incident)
	}
	if result.Rule.LastOutcome != string(store.OutcomeSignalCollection) {
		t.Fatalf("last outcome = %q, want collection", result.Rule.LastOutcome)
	}
}

// TestRecordExpectedSignalOutcomeRejectsRaiseWithoutTask keeps the incident
// ledger's canonical-task invariant intact at the store boundary.
func TestRecordExpectedSignalOutcomeRejectsRaiseWithoutTask(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	decision := store.ExpectedSignalDecision{
		Outcome: store.OutcomeSignalAbsent, Raise: true,
		ClassKey: f.rule.AbsenceClassKey(), Severity: store.SeverityError,
		Title: "orders stopped",
	}
	if _, err := f.db.RecordExpectedSignalOutcome(f.ctx, store.ExpectedSignalRecord{
		RuleID: f.rule.ID, Decision: decision, ObservedAt: now,
	}); err == nil {
		t.Fatal("raising without a task_id must be rejected")
	}
	decision.Severity = "loud"
	if _, err := f.db.RecordExpectedSignalOutcome(f.ctx, store.ExpectedSignalRecord{
		RuleID: f.rule.ID, TaskID: f.task.ID, Decision: decision, ObservedAt: now,
	}); err == nil {
		t.Fatal("an invalid severity must be rejected")
	}
	if _, err := f.db.RecordExpectedSignalOutcome(f.ctx, store.ExpectedSignalRecord{
		RuleID: "missing", Decision: store.ExpectedSignalDecision{Outcome: store.OutcomeSignalHealthy},
		ObservedAt: now,
	}); !errors.Is(err, store.ErrMonitoringExpectedSignalNotFound) {
		t.Fatalf("unknown rule err = %v, want ErrMonitoringExpectedSignalNotFound", err)
	}
}
