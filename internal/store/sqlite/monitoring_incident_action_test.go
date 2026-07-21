package sqlite_test

// Integration coverage for the operator incident actions (migration 150),
// driving the public store methods against a real SQLite database through the
// same fixtures the renotify and expected-signal tests use. The assertions are
// the session's hard rules: a pause mutes the nag but never an escalation, a
// silence auto-expires, a dismiss resolves yet a recurrence still fires, and
// every pause is visible and attributed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// triageSeed is one incident class whose severity the test controls across
// successive observations. The template and canonical task are created once so
// re-recording only moves the incident, not the mapped-occurrence bookkeeping.
type triageSeed struct {
	f        *renotifyFixture
	classKey string
	tplID    string
	taskID   string
}

func newTriageSeed(t *testing.T, f *renotifyFixture, tplID, classKey string) *triageSeed {
	t.Helper()
	tpl := &store.LogTemplate{
		ID: tplID, SourceID: f.source.ID, Masked: "sync tick <n>",
		Severity: store.SeverityError, FirstSeen: incidentStart, LastSeen: incidentStart,
		SampleFirst: "sync tick 1", SampleLast: "sync tick 1",
	}
	if _, err := f.db.UpsertLogTemplate(f.ctx, tpl, 1); err != nil {
		t.Fatalf("upsert template: %v", err)
	}
	task := &store.Task{WorkspaceID: f.wsID, Title: "sync stalled"}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return &triageSeed{f: f, classKey: classKey, tplID: tplID, taskID: task.ID}
}

func (s *triageSeed) record(t *testing.T, severity string, at time.Time) *store.MonitoringTriageResult {
	t.Helper()
	res, err := s.f.db.RecordMonitoringTriage(s.f.ctx, store.MonitoringTriageRecord{
		WorkspaceID: s.f.wsID, ClassKey: s.classKey, TaskID: s.taskID,
		Disposition: store.MonitoringDispositionActionable, Severity: severity,
		Title: "sync stalled", SourceID: s.f.source.ID,
		TemplateIDs: []string{s.tplID}, Evidence: "observed", ObservedAt: at,
	})
	if err != nil {
		t.Fatalf("record triage at %v: %v", at, err)
	}
	return res
}

// sustained records three observations across distinct 15m buckets so the
// incident is sustained and, at +90m from start, due for a persistent re-notify.
func (s *triageSeed) sustained(t *testing.T) *store.MonitoringIncident {
	t.Helper()
	s.record(t, store.SeverityError, incidentStart)
	s.record(t, store.SeverityError, incidentStart.Add(20*time.Minute))
	inc := s.record(t, store.SeverityError, incidentStart.Add(40*time.Minute)).Incident
	s.f.markNotified(t, inc.ID, store.SeverityError, incidentStart)
	return inc
}

func ref(f *renotifyFixture, id, actor string, at time.Time) store.MonitoringIncidentActionRef {
	return store.MonitoringIncidentActionRef{WorkspaceID: f.wsID, IncidentID: id, Actor: actor, At: at}
}

func TestIncidentAckPausesRenotifyButNotEscalation(t *testing.T) {
	f := newRenotifyFixture(t)
	s := newTriageSeed(t, f, "tpl-ack", "correlation:orders|ack")
	inc := s.sustained(t)
	now := incidentStart.Add(90 * time.Minute)
	if len(f.due(t, now, 10)) != 1 {
		t.Fatal("incident should be due before ack")
	}
	view, err := f.db.AckMonitoringIncident(f.ctx, ref(f, inc.ID, "max", now))
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if view.AckedAt == nil || view.AckedBy != "max" {
		t.Fatalf("ack not attributed: %+v", view.MonitoringIncident)
	}
	if len(f.due(t, now, 10)) != 0 {
		t.Fatal("ack must pause the routine re-notify")
	}
	// A classifier escalation to critical must pierce the ack.
	esc := incidentStart.Add(100 * time.Minute)
	s.record(t, store.SeverityCritical, esc)
	due := f.due(t, esc, 10)
	if len(due) != 1 || due[0].NotificationReason != "severity_escalation" {
		t.Fatalf("escalation must pierce ack: %+v", due)
	}
}

