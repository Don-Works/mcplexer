package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

type lifecycleLease struct {
	root        string
	workspace   string
	branch      string
	cleanups    atomic.Int32
	snapshots   atomic.Int32
	snapshotErr error
}

func (l *lifecycleLease) RootPath() string                             { return l.root }
func (l *lifecycleLease) WorkspacePath() string                        { return l.workspace }
func (l *lifecycleLease) Branch() string                               { return l.branch }
func (l *lifecycleLease) ConfigureSnapshotPolicy([]string, bool) error { return nil }
func (l *lifecycleLease) Snapshot(context.Context) (runner.WorktreeSnapshot, error) {
	l.snapshots.Add(1)
	if l.snapshotErr != nil {
		return runner.WorktreeSnapshot{}, l.snapshotErr
	}
	return runner.WorktreeSnapshot{Branch: l.branch, Commit: "deadbeef"}, nil
}
func (l *lifecycleLease) Abandon(context.Context) error {
	l.cleanups.Add(1)
	return nil
}
func (l *lifecycleLease) Cleanup(context.Context) error {
	l.cleanups.Add(1)
	return nil
}

type lifecycleWorktrees struct {
	lease *lifecycleLease
	err   error
	calls atomic.Int32
}

func (m *lifecycleWorktrees) Prepare(
	_ context.Context, _ string, _ string,
) (runner.WorktreeLease, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return m.lease, nil
}

type lifecycleDispatcher struct {
	lease     *lifecycleLease
	hookCalls atomic.Int32
	listCalls atomic.Int32
	listErr   error
}

func (d *lifecycleDispatcher) ListTools(context.Context, []string) ([]models.ToolSchema, error) {
	d.listCalls.Add(1)
	if d.lease != nil && d.lease.cleanups.Load() != 0 {
		return nil, errors.New("worktree cleaned before tool listing")
	}
	return nil, d.listErr
}

func (d *lifecycleDispatcher) DispatchTool(
	_ context.Context, _ runner.ToolCallRequest,
) (runner.ToolCallResult, error) {
	if d.lease.cleanups.Load() != 0 {
		return runner.ToolCallResult{}, errors.New("worktree cleaned before post_execute")
	}
	d.hookCalls.Add(1)
	return runner.ToolCallResult{
		OutputJSON: `{"content":[{"type":"text","text":"ok"}],"isError":false}`,
	}, nil
}

func (d *lifecycleDispatcher) Classify(string) bool { return false }

type lifetimeStore struct {
	*sqlite.DB
	t           *testing.T
	lease       *lifecycleLease
	finalizeErr error
}

func (s *lifetimeStore) UpdateWorkerRunStatus(
	ctx context.Context, id string, fin store.WorkerRunFinalize,
) error {
	if s.lease.cleanups.Load() != 0 {
		s.t.Error("worktree cleaned before terminal run persistence")
	}
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	return s.DB.UpdateWorkerRunStatus(ctx, id, fin)
}

