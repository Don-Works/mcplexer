package baseline

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// Doubles and fixtures for the evaluator tests. Split out of evaluate_test.go
// to keep every file in this package inside the 300-line gate.

// fakeEvalStore is a hand-rolled stand-in for the sqlite store, holding just
// enough state to exercise the incident-convergence and recovery edges. It
// reproduces the real store's contract on the two things that matter: raising
// converges on ONE incident per class key, and recovery clears the latch.
type fakeEvalStore struct {
	rule      *store.MonitoringExpectedSignal
	source    *store.LogSource
	observed  store.ExpectedSignalObservation
	health    store.SourceCollectionHealth
	incidents map[string]*store.MonitoringIncident

	records    []store.ExpectedSignalRecord
	observeErr error
	sourceErr  error
}

func newFakeEvalStore(rule *store.MonitoringExpectedSignal) *fakeEvalStore {
	return &fakeEvalStore{
		rule:      rule,
		source:    &store.LogSource{ID: rule.SourceID, Name: "orders-api", Selector: "unit=orders"},
		incidents: map[string]*store.MonitoringIncident{},
	}
}

func (f *fakeEvalStore) GetLogSource(_ context.Context, id string) (*store.LogSource, error) {
	if f.sourceErr != nil {
		return nil, f.sourceErr
	}
	if f.source != nil && f.source.ID == id {
		return f.source, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeEvalStore) ListEnabledMonitoringExpectedSignals(
	context.Context,
) ([]*store.MonitoringExpectedSignal, error) {
	return []*store.MonitoringExpectedSignal{f.rule}, nil
}

func (f *fakeEvalStore) ObserveExpectedSignal(
	_ context.Context, _ *store.MonitoringExpectedSignal, _ time.Time,
) (store.ExpectedSignalObservation, store.SourceCollectionHealth, error) {
	return f.observed, f.health, f.observeErr
}

func (f *fakeEvalStore) RecordExpectedSignalOutcome(
	_ context.Context, in store.ExpectedSignalRecord,
) (*store.ExpectedSignalResult, error) {
	f.records = append(f.records, in)
	result := &store.ExpectedSignalResult{Rule: f.rule}
	if in.Decision.Raise {
		incident, existed := f.incidents[in.Decision.ClassKey]
		if !existed {
			incident = &store.MonitoringIncident{
				ID: "incident-" + in.Decision.ClassKey, ClassKey: in.Decision.ClassKey,
				WorkspaceID: f.rule.WorkspaceID, TaskID: in.TaskID,
				Severity: in.Decision.Severity, Title: in.Decision.Title,
			}
			f.incidents[in.Decision.ClassKey] = incident
		}
		incident.OccurrenceCount++
		result.Incident = incident
		result.NewIncident = !existed
		result.ShouldNotify = !existed
		result.EffectiveSeverity = in.Decision.Severity
		f.rule.ActiveIncidentID = incident.ID
		return result, nil
	}
	result.Recovered = in.Decision.SignalPresent && f.rule.ActiveIncidentID != ""
	if result.Recovered {
		f.rule.ActiveIncidentID = ""
	}
	return result, nil
}

func (f *fakeEvalStore) GetMonitoringIncidentByClass(
	_ context.Context, _, classKey string,
) (*store.MonitoringIncident, error) {
	if incident, ok := f.incidents[classKey]; ok {
		return incident, nil
	}
	return nil, store.ErrMonitoringIncidentNotFound
}

// Unused CRUD, present to satisfy store.MonitoringExpectedSignalStore.
func (f *fakeEvalStore) CreateMonitoringExpectedSignal(
	context.Context, *store.MonitoringExpectedSignal) error {
	return nil
}

func (f *fakeEvalStore) GetMonitoringExpectedSignal(
	_ context.Context, id string) (*store.MonitoringExpectedSignal, error) {
	if f.rule != nil && f.rule.ID == id {
		return f.rule, nil
	}
	return nil, store.ErrMonitoringExpectedSignalNotFound
}

func (f *fakeEvalStore) ListMonitoringExpectedSignals(
	context.Context, string) ([]*store.MonitoringExpectedSignal, error) {
	return []*store.MonitoringExpectedSignal{f.rule}, nil
}

func (f *fakeEvalStore) UpdateMonitoringExpectedSignal(
	context.Context, *store.MonitoringExpectedSignal) error {
	return nil
}

func (f *fakeEvalStore) DeleteMonitoringExpectedSignal(context.Context, string) error { return nil }

// fakeTasks elects one task per class key, exactly as the real canonical-task
// election does — so a per-tick task explosion shows up as a test failure.
type fakeTasks struct {
	byClass map[string]string
	titles  map[string]string
	bodies  map[string]string
	creates int
	closed  []string
}

func newFakeTasks() *fakeTasks {
	return &fakeTasks{byClass: map[string]string{}, titles: map[string]string{}, bodies: map[string]string{}}
}

func (f *fakeTasks) Ensure(
	_ context.Context, _, classKey, title, body, _ string,
) (string, error) {
	if id, ok := f.byClass[classKey]; ok {
		return id, nil
	}
	f.creates++
	id := "task-" + classKey
	f.byClass[classKey] = id
	f.titles[classKey] = title
	f.bodies[classKey] = body
	return id, nil
}

func (f *fakeTasks) Close(_ context.Context, _, taskID, _ string) error {
	f.closed = append(f.closed, taskID)
	return nil
}

type fakeNotifier struct{ sent []distill.Notification }

func (f *fakeNotifier) Notify(_ context.Context, n distill.Notification) error {
	f.sent = append(f.sent, n)
	return nil
}

// learnedRule is what the learner promotes for a ten-minute job: a one-hour
// window, always-on, liveness required.
func learnedRule() *store.MonitoringExpectedSignal {
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rule := &store.MonitoringExpectedSignal{
		ID: "rule-1", WorkspaceID: "ws-1", SourceID: "src-1",
		Name: "auto/tpl-orders-s", MatchSubstring: "order sync completed batch=",
		MinCount: 1, WindowSeconds: 3600, Severity: store.SeverityError,
		RequireSourceLiveness: true, Enabled: true, CreatedAt: created,
	}
	store.ApplyExpectedSignalDefaults(rule)
	return rule
}

func newTestEvaluator(
	st *fakeEvalStore,
) (*Evaluator, *fakeTasks, *fakeNotifier) {
	tasks, notifier := newFakeTasks(), &fakeNotifier{}
	e := NewEvaluator(st, tasks, notifier)
	e.now = func() time.Time { return time.Date(2026, 7, 20, 3, 0, 0, 0, time.UTC) }
	return e, tasks, notifier
}
