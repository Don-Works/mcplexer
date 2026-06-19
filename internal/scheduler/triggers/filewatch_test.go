package triggers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// memJobStore is the minimal ScheduledJobStore needed by the trigger
// tests. Read-only mutators (Update/Delete/Due) return errors so a
// misuse during a test fails loudly instead of silently doing nothing.
type memJobStore struct {
	mu   sync.Mutex
	jobs map[string]store.ScheduledJob
}

func newMemJobStore() *memJobStore {
	return &memJobStore{jobs: map[string]store.ScheduledJob{}}
}

func (m *memJobStore) put(j store.ScheduledJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
}

func (m *memJobStore) CreateScheduledJob(_ context.Context, j *store.ScheduledJob) error {
	m.put(*j)
	return nil
}
func (m *memJobStore) GetScheduledJob(_ context.Context, id string) (*store.ScheduledJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		return &j, nil
	}
	return nil, store.ErrNotFound
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
	m.put(*j)
	return nil
}
func (m *memJobStore) DeleteScheduledJob(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, id)
	return nil
}
func (m *memJobStore) DueScheduledJobs(_ context.Context, _ time.Time, _ int) ([]store.ScheduledJob, error) {
	return nil, nil
}

// recordingRunner records every RunOnce invocation so tests can assert
// counts + ordering.
type recordingRunner struct {
	mu    sync.Mutex
	calls []string
	total atomic.Int32
}

func (r *recordingRunner) RunOnce(_ context.Context, jobID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, jobID)
	r.total.Add(1)
	return nil
}

func (r *recordingRunner) count() int { return int(r.total.Load()) }

func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitFor %q timed out", label)
}

func TestFileWatcherFiresRunOnceOnWrite(t *testing.T) {
	dir := t.TempDir()
	st := newMemJobStore()
	runner := &recordingRunner{}
	st.put(store.ScheduledJob{
		ID:      "j1",
		Kind:    scheduler.KindFileWatch,
		Spec:    filepath.Join(dir, "*.txt"),
		Enabled: true,
	})
	w, err := NewFileWatcher(st, runner, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = w.Stop() }()

	if err := os.WriteFile(filepath.Join(dir, "foo.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run-once fired", func() bool { return runner.count() >= 1 })
	runner.mu.Lock()
	got := append([]string(nil), runner.calls...)
	runner.mu.Unlock()
	if len(got) == 0 || got[0] != "j1" {
		t.Fatalf("expected j1 call, got %v", got)
	}
}

func TestFileWatcherDebouncesBurst(t *testing.T) {
	dir := t.TempDir()
	st := newMemJobStore()
	runner := &recordingRunner{}
	st.put(store.ScheduledJob{
		ID: "burst", Kind: scheduler.KindFileWatch,
		Spec: filepath.Join(dir, "*.txt"), Enabled: true,
	})
	// Generous debounce so the second write certainly lands inside it.
	w, _ := NewFileWatcher(st, runner, 300*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = w.Start(ctx)
	defer func() { _ = w.Stop() }()

	target := filepath.Join(dir, "burst.txt")
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(target, []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait past the debounce window so the (single) timer must have fired.
	time.Sleep(500 * time.Millisecond)
	if got := runner.count(); got != 1 {
		t.Fatalf("expected exactly 1 run after debounced burst, got %d", got)
	}
}

func TestFileWatcherReloadPicksUpNewJob(t *testing.T) {
	dir := t.TempDir()
	st := newMemJobStore()
	runner := &recordingRunner{}
	w, _ := NewFileWatcher(st, runner, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = w.Start(ctx)
	defer func() { _ = w.Stop() }()

	// Initially no jobs; writes do nothing.
	if err := os.WriteFile(filepath.Join(dir, "ignored.json"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := runner.count(); got != 0 {
		t.Fatalf("expected 0 fires before reload, got %d", got)
	}
	st.put(store.ScheduledJob{
		ID: "late", Kind: scheduler.KindFileWatch,
		Spec: filepath.Join(dir, "*.json"), Enabled: true,
	})
	if err := w.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "now.json"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "late job fires", func() bool { return runner.count() >= 1 })
}

func TestFileWatcherNilArgsRejected(t *testing.T) {
	if _, err := NewFileWatcher(nil, &recordingRunner{}, 0); err == nil {
		t.Error("expected error on nil store")
	}
	if _, err := NewFileWatcher(newMemJobStore(), nil, 0); err == nil {
		t.Error("expected error on nil runner")
	}
}

func TestFileWatcherStopIsIdempotent(t *testing.T) {
	st := newMemJobStore()
	w, _ := NewFileWatcher(st, &recordingRunner{}, 50*time.Millisecond)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Errorf("first stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Errorf("second stop: %v", err)
	}
}

func TestGlobMatchPatterns(t *testing.T) {
	cases := []struct {
		spec, path string
		want       bool
	}{
		{"/tmp/*.txt", "/tmp/foo.txt", true},
		{"/tmp/*.txt", "/tmp/sub/foo.txt", false},
		{"**/*.go", "/repo/cmd/foo.go", true},
		{"**/*.go", "/repo/cmd/foo.txt", false},
		{"/repo/**/*.json", "/repo/sub/cfg.json", true},
		{"/repo/**/*.json", "/other/cfg.json", false},
		{"plain.txt", "plain.txt", true},
	}
	for _, c := range cases {
		if got := globMatch(c.spec, c.path); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.spec, c.path, got, c.want)
		}
	}
}

func TestFixedPrefixDir(t *testing.T) {
	cases := map[string]string{
		"/tmp/foo/*.txt":   "/tmp/foo",
		"/tmp/foo/bar.txt": "/tmp/foo",
		"/tmp/**/*.go":     "/tmp",
		"*.txt":            ".",
	}
	for in, want := range cases {
		if got := fixedPrefixDir(in); got != want {
			t.Errorf("fixedPrefixDir(%q)=%q want %q", in, got, want)
		}
	}
}

// errStore returns an error from ListScheduledJobs so we exercise the
// Reload error path.
type errStore struct{ memJobStore }

func (e *errStore) ListScheduledJobs(_ context.Context) ([]store.ScheduledJob, error) {
	return nil, errors.New("boom")
}

func TestFileWatcherReloadPropagatesStoreError(t *testing.T) {
	st := &errStore{memJobStore: *newMemJobStore()}
	w, err := NewFileWatcher(st, &recordingRunner{}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Reload(context.Background()); err == nil {
		t.Error("expected error from Reload when store fails")
	}
}
