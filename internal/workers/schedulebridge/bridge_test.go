package schedulebridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// memJobStore is a minimal in-memory store.ScheduledJobStore satisfying
// the bridge's needs.
type memJobStore struct {
	mu   sync.Mutex
	jobs map[string]store.ScheduledJob
}

func newMemJobStore() *memJobStore {
	return &memJobStore{jobs: map[string]store.ScheduledJob{}}
}

func (m *memJobStore) CreateScheduledJob(_ context.Context, j *store.ScheduledJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; ok {
		return store.ErrAlreadyExists
	}
	m.jobs[j.ID] = *j
	return nil
}

func (m *memJobStore) GetScheduledJob(_ context.Context, id string) (*store.ScheduledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &j, nil
}

func (m *memJobStore) ListScheduledJobs(_ context.Context) ([]store.ScheduledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.ScheduledJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out, nil
}

func (m *memJobStore) UpdateScheduledJob(_ context.Context, j *store.ScheduledJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return store.ErrNotFound
	}
	m.jobs[j.ID] = *j
	return nil
}

func (m *memJobStore) DeleteScheduledJob(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.jobs, id)
	return nil
}

func (m *memJobStore) DueScheduledJobs(_ context.Context, _ time.Time, _ int) ([]store.ScheduledJob, error) {
	return nil, nil
}

// kickerSpy counts reloads so tests can assert the bridge nudges the
// scheduler after a write.
type kickerSpy struct {
	reloads atomic.Int32
}

func (k *kickerSpy) Reload(_ context.Context) error {
	k.reloads.Add(1)
	return nil
}

func mkWorker(id, spec string, enabled bool) *store.Worker {
	return &store.Worker{
		ID:           id,
		Name:         "test-" + id,
		ScheduleSpec: spec,
		Enabled:      enabled,
	}
}

func TestEnsureForWorkerCreatesRow(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-a", "5m", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 1 {
		t.Fatalf("len jobs = %d, want 1", len(list))
	}
	got := list[0]
	if got.Kind != scheduler.KindWorker || got.WorkerID != w.ID {
		t.Errorf("kind/worker_id = %q/%q, want worker/%s", got.Kind, got.WorkerID, w.ID)
	}
	if got.Spec != "5m" {
		t.Errorf("spec = %q, want 5m", got.Spec)
	}
	if got.Surface != "worker" {
		t.Errorf("surface = %q, want worker", got.Surface)
	}
	if got.NextRunAt == nil {
		t.Error("next_run_at must be populated for a parseable interval spec")
	}
	if kick.reloads.Load() != 1 {
		t.Errorf("reloads = %d, want 1", kick.reloads.Load())
	}
}

func TestEnsureForWorkerIdempotent(t *testing.T) {
	jobs := newMemJobStore()
	b := New(jobs, nil)
	w := mkWorker("wkr-i", "1m", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 1 {
		t.Errorf("idempotent ensure produced %d rows, want 1", len(list))
	}
}

func TestEnsureForWorkerUpdatesSpec(t *testing.T) {
	jobs := newMemJobStore()
	b := New(jobs, nil)
	w := mkWorker("wkr-u", "1m", true)
	_ = b.EnsureForWorker(context.Background(), w)

	// Now change the spec.
	w.ScheduleSpec = "10m"
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("update ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if list[0].Spec != "10m" {
		t.Errorf("spec = %q, want 10m", list[0].Spec)
	}
}

func TestEnsureForWorkerHandlesBadSpec(t *testing.T) {
	jobs := newMemJobStore()
	b := New(jobs, nil)
	w := mkWorker("wkr-bad", "not-a-spec", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("ensure must succeed even with bad spec: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if list[0].NextRunAt != nil {
		t.Error("next_run_at must be nil for unparseable spec")
	}
}

// TestEnsureForWorkerManualSkipsRowCreation: a worker with the
// "manual" sentinel must never get a scheduled_jobs row — the
// scheduler heap must not see it.
func TestEnsureForWorkerManualSkipsRowCreation(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-manual", scheduler.SpecManual, true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("manual worker created %d rows, want 0", len(list))
	}
	if kick.reloads.Load() != 0 {
		t.Errorf("manual worker triggered %d reloads, want 0", kick.reloads.Load())
	}
}

// TestEnsureForWorkerFlipToManualDeletesRow: an operator flipping
// schedule_spec from "5m" to "manual" must remove the existing
// scheduled_jobs row so the heap stops firing it.
func TestEnsureForWorkerFlipToManualDeletesRow(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-flip", "5m", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("initial ensure: %v", err)
	}
	if list, _ := jobs.ListScheduledJobs(context.Background()); len(list) != 1 {
		t.Fatalf("pre-flip len = %d, want 1", len(list))
	}

	// Flip to manual.
	w.ScheduleSpec = scheduler.SpecManual
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("flip ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("post-flip rows = %d, want 0", len(list))
	}
	// One reload for the create, one for the delete.
	if kick.reloads.Load() != 2 {
		t.Errorf("reloads = %d, want 2", kick.reloads.Load())
	}
}