func isolatedWorkerFixture(t *testing.T) (*sqlite.DB, *store.Worker, string) {
	t.Helper()
	db := newTestStore(t)
	parentRoot := t.TempDir()
	ws := &store.Workspace{Name: "isolated", RootPath: parentRoot, DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "test-scope", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	w := sampleWorker(ws.ID, scope.ID)
	w.ParametersJSON = `{"topic":"isolation","_mcplexer_delegation":{"id":"del-test","kind":"token_preserving_delegation","worker_isolation":"worktree"}}`
	createWorker(t, db, w)
	return db, w, parentRoot
}

func TestDelegationWorktreeLivesThroughPostExecuteAndPersistence(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	isolatedRoot := t.TempDir()
	lease := &lifecycleLease{root: isolatedRoot, workspace: isolatedRoot, branch: "mcplexer/delegation/test"}
	manager := &lifecycleWorktrees{lease: lease}
	dispatcher := &lifecycleDispatcher{lease: lease}
	worker.PostExecuteScript = `print("validated")`
	if err := db.UpdateWorker(context.Background(), worker); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{responses: []models.SendResponse{{Text: "done", StopReason: models.StopEndTurn}}}
	wrappedStore := &lifetimeStore{DB: db, t: t, lease: lease}
	r := runner.New(runner.Deps{
		Store:      wrappedStore,
		Dispatcher: dispatcher,
		Secrets:    &fakeSecrets{},
		Worktrees:  manager,
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})

	runID, err := r.Run(context.Background(), worker.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.cleanups.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want exactly 1", lease.cleanups.Load())
	}
	if dispatcher.hookCalls.Load() != 1 {
		t.Fatalf("post_execute hook calls = %d, want 1", dispatcher.hookCalls.Load())
	}
	canonicalIsolatedRoot, err := filepath.EvalSymlinks(isolatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.lastWorkspacePath != canonicalIsolatedRoot {
		t.Fatalf("adapter workspace = %q, want %q", adapter.lastWorkspacePath, canonicalIsolatedRoot)
	}
	if len(adapter.requests) != 1 || !strings.Contains(adapter.requests[0].System, lease.branch) {
		t.Fatal("isolated branch instructions were not included in the system prompt")
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runner.StatusSuccess {
		t.Fatalf("run status = %q, want success (error=%q)", run.Status, run.Error)
	}
}

func TestDelegationWorktreeCreationFailureFailsClosed(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	adapter := &fakeAdapter{}
	manager := &lifecycleWorktrees{err: errors.New("injected worktree failure")}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &lifecycleDispatcher{},
		Secrets:    &fakeSecrets{},
		Worktrees:  manager,
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
	runID, err := r.Run(context.Background(), worker.ID)
	if err == nil || !strings.Contains(err.Error(), "injected worktree failure") {
		t.Fatalf("Run error = %v, want worktree failure", err)
	}
	if runID != "" {
		t.Fatalf("run id = %q, want empty", runID)
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("adapter ran %d times after isolation failure", len(adapter.requests))
	}
	runs, err := db.ListWorkerRuns(context.Background(), worker.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("created %d run rows after isolation failure", len(runs))
	}
}

func TestPersistedIsolatedCLIRejectsBeforeEveryRunnerSideEffect(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	manager := &lifecycleWorktrees{}
	dispatcher := &lifecycleDispatcher{}
	secrets := &countingSecrets{value: []byte("must-not-read")}
	var adapterBuilds atomic.Int32
	r := runner.New(runner.Deps{
		Store: db, Dispatcher: dispatcher, Secrets: secrets, Worktrees: manager,
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			adapterBuilds.Add(1)
			return &fakeAdapter{}, nil
		},
	})
	for _, provider := range []string{
		models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
		models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI, models.ProviderPiCLI,
	} {
		t.Run(provider, func(t *testing.T) {
			worker.ModelProvider = provider
			worker.ModelID = "test-model"
			if err := db.UpdateWorker(context.Background(), worker); err != nil {
				t.Fatal(err)
			}
			runID, err := r.Run(context.Background(), worker.ID)
			if err == nil || !strings.Contains(err.Error(), "isolated CLI delegation is unavailable") {
				t.Fatalf("error = %v, want isolated CLI rejection", err)
			}
			if runID != "" {
				t.Fatalf("run id = %q, want empty", runID)
			}
		})
	}
	if manager.calls.Load() != 0 || dispatcher.listCalls.Load() != 0 || secrets.callCount() != 0 || adapterBuilds.Load() != 0 {
		t.Fatalf("runner side effects: worktrees=%d lists=%d secrets=%d adapters=%d",
			manager.calls.Load(), dispatcher.listCalls.Load(), secrets.callCount(), adapterBuilds.Load())
	}
	runs, err := db.ListWorkerRuns(context.Background(), worker.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("created %d worker run rows before rejection", len(runs))
	}
}

func TestDelegationPreparationFailureCleansCreatedLeaseOnce(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	isolatedRoot := t.TempDir()
	lease := &lifecycleLease{root: isolatedRoot, workspace: isolatedRoot, branch: "mcplexer/delegation/test"}
	manager := &lifecycleWorktrees{lease: lease}
	dispatcher := &lifecycleDispatcher{lease: lease, listErr: errors.New("injected list failure")}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: dispatcher,
		Secrets:    &fakeSecrets{},
		Worktrees:  manager,
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return &fakeAdapter{}, nil
		},
	})
	_, err := r.Run(context.Background(), worker.ID)
	if err == nil || !strings.Contains(err.Error(), "injected list failure") {
		t.Fatalf("Run error = %v, want list failure", err)
	}
	if lease.cleanups.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want exactly 1", lease.cleanups.Load())
	}
}

