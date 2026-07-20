package baseline

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeLearnerStore is an in-memory stand-in for *sqlite.DB, for the LEARNER
// half of this package. No database, no SSH, no clock: a pass is driven off a
// fixed candidate slice, so its outcome is pure control flow over the promotion
// ladder.
//
// The evaluator half has its own double (fakeEvalStore in evaluate_test.go)
// because it needs incident bookkeeping this one has no use for.
type fakeLearnerStore struct {
	sources    []*store.LogSource
	candidates map[string][]store.BaselineCandidate // source id -> mined evidence
	mineErr    error

	baselines map[string]*store.SignalBaseline           // template id -> row
	rules     map[string]*store.MonitoringExpectedSignal // rule id -> row

	// Call ledgers the assertions read.
	created  []*store.MonitoringExpectedSignal
	updated  []*store.MonitoringExpectedSignal
	upserted []*store.SignalBaseline
	nextID   int
}

func newFakeLearnerStore() *fakeLearnerStore {
	return &fakeLearnerStore{
		candidates: map[string][]store.BaselineCandidate{},
		baselines:  map[string]*store.SignalBaseline{},
		rules:      map[string]*store.MonitoringExpectedSignal{},
	}
}

// --- store.MonitoringBaselineStore ---

func (f *fakeLearnerStore) ListEnabledLogSources(context.Context) ([]*store.LogSource, error) {
	return f.sources, nil
}

func (f *fakeLearnerStore) MineBaselineCandidates(
	_ context.Context, src *store.LogSource, _, _ time.Time,
) ([]store.BaselineCandidate, error) {
	if f.mineErr != nil {
		return nil, f.mineErr
	}
	return f.candidates[src.ID], nil
}

func (f *fakeLearnerStore) UpsertSignalBaseline(_ context.Context, b *store.SignalBaseline) error {
	clone := *b
	f.baselines[b.TemplateID] = &clone
	f.upserted = append(f.upserted, &clone)
	return nil
}

func (f *fakeLearnerStore) GetSignalBaselineByTemplate(
	_ context.Context, templateID string,
) (*store.SignalBaseline, error) {
	if b, ok := f.baselines[templateID]; ok {
		return b, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeLearnerStore) ListSignalBaselines(
	_ context.Context, workspaceID string, _ int,
) ([]*store.SignalBaseline, error) {
	out := []*store.SignalBaseline{}
	for _, b := range f.baselines {
		if b.WorkspaceID == workspaceID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeLearnerStore) ListSignalBaselinesForSource(
	_ context.Context, sourceID string, _ int,
) ([]*store.SignalBaseline, error) {
	out := []*store.SignalBaseline{}
	for _, b := range f.baselines {
		if b.SourceID == sourceID {
			out = append(out, b)
		}
	}
	return out, nil
}

// --- store.MonitoringExpectedSignalStore ---

func (f *fakeLearnerStore) CreateMonitoringExpectedSignal(
	_ context.Context, r *store.MonitoringExpectedSignal,
) error {
	if r.ID == "" {
		f.nextID++
		r.ID = "rule-" + time.Duration(f.nextID).String()
	}
	clone := *r
	f.rules[r.ID] = &clone
	f.created = append(f.created, &clone)
	return nil
}

func (f *fakeLearnerStore) GetMonitoringExpectedSignal(
	_ context.Context, id string,
) (*store.MonitoringExpectedSignal, error) {
	if r, ok := f.rules[id]; ok {
		return r, nil
	}
	return nil, store.ErrMonitoringExpectedSignalNotFound
}

func (f *fakeLearnerStore) ListMonitoringExpectedSignals(
	_ context.Context, workspaceID string,
) ([]*store.MonitoringExpectedSignal, error) {
	out := []*store.MonitoringExpectedSignal{}
	for _, r := range f.rules {
		if r.WorkspaceID == workspaceID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeLearnerStore) ListEnabledMonitoringExpectedSignals(
	context.Context,
) ([]*store.MonitoringExpectedSignal, error) {
	out := []*store.MonitoringExpectedSignal{}
	for _, r := range f.rules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeLearnerStore) UpdateMonitoringExpectedSignal(
	_ context.Context, r *store.MonitoringExpectedSignal,
) error {
	clone := *r
	f.rules[r.ID] = &clone
	f.updated = append(f.updated, &clone)
	return nil
}

func (f *fakeLearnerStore) DeleteMonitoringExpectedSignal(_ context.Context, id string) error {
	delete(f.rules, id)
	return nil
}

// ObserveExpectedSignal and RecordExpectedSignalOutcome exist only to satisfy
// store.MonitoringExpectedSignalStore. The learner never evaluates a rule — that
// is the evaluator's job and a different tick — so a call here is a wiring bug
// and the tests should hear about it loudly.
func (f *fakeLearnerStore) ObserveExpectedSignal(
	context.Context, *store.MonitoringExpectedSignal, time.Time,
) (store.ExpectedSignalObservation, store.SourceCollectionHealth, error) {
	panic("learner must not observe expected signals")
}

func (f *fakeLearnerStore) RecordExpectedSignalOutcome(
	context.Context, store.ExpectedSignalRecord,
) (*store.ExpectedSignalResult, error) {
	panic("learner must not record expected-signal outcomes")
}