func TestIncidentSilenceAutoExpiresThenRenotifies(t *testing.T) {
	f := newRenotifyFixture(t)
	s := newTriageSeed(t, f, "tpl-sil", "correlation:orders|sil")
	inc := s.sustained(t)
	at := incidentStart.Add(50 * time.Minute)
	view, err := f.db.SilenceMonitoringIncident(f.ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: ref(f, inc.ID, "max", at), Duration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("silence: %v", err)
	}
	if view.SilencedUntil == nil || view.SilencedBy != "max" {
		t.Fatalf("silence not attributed: %+v", view.MonitoringIncident)
	}
	if len(f.due(t, incidentStart.Add(70*time.Minute), 10)) != 0 {
		t.Fatal("silence must mute re-notify within its window")
	}
	// Past expiry, still active: the incident must speak again.
	due := f.due(t, incidentStart.Add(90*time.Minute), 10)
	if len(due) != 1 || due[0].NotificationReason != "persistent_incident" {
		t.Fatalf("expired silence must re-notify a live incident: %+v", due)
	}
}

func TestIncidentUnsilenceRestoresRenotify(t *testing.T) {
	f := newRenotifyFixture(t)
	s := newTriageSeed(t, f, "tpl-uns", "correlation:orders|uns")
	inc := s.sustained(t)
	at := incidentStart.Add(50 * time.Minute)
	if _, err := f.db.SilenceMonitoringIncident(f.ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: ref(f, inc.ID, "max", at), Duration: 2 * time.Hour,
	}); err != nil {
		t.Fatalf("silence: %v", err)
	}
	if len(f.due(t, incidentStart.Add(70*time.Minute), 10)) != 0 {
		t.Fatal("silence should mute before unsilence")
	}
	if _, err := f.db.UnsilenceMonitoringIncident(f.ctx, ref(f, inc.ID, "max", incidentStart.Add(75*time.Minute))); err != nil {
		t.Fatalf("unsilence: %v", err)
	}
	if len(f.due(t, incidentStart.Add(90*time.Minute), 10)) != 1 {
		t.Fatal("unsilence must restore re-notify")
	}
}

func TestIncidentDismissResolvesAndRecurrenceFires(t *testing.T) {
	f := newRenotifyFixture(t)
	s := newTriageSeed(t, f, "tpl-dis", "correlation:orders|dis")
	inc := s.sustained(t)
	res, err := f.db.DismissMonitoringIncident(f.ctx, store.MonitoringIncidentDismissInput{
		MonitoringIncidentActionRef: ref(f, inc.ID, "max", incidentStart.Add(90*time.Minute)),
		StatusText:                  "handled",
	})
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if res.Outcome != store.MonitoringOutcomeBenign || res.ResolvedByActor != "max" {
		t.Fatalf("dismiss did not resolve as attributed benign: %+v", res)
	}
	got, err := f.db.GetMonitoringIncident(f.ctx, f.wsID, inc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Disposition != store.MonitoringDispositionBenign {
		t.Fatalf("dismiss must mark disposition benign, got %q", got.Disposition)
	}
	if len(f.due(t, incidentStart.Add(120*time.Minute), 10)) != 0 {
		t.Fatal("dismissed incident must be muted")
	}
	// A later recurrence of the SAME class must fire again — dismiss is not a
	// permanent blacklist.
	rec := s.record(t, store.SeverityError, incidentStart.Add(3*time.Hour))
	if !rec.ShouldNotify {
		t.Fatal("recurrence of a dismissed class must fire")
	}
	if rec.Incident.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("recurrence must lift the benign suppression, got %q", rec.Incident.Disposition)
	}
}

