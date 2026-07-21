package runner_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/delegscope"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// cliScopeWorker builds a non-isolated worker (no delegation block in
// ParametersJSON) so the isolated-CLI refusal at the top of prepareRun is not
// the thing under test.
func cliScopeWorker(t *testing.T, provider string) (*sqlite.DB, *store.Worker) {
	t.Helper()
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ModelProvider = provider
	w.ModelID = "test-model"
	createWorker(t, db, w)
	return db, w
}

// TestScopedCLIWorkerIsRefusedBeforeAnySpend is the regression for the
// capability relay hole: a CLI-provider worker configured with a capability
// profile or tool allowlist must not run, because neither reaches the CLI
// child's own MCP session. Before the guard this test fails — the run
// proceeds, the adapter is built and invoked, and the child executes unscoped.
func TestScopedCLIWorkerIsRefusedBeforeAnySpend(t *testing.T) {
	scopes := []struct {
		name      string
		apply     func(*store.Worker)
		wantNamed string
	}{
		{
			name:      "capability profile",
			apply:     func(w *store.Worker) { w.CapabilityProfileJSON = `{"preset":"researcher"}` },
			wantNamed: "capability_profile_json",
		},
		{
			name:      "tool allowlist",
			apply:     func(w *store.Worker) { w.ToolAllowlistJSON = `["task__create"]` },
			wantNamed: "tool_allowlist_json",
		},
		{
			name: "both columns",
			apply: func(w *store.Worker) {
				w.ToolAllowlistJSON = `["task__create"]`
				w.CapabilityProfileJSON = `{"preset":"minimal"}`
			},
			wantNamed: "capability_profile_json",
		},
	}
	for _, provider := range []string{
		models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
		models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI,
		models.ProviderPiCLI,
	} {
		for _, sc := range scopes {
			t.Run(provider+"/"+sc.name, func(t *testing.T) {
				db, worker := cliScopeWorker(t, provider)
				sc.apply(worker)
				if err := db.UpdateWorker(context.Background(), worker); err != nil {
					t.Fatal(err)
				}
				var adapterBuilds atomic.Int32
				secrets := &countingSecrets{value: []byte("must-not-read")}
				r := runner.New(runner.Deps{
					Store:      db,
					Dispatcher: &fakeDispatcher{},
					Secrets:    secrets,
					Adapter: func(models.Config) (models.ModelAdapter, error) {
						adapterBuilds.Add(1)
						return &fakeAdapter{}, nil
					},
				})

				runID, err := r.Run(context.Background(), worker.ID)
				if err == nil {
					t.Fatal("scoped CLI worker ran; its configured scope reaches nothing")
				}
				if !strings.Contains(err.Error(), sc.wantNamed) {
					t.Errorf("error omits the unenforceable column %q: %v", sc.wantNamed, err)
				}
				if runID != "" {
					t.Errorf("run id = %q, want empty", runID)
				}
				// Refuse before spend: no adapter, no secret read, no run row.
				if n := adapterBuilds.Load(); n != 0 {
					t.Errorf("adapter built %d times after refusal", n)
				}
				if n := secrets.callCount(); n != 0 {
					t.Errorf("secret read %d times after refusal", n)
				}
				runs, err := db.ListWorkerRuns(context.Background(), worker.ID, 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(runs) != 0 {
					t.Errorf("created %d run rows after refusal", len(runs))
				}
			})
		}
	}
}

// TestUnscopedCLIWorkerStillRuns pins the blast radius: the guard fires only
// when an operator actually asked for a scope. A CLI worker with no scope
// columns is unchanged.
func TestUnscopedCLIWorkerStillRuns(t *testing.T) {
	db, worker := cliScopeWorker(t, models.ProviderClaudeCLI)
	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Secrets:    &fakeSecrets{},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})

	runID, err := r.Run(context.Background(), worker.ID)
	if err != nil {
		t.Fatalf("unscoped CLI worker refused: %v", err)
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runner.StatusSuccess {
		t.Fatalf("run status = %q, want %q (error: %s)", run.Status, runner.StatusSuccess, run.Error)
	}
}