func TestDelegationPanicStillCleansLease(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	isolatedRoot := t.TempDir()
	lease := &lifecycleLease{root: isolatedRoot, workspace: isolatedRoot, branch: "mcplexer/delegation/test"}
	adapter := &fakeAdapter{panicVal: "boom"}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &lifecycleDispatcher{lease: lease},
		Secrets:    &fakeSecrets{},
		Worktrees:  &lifecycleWorktrees{lease: lease},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
		Caps: runner.Caps{MaxWallClock: time.Minute},
	})
	if _, err := r.Run(context.Background(), worker.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.cleanups.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want exactly 1", lease.cleanups.Load())
	}
}

func TestDelegationSnapshotFailureRetainsWorktreeAndPersistsRecoveryBranch(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	isolatedRoot := t.TempDir()
	lease := &lifecycleLease{
		root: isolatedRoot, workspace: isolatedRoot,
		branch: "mcplexer/delegation/snapshot-failure", snapshotErr: errors.New("snapshot exploded"),
	}
	r := runner.New(runner.Deps{
		Store: db, Dispatcher: &lifecycleDispatcher{lease: lease}, Secrets: &fakeSecrets{},
		Worktrees: &lifecycleWorktrees{lease: lease},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return &fakeAdapter{responses: []models.SendResponse{{Text: "done", StopReason: models.StopEndTurn}}}, nil
		},
	})
	runID, err := r.Run(context.Background(), worker.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.snapshots.Load() != 1 || lease.cleanups.Load() != 0 {
		t.Fatalf("snapshot calls=%d cleanup calls=%d, want 1 and 0", lease.snapshots.Load(), lease.cleanups.Load())
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runner.StatusFailure || run.ResultBranch != lease.branch || run.ResultCommit != "" || run.ResultChanged {
		t.Fatalf("snapshot failure result = %+v", run)
	}
	if !strings.Contains(run.Error, "trusted worktree snapshot failed") {
		t.Fatalf("snapshot failure error = %q", run.Error)
	}
}

func TestDelegationFinalizeStoreFailureRetainsSnapshottedWorktree(t *testing.T) {
	db, worker, _ := isolatedWorkerFixture(t)
	isolatedRoot := t.TempDir()
	lease := &lifecycleLease{root: isolatedRoot, workspace: isolatedRoot, branch: "mcplexer/delegation/finalize-failure"}
	wrappedStore := &lifetimeStore{DB: db, t: t, lease: lease, finalizeErr: errors.New("terminal store unavailable")}
	r := runner.New(runner.Deps{
		Store: wrappedStore, Dispatcher: &lifecycleDispatcher{lease: lease}, Secrets: &fakeSecrets{},
		Worktrees: &lifecycleWorktrees{lease: lease},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return &fakeAdapter{responses: []models.SendResponse{{Text: "done", StopReason: models.StopEndTurn}}}, nil
		},
	})
	if _, err := r.Run(context.Background(), worker.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.snapshots.Load() != 1 || lease.cleanups.Load() != 0 {
		t.Fatalf("snapshot calls=%d cleanup calls=%d, want 1 and 0", lease.snapshots.Load(), lease.cleanups.Load())
	}
}

var _ store.Store = (*lifetimeStore)(nil)