func TestEnsureForWorkerDisabledDeletesRow(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-disabled", "5m", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("initial ensure: %v", err)
	}

	w.Enabled = false
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("disabled ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("disabled worker left %d schedule rows, want 0", len(list))
	}
	if kick.reloads.Load() != 2 {
		t.Errorf("reloads = %d, want 2", kick.reloads.Load())
	}
}

func TestEnsureForWorkerArchivedDeletesRow(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-archived", "5m", true)
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("initial ensure: %v", err)
	}

	now := time.Now().UTC()
	w.Enabled = false
	w.ArchivedAt = &now
	if err := b.EnsureForWorker(context.Background(), w); err != nil {
		t.Fatalf("archived ensure: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("archived worker left %d schedule rows, want 0", len(list))
	}
	if kick.reloads.Load() != 2 {
		t.Errorf("reloads = %d, want 2", kick.reloads.Load())
	}
}

func TestRemoveForWorker(t *testing.T) {
	jobs := newMemJobStore()
	kick := &kickerSpy{}
	b := New(jobs, kick)
	w := mkWorker("wkr-r", "5m", true)
	_ = b.EnsureForWorker(context.Background(), w)
	if err := b.RemoveForWorker(context.Background(), w.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("len = %d, want 0", len(list))
	}
	// Idempotent — removing a non-existent worker is fine.
	if err := b.RemoveForWorker(context.Background(), w.ID); err != nil {
		t.Errorf("second remove errored: %v", err)
	}
}

// fakeWorkerLister satisfies WorkerLister + WorkspaceLister so we can
// drive ResyncAllEnabled deterministically.
type fakeWorkerLister struct {
	workspaces []store.Workspace
	byWS       map[string][]*store.Worker
}

func (f *fakeWorkerLister) ListWorkspaces(_ context.Context) ([]store.Workspace, error) {
	return f.workspaces, nil
}

func (f *fakeWorkerLister) ListWorkers(_ context.Context, wsID string, enabledOnly bool) ([]*store.Worker, error) {
	out := make([]*store.Worker, 0)
	for _, w := range f.byWS[wsID] {
		if enabledOnly && !w.Enabled {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func TestResyncAllEnabled(t *testing.T) {
	jobs := newMemJobStore()
	b := New(jobs, nil)
	lister := &fakeWorkerLister{
		workspaces: []store.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
		byWS: map[string][]*store.Worker{
			"ws-1": {mkWorker("wkr-1", "5m", true), mkWorker("wkr-2", "1h", false)},
			"ws-2": {mkWorker("wkr-3", "0 9 * * *", true)},
		},
	}
	if err := b.ResyncAllEnabled(context.Background(), lister, lister); err != nil {
		t.Fatalf("resync: %v", err)
	}
	list, _ := jobs.ListScheduledJobs(context.Background())
	if len(list) != 2 {
		t.Fatalf("resync produced %d rows, want 2 (only enabled workers)", len(list))
	}
	have := map[string]bool{}
	for _, j := range list {
		have[j.WorkerID] = true
	}
	if !have["wkr-1"] || !have["wkr-3"] {
		t.Errorf("missing expected workers: %+v", have)
	}
}