// TestCLIWorkerWithDefaultDelegationAllowlistRuns is the end-to-end regression
// for the CLI delegation break. The delegation admin layer stamps the system
// default allowlist onto every delegated worker, so a real CLI delegation
// carries a non-empty tool_allowlist_json. That must run: the default is a
// baseline, not an operator scope the guard should refuse. Exercised across all
// CLI providers through the full prepareRun path that failed live.
func TestCLIWorkerWithDefaultDelegationAllowlistRuns(t *testing.T) {
	for _, provider := range []string{
		models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
		models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI,
		models.ProviderPiCLI,
	} {
		for _, def := range []struct {
			label string
			json  string
		}{
			{"execute default", delegscope.DefaultToolsJSON},
			{"review default", delegscope.DefaultReviewToolsJSON},
		} {
			t.Run(provider+"/"+def.label, func(t *testing.T) {
				db, worker := cliScopeWorker(t, provider)
				worker.ToolAllowlistJSON = def.json
				if err := db.UpdateWorker(context.Background(), worker); err != nil {
					t.Fatal(err)
				}
				adapter := &fakeAdapter{responses: []models.SendResponse{
					{Text: "done", StopReason: models.StopEndTurn},
				}}
				r := runner.New(runner.Deps{
					Store:      db,
					Dispatcher: &fakeDispatcher{},
					Secrets:    &fakeSecrets{},
					Adapter: func(models.Config) (models.ModelAdapter, error) {
						return adapter, nil
					},
				})
				runID, err := r.Run(context.Background(), worker.ID)
				if err != nil {
					t.Fatalf("CLI worker with default delegation allowlist refused: %v", err)
				}
				run, err := db.GetWorkerRun(context.Background(), runID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status != runner.StatusSuccess {
					t.Fatalf("run status = %q, want %q (error: %s)", run.Status, runner.StatusSuccess, run.Error)
				}
			})
		}
	}
}

// TestScopedAPIWorkerStillRuns pins the other half of the blast radius: an
// API-provider worker dispatches its tool calls back through the runner, so
// its scope IS enforced and the guard must stay out of the way.
func TestScopedAPIWorkerStillRuns(t *testing.T) {
	db, worker := cliScopeWorker(t, models.ProviderAnthropic)
	worker.CapabilityProfileJSON = `{"preset":"researcher"}`
	worker.ToolAllowlistJSON = `["task__create"]`
	if err := db.UpdateWorker(context.Background(), worker); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Secrets:    &fakeSecrets{},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})

	runID, err := r.Run(context.Background(), worker.ID)
	if err != nil {
		t.Fatalf("scoped API worker refused: %v", err)
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runner.StatusSuccess {
		t.Fatalf("run status = %q, want %q (error: %s)", run.Status, runner.StatusSuccess, run.Error)
	}
}

// TestCLIAdaptersNeverReturnToolCalls is the premise the guard rests on: if a
// CLI adapter ever started dispatching tool calls back through the runner, its
// scope WOULD be enforced for those calls and the refusal would be wrong. The
// dispatcher is the only place a worker's capability profile is attached, and
// CLI providers never reach it — no CLI adapter's Send populates ToolCalls.
//
// This asserts the property at the seam the runner actually depends on: after
// a full CLI-provider run, the dispatcher saw zero tool dispatches.
func TestCLIAdaptersNeverReturnToolCalls(t *testing.T) {
	db, worker := cliScopeWorker(t, models.ProviderClaudeCLI)
	disp := &fakeDispatcher{}
	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Secrets:    &fakeSecrets{},
		Adapter: func(models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
	if _, err := r.Run(context.Background(), worker.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(disp.dispatched); n != 0 {
		t.Fatalf("CLI run dispatched %d tool calls through the runner; if CLI "+
			"providers now round-trip tool calls, the capability profile reaches "+
			"them and cli_scope_guard.go must be revisited", n)
	}
}