func TestListSuppressedIncidentsListsAckAndSilence(t *testing.T) {
	f := newRenotifyFixture(t)
	now := incidentStart.Add(90 * time.Minute)
	sA := newTriageSeed(t, f, "tpl-a", "correlation:orders|a")
	sB := newTriageSeed(t, f, "tpl-b", "correlation:orders|b")
	sC := newTriageSeed(t, f, "tpl-c", "correlation:orders|c")
	incA, incB, incC := sA.sustained(t), sB.sustained(t), sC.sustained(t)
	if _, err := f.db.AckMonitoringIncident(f.ctx, ref(f, incA.ID, "max", now)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := f.db.SilenceMonitoringIncident(f.ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: ref(f, incB.ID, "sam", now), Duration: 2 * time.Hour,
	}); err != nil {
		t.Fatalf("silence: %v", err)
	}
	if _, err := f.db.DismissMonitoringIncident(f.ctx, store.MonitoringIncidentDismissInput{
		MonitoringIncidentActionRef: ref(f, incC.ID, "max", now),
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	suppressed, err := f.db.ListSuppressedMonitoringIncidents(f.ctx, f.wsID, now, 100)
	if err != nil {
		t.Fatalf("list suppressed: %v", err)
	}
	if len(suppressed) != 2 {
		t.Fatalf("want ack+silence in suppressed list, got %d: %+v", len(suppressed), suppressed)
	}
	seen := map[string]*store.MonitoringIncidentView{}
	for _, sup := range suppressed {
		seen[sup.ID] = sup
	}
	if a := seen[incA.ID]; a == nil || !a.AckActive || a.AckedBy != "max" {
		t.Fatalf("acked incident missing/misattributed: %+v", a)
	}
	if b := seen[incB.ID]; b == nil || !b.SilenceActive || b.SilencedUntil == nil {
		t.Fatalf("silenced incident missing/misattributed: %+v", b)
	}
	if _, ok := seen[incC.ID]; ok {
		t.Fatal("dismissed incident must not appear as ack/silence-suppressed")
	}
	// Dismiss is visible on the resolution surface instead.
	dismissed, err := f.db.ListMonitoringResolutions(f.ctx, f.wsID, true, 100)
	if err != nil {
		t.Fatalf("list resolutions: %v", err)
	}
	if len(dismissed) != 1 || dismissed[0].IncidentID != incC.ID {
		t.Fatalf("dismissed incident must appear on the resolution surface: %+v", dismissed)
	}
}

func TestIncidentActionValidation(t *testing.T) {
	f := newRenotifyFixture(t)
	s := newTriageSeed(t, f, "tpl-v", "correlation:orders|v")
	inc := s.sustained(t)
	now := incidentStart.Add(90 * time.Minute)
	// Unbounded silence is forbidden — zero and beyond-max both rejected.
	for _, d := range []time.Duration{0, 8 * 24 * time.Hour} {
		if _, err := f.db.SilenceMonitoringIncident(f.ctx, store.MonitoringIncidentSilenceInput{
			MonitoringIncidentActionRef: ref(f, inc.ID, "max", now), Duration: d,
		}); !errors.Is(err, store.ErrMonitoringSilenceUnbounded) {
			t.Fatalf("silence(%s) err = %v, want ErrMonitoringSilenceUnbounded", d, err)
		}
	}
	// Actor is mandatory.
	if _, err := f.db.AckMonitoringIncident(f.ctx, ref(f, inc.ID, "", now)); !errors.Is(err, store.ErrMonitoringActionActorRequired) {
		t.Fatalf("ack without actor err = %v, want ErrMonitoringActionActorRequired", err)
	}
	// An absent/foreign incident is not-found, not a 500.
	if _, err := f.db.AckMonitoringIncident(f.ctx, ref(f, "missing", "max", now)); !errors.Is(err, store.ErrMonitoringIncidentNotFound) {
		t.Fatalf("ack missing incident err = %v, want ErrMonitoringIncidentNotFound", err)
	}
}

// TestDismissedAbsenceClassRecurrenceFires proves the expected-signal (absence)
// path breaks a dismiss on recurrence, exactly like the template path — without
// it, a dismissed "signal stopped" incident would be muted forever.
func TestDismissedAbsenceClassRecurrenceFires(t *testing.T) {
	f := seedExpectedSignalFixture(t)
	actions, ok := f.db.(store.MonitoringIncidentActionStore)
	if !ok {
		t.Fatal("store does not implement MonitoringIncidentActionStore")
	}
	raise := func(at time.Time) *store.ExpectedSignalResult {
		res, err := f.db.RecordExpectedSignalOutcome(f.ctx, store.ExpectedSignalRecord{
			RuleID: f.rule.ID, TaskID: f.task.ID, ObservedAt: at,
			Decision: store.ExpectedSignalDecision{
				Outcome: store.OutcomeSignalAbsent, Raise: true,
				ClassKey: f.rule.AbsenceClassKey(), Severity: store.SeverityError,
				Title: "orders ingest stopped", Reason: store.ReasonNoMatches,
			},
		})
		if err != nil {
			t.Fatalf("raise absence at %v: %v", at, err)
		}
		return res
	}
	t0 := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	inc := raise(t0).Incident
	if _, err := actions.DismissMonitoringIncident(context.Background(), store.MonitoringIncidentDismissInput{
		MonitoringIncidentActionRef: store.MonitoringIncidentActionRef{
			WorkspaceID: f.wsID, IncidentID: inc.ID, Actor: "max", At: t0.Add(time.Minute),
		},
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	rec := raise(t0.Add(2 * time.Hour))
	if !rec.ShouldNotify {
		t.Fatal("recurrence of a dismissed absence class must fire")
	}
	if rec.Incident.Disposition != store.MonitoringDispositionActionable {
		t.Fatalf("absence recurrence must lift benign, got %q", rec.Incident.Disposition)
	}
}
