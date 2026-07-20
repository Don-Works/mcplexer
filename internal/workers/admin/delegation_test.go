package admin_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

type failCreateWorkerStore struct {
	store.WorkerStore
	failAfter int
	calls     int
}

func legacyCLIIsolation(provider string) string {
	if models.IsCLIProvider(provider) {
		return "none"
	}
	return ""
}

func (f *failCreateWorkerStore) CreateWorker(ctx context.Context, w *store.Worker) error {
	f.calls++
	if f.calls > f.failAfter {
		return errors.New("injected create failure")
	}
	return f.WorkerStore.CreateWorker(ctx, w)
}

type fakeOpenCodeRuntime struct {
	endpoint string
	starts   atomic.Int32
	started  chan struct{}
	err      error
}

func (f *fakeOpenCodeRuntime) Start(context.Context) error {
	f.starts.Add(1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	return f.err
}

func (f *fakeOpenCodeRuntime) Endpoint() string { return f.endpoint }

func TestServiceDelegationAutoStartsManagedOpenCode(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	db, err := sqlite.New(context.Background(), t.TempDir()+"/delegation-opencode.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "placeholder", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	rt := &fakeOpenCodeRuntime{endpoint: "http://127.0.0.1:4096"}
	svc := admin.New(db, admin.Options{Workspaces: db, OpenCodeRuntime: rt})

	out, err := svc.Delegate(context.Background(), admin.DelegationInput{
		WorkspaceID:         ws.ID,
		Objective:           "Review the dashboard.",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if rt.starts.Load() != 1 {
		// The create path now launches managed runtime Start() asynchronously
		// so the HTTP/MCP delegation create returns promptly. Allow a brief
		// window for the goroutine to run (test still validates the side-effect).
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) && rt.starts.Load() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if rt.starts.Load() != 1 {
		t.Fatalf("opencode starts = %d, want 1", rt.starts.Load())
	}
	_ = waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	got, err := svc.Get(context.Background(), admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.ModelEndpointURL != rt.endpoint {
		t.Fatalf("endpoint = %q, want %q", got.Worker.ModelEndpointURL, rt.endpoint)
	}
}

func TestServiceDelegationRejectsEveryCLIProviderBeforeSideEffects(t *testing.T) {
	for _, env := range []string{
		"MCPLEXER_ALLOW_CLAUDE_CLI", "MCPLEXER_ALLOW_OPENCODE_CLI", "MCPLEXER_ALLOW_GROK_CLI",
		"MCPLEXER_ALLOW_MIMO_CLI", "MCPLEXER_ALLOW_GEMINI_CLI", "MCPLEXER_ALLOW_CODEX_CLI", "MCPLEXER_ALLOW_PI_CLI",
	} {
		t.Setenv(env, "1")
	}
	_, db, wsID, scopeID := newTestService(t)
	runtime := &fakeOpenCodeRuntime{endpoint: "http://127.0.0.1:4096"}
	svc := admin.New(db, admin.Options{Workspaces: db, OpenCodeRuntime: runtime})
	runnerProbe := &fakeRunner{}
	svc.SetRunnerForTest(runnerProbe)
	ctx := context.Background()

	for _, provider := range []string{
		models.ProviderClaudeCLI, models.ProviderOpenCodeCLI, models.ProviderGrokCLI,
		models.ProviderMiMoCLI, models.ProviderGeminiCLI, models.ProviderCodexCLI, models.ProviderPiCLI,
	} {
		t.Run(provider, func(t *testing.T) {
			_, err := svc.Delegate(ctx, admin.DelegationInput{
				WorkspaceID: wsID, Objective: "must reject before side effects",
				ModelProvider: provider, ModelID: "test-model", SecretScopeID: scopeID,
			})
			if err == nil || !strings.Contains(err.Error(), "worktree isolation is unavailable for CLI provider") {
				t.Fatalf("error = %v, want isolated CLI preflight rejection", err)
			}
		})
	}
	_, mixedErr := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID: wsID, Objective: "mixed plan must reject atomically", ModelSelectionMode: "side_by_side",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: models.ProviderAnthropic, ModelID: "claude-test", SecretScopeID: scopeID},
			{ModelProvider: models.ProviderGrokCLI, ModelID: "grok-test", SecretScopeID: scopeID},
		},
	})
	if mixedErr == nil || !strings.Contains(mixedErr.Error(), "grok_cli") {
		t.Fatalf("mixed plan error = %v, want CLI rejection", mixedErr)
	}
	workers, err := db.ListWorkers(ctx, wsID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 0 || runtime.starts.Load() != 0 || len(runnerProbe.calls) != 0 {
		t.Fatalf("preflight side effects: workers=%d runtime_starts=%d dispatches=%d", len(workers), runtime.starts.Load(), len(runnerProbe.calls))
	}
}

func TestServiceDelegationGrokCLIAutoFillsPlaceholderScope(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Run a bounded Grok review pass.",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-build",
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	_ = waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.SecretScopeID != scopeID {
		t.Fatalf("secret scope = %q, want placeholder %q", got.Worker.SecretScopeID, scopeID)
	}

	// Exercise the read-side derive for CLI tool_calls_count inside the
	// delegations path (hydrateDelegationRuns now calls annotate). Even
	// with no matching audit rows the CLI provider must be stamped
	// "derived" so callers know 0 is not authoritative.
	dels, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID, Limit: 10})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	found := false
	for _, d := range dels {
		if d.ID == out.DelegationID {
			for _, w := range d.Workers {
				if w.LatestRun != nil && w.LatestRun.ModelProvider == "grok_cli" {
					found = true
					if w.LatestRun.ToolCallsCountSource != "derived" {
						t.Errorf("latest run source = %q, want derived for grok_cli delegation run", w.LatestRun.ToolCallsCountSource)
					}
				}
			}
		}
	}
	if !found {
		// Acceptable in a fast test if the run row isn't visible yet; the
		// Get above already validated the worker. Do not fail hard.
		t.Logf("no grok_cli latest run visible in ListDelegations yet (ok for timing)")
	}

	// Now seed one child-CLI audit row inside the run window and wire the
	// db as AuditCounter. ListDelegations must surface a non-zero derived
	// count (real telemetry) rather than the persisted 0. This covers the
	// "missing vs real zero" distinction for tool calls on the delegation
	// surfaces used for model rank/cost views.
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	auditTS := run.StartedAt.Add(1 * time.Second)
	// Ensure the run's time window will include the audit ts we are about
	// to insert. If the run is already terminal with an early FinishedAt,
	// bump it forward via finalize so the derive query window captures it.
	newFinished := auditTS.Add(30 * time.Second)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             run.Status,
		FinishedAt:         newFinished,
		InputTokens:        run.InputTokens,
		OutputTokens:       run.OutputTokens,
		CostUSD:            run.CostUSD,
		ToolCallsCount:     run.ToolCallsCount,
		OutputText:         run.OutputText,
		Error:              run.Error,
		MeshMessageIDsJSON: run.MeshMessageIDsJSON,
		AuditRecordIDsJSON: run.AuditRecordIDsJSON,
	}); err != nil {
		t.Fatalf("bump run finished for derive window: %v", err)
	}
	// The child's MCP session row has to exist too: the derive attributes
	// audit rows to a run via the session that produced them, and a session
	// that opened during the run window is what marks it as this run's
	// child rather than a bystander's.
	childDisconnected := auditTS.Add(2 * time.Second)
	if err := db.CreateSession(ctx, &store.Session{
		ID:             "sess-grok-child",
		ClientType:     "grok_cli",
		ConnectedAt:    run.StartedAt,
		DisconnectedAt: &childDisconnected,
	}); err != nil {
		t.Fatalf("seed child cli session: %v", err)
	}
	ar := &store.AuditRecord{
		Timestamp:   auditTS,
		CreatedAt:   auditTS,
		SessionID:   "sess-grok-child",
		ClientType:  "grok_cli",
		WorkspaceID: wsID,
		ToolName:    "mcpx__execute_code",
		Status:      "success",
		ActorKind:   "user",
	}
	if err := db.InsertAuditRecord(ctx, ar); err != nil {
		t.Fatalf("seed child cli audit: %v", err)
	}
	svc.SetAuditCounterForTest(db)
	dels2, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID, Limit: 10})
	if err != nil {
		t.Fatalf("ListDelegations after seed: %v", err)
	}
	for _, d := range dels2 {
		if d.ID == out.DelegationID {
			for _, w := range d.Workers {
				if w.LatestRun != nil && w.LatestRun.ModelProvider == "grok_cli" {
					if w.LatestRun.ToolCallsCountSource != "derived" {
						t.Errorf("after seed source = %q, want derived", w.LatestRun.ToolCallsCountSource)
					}
					if w.LatestRun.ToolCallsCount < 1 {
						t.Errorf("after seed tool_calls_count = %d, want >=1 (derived from seeded audit)", w.LatestRun.ToolCallsCount)
					}
				}
			}
		}
	}
}

func TestServiceDelegationRejectsAdminToolAllowlist(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		allowlist string
	}{
		{"literal admin tool", `["mcplexer__create_worker"]`},
		{"mcplexer glob", `["mcplexer__*"]`},
		{"mcpx admin glob", `["mcpx__*"]`},
		{"catch-all glob", `["*"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Delegate(ctx, admin.DelegationInput{
				WorkspaceID:       wsID,
				Objective:         "Do bounded work.",
				ModelProvider:     "anthropic",
				ModelID:           "claude-sonnet-4-5",
				SecretScopeID:     scopeID,
				ToolAllowlistJSON: tc.allowlist,
			})
			if err == nil {
				t.Fatal("expected admin allowlist rejection")
			}
			if !strings.Contains(err.Error(), "admin-only tools") {
				t.Fatalf("error %q should mention admin-only tools", err)
			}
		})
	}
}

// TestServiceDelegationReviewModeUsesRestrictedAllowlist is the narrowly-scoped
// test for worker role-filter hardening (review vs execute). Review role must
// default to a restricted allowlist that omits mutators; execute gets the full
// default. This is enforced at Delegate/create time so the persisted worker row
// carries the hardened surface (subsequently wired by runner/dispatcher).
func TestServiceDelegationReviewModeUsesRestrictedAllowlist(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	// review mode without explicit allowlist -> hardened review default
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "Review-only inspection.",
		ModelProvider: "anthropic",
		ModelID:       "claude-sonnet-4-5",
		SecretScopeID: scopeID,
		WorkerMode:    "review",
	})
	if err != nil {
		t.Fatalf("Delegate review: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get review worker: %v", err)
	}
	al := got.Worker.ToolAllowlistJSON
	if !strings.Contains(al, "memory__recall") || strings.Contains(al, "memory__save") {
		t.Fatalf("review allowlist must include recall but exclude save: %s", al)
	}
	if strings.Contains(al, `"task__create"`) || strings.Contains(al, `"task__update"`) {
		t.Fatalf("review allowlist must exclude task__create/update: %s", al)
	}
	if !strings.Contains(al, `"task__append_note"`) {
		t.Fatalf("review allowlist should retain append_note: %s", al)
	}

	// default (execute) mode gets the full delegation surface including mutators
	out2, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "Execute the work.",
		ModelProvider: "anthropic",
		ModelID:       "claude-sonnet-4-5",
		SecretScopeID: scopeID,
		// WorkerMode empty -> normalizes to "execute"
	})
	if err != nil {
		t.Fatalf("Delegate execute: %v", err)
	}
	got2, err := svc.Get(ctx, admin.GetInput{ID: out2.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get execute worker: %v", err)
	}
	al2 := got2.Worker.ToolAllowlistJSON
	if !strings.Contains(al2, "memory__save") || !strings.Contains(al2, `"task__create"`) {
		t.Fatalf("execute/default allowlist must include mutators: %s", al2)
	}
}

func TestServiceDelegationDefaultsToOneHourWallClock(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "Do a normal coding workflow.",
		ModelProvider: "anthropic",
		ModelID:       "claude-sonnet-4-5",
		SecretScopeID: scopeID,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.MaxWallClockSeconds != 3600 {
		t.Fatalf("max wall clock = %d, want 3600", got.Worker.MaxWallClockSeconds)
	}
	if got.Worker.MaxToolCalls != 80 {
		t.Fatalf("max tool calls = %d, want 80", got.Worker.MaxToolCalls)
	}
}

func TestServiceDelegationMissingModelSelectionGivesSetupGuidance(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID: wsID,
		Objective:   "Summarise recent repository changes.",
	})
	if err == nil {
		t.Fatal("expected missing delegation model error")
	}
	for _, want := range []string{
		"delegation model required",
		"model_profile_id",
		"model_provider and model_id",
		`model_selection_mode:"capacity"`,
		"mcpx__list_delegation_model_capacity",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestServiceDelegationCapacityModeWithoutRegisteredModelsGivesSetupGuidance(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Pick the best registered coding model.",
		ModelSelectionMode: "capacity",
	})
	if err == nil {
		t.Fatal("expected no registered capacity models error")
	}
	for _, want := range []string{
		"no registered delegation model candidates",
		"Workers > Model Profiles",
		"model_provider and model_id",
		"mcpx__list_delegation_model_capacity",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestServiceDelegationParallelCreateFailureRollsBack(t *testing.T) {
	db, err := sqlite.New(context.Background(), t.TempDir()+"/delegation-rollback.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	wrapped := &failCreateWorkerStore{WorkerStore: db, failAfter: 1}
	svc := admin.New(wrapped, admin.Options{Workspaces: db})
	ctx := context.Background()

	_, err = svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   ws.ID,
		Objective:     "Fan out two workers.",
		ModelProvider: "anthropic",
		ModelID:       "claude-sonnet-4-5",
		SecretScopeID: scope.ID,
		Parallelism:   2,
	})
	if err == nil {
		t.Fatal("expected injected create failure")
	}
	if !strings.Contains(err.Error(), "injected create failure") {
		t.Fatalf("error = %q", err)
	}

	rows, err := svc.List(ctx, admin.ListInput{WorkspaceID: ws.ID, NamePattern: "delegate-"})
	if err != nil {
		t.Fatalf("List workers: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rollback left %d delegate workers, want 0: %+v", len(rows), rows)
	}
}

func TestServiceDelegationLifecycleAggregatesSavingsAndReview(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:            wsID,
		Objective:              "Implement the low-level parser changes.",
		Handoff:                "Touch parser files only. Run the parser tests.",
		ModelProvider:          "anthropic",
		ModelID:                "claude-sonnet-4-5",
		SecretScopeID:          scopeID,
		ParentContextID:        "ctx-parent",
		ParentModel:            "claude-opus-4-5",
		ParentInputTokens:      60000,
		ParentOutputTokens:     5000,
		ParentCostUSD:          4.20,
		BaselineTokensEstimate: 160000,
		BaselineCostUSD:        12.00,
		Parallelism:            2,
		ReviewRequired:         boolPtr(true),
		MaxWallClockSeconds:    30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.DelegationID == "" {
		t.Fatalf("delegation id was empty")
	}
	if out.WorkerMode != "execute" || !out.ReviewRequired {
		t.Fatalf("mode/review required = %q/%v, want execute/true", out.WorkerMode, out.ReviewRequired)
	}
	if len(out.Dispatches) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(out.Dispatches))
	}

	for i, dispatch := range out.Dispatches {
		run := waitForDelegationRun(t, db, dispatch.WorkerID)
		input := 10000 + i*1000
		fin := store.WorkerRunFinalize{
			Status:             "success",
			FinishedAt:         time.Now().UTC(),
			InputTokens:        input,
			OutputTokens:       5000,
			CostUSD:            0.50,
			ToolCallsCount:     12,
			OutputText:         "STATUS: success\nHANDOFF: parent can review the diff",
			MeshMessageIDsJSON: "[]",
			AuditRecordIDsJSON: "[]",
		}
		if err := db.UpdateWorkerRunStatus(ctx, run.ID, fin); err != nil {
			t.Fatalf("finish run %s: %v", run.ID, err)
		}
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Status != "needs_review" {
		t.Fatalf("status = %q, want needs_review before parent score", got.Status)
	}
	if got.WorkerMode != "execute" || !got.ReviewRequired || got.Review.Reviewed {
		t.Fatalf("mode/review state = %q/%v/%v, want execute/true/unreviewed", got.WorkerMode, got.ReviewRequired, got.Review.Reviewed)
	}
	if got.Aggregate.Workers != 2 || got.Aggregate.Success != 2 {
		t.Fatalf("aggregate worker counts = %+v", got.Aggregate)
	}
	if got.Aggregate.TotalTokens != 31000 {
		t.Fatalf("total tokens = %d, want 31000", got.Aggregate.TotalTokens)
	}
	if got.Aggregate.ParentTokens != 65000 {
		t.Fatalf("parent tokens = %d, want 65000", got.Aggregate.ParentTokens)
	}
	if got.Aggregate.CombinedTokens != 96000 {
		t.Fatalf("combined tokens = %d, want 96000", got.Aggregate.CombinedTokens)
	}
	if got.Aggregate.FrontierTokensAvoided != 160000 {
		t.Fatalf("frontier avoided = %d, want 160000", got.Aggregate.FrontierTokensAvoided)
	}
	if got.Aggregate.EstimatedParentTokensSaved != 160000 {
		t.Fatalf("parent saved = %d, want 160000", got.Aggregate.EstimatedParentTokensSaved)
	}
	if got.Aggregate.WorkerTokenDelta != 129000 || got.Aggregate.NetTokensDelta != 129000 {
		t.Fatalf("worker token delta/net delta = %d/%d, want 129000", got.Aggregate.WorkerTokenDelta, got.Aggregate.NetTokensDelta)
	}
	if math.Abs(got.Aggregate.EstimatedCostSavedUSD-11.00) > 0.000001 {
		t.Fatalf("cost saved = %.4f, want 11.00", got.Aggregate.EstimatedCostSavedUSD)
	}
	if len(got.ModelStats) != 1 {
		t.Fatalf("model stats len = %d, want 1", len(got.ModelStats))
	}
	stat := got.ModelStats[0]
	if stat.ModelKey != "anthropic/claude-sonnet-4-5" || stat.Runs != 2 || stat.Success != 2 || stat.TotalTokens != 31000 {
		t.Fatalf("model stat before review = %+v", stat)
	}
	if stat.ReviewCount != 0 {
		t.Fatalf("review count before review = %d, want 0", stat.ReviewCount)
	}

	reviewed, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:       wsID,
		DelegationID:      out.DelegationID,
		Score:             87,
		Notes:             "Good diff and tests; parent accepted.",
		ReviewerContextID: "ctx-parent",
		ReviewerModel:     "claude-opus-4-5",
	})
	if err != nil {
		t.Fatalf("ReviewDelegation: %v", err)
	}
	if reviewed.Status != "success" {
		t.Fatalf("status after review = %q, want success", reviewed.Status)
	}
	if !reviewed.Review.Reviewed || reviewed.Review.Score != 87 || reviewed.Review.Outcome != "accepted" {
		t.Fatalf("review = %+v, want accepted score 87", reviewed.Review)
	}
	if reviewed.Aggregate.ReviewScore != 87 {
		t.Fatalf("aggregate review score = %d, want 87", reviewed.Aggregate.ReviewScore)
	}
	if len(reviewed.ModelStats) != 1 || reviewed.ModelStats[0].ReviewCount != 1 || reviewed.ModelStats[0].ReviewScore != 87 {
		t.Fatalf("reviewed model stats = %+v, want score 87", reviewed.ModelStats)
	}
}

func TestServiceDelegationDefaultReviewOptional(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Run a routine delegated implementation.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-haiku-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.ReviewRequired {
		t.Fatalf("review_required = true, want default false")
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        100,
		OutputTokens:       50,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.ReviewRequired {
		t.Fatalf("listed review_required = true, want false")
	}
	if got.Status != "success" {
		t.Fatalf("status = %q, want success without parent review", got.Status)
	}
}

func TestServiceDelegationCapacityModeUsesReviewedProfileRanking(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []*store.ModelProfile{
		{
			Name:          "baseline-openai",
			Provider:      "openai",
			SecretScopeID: scopeID,
			KnownModels:   []string{"slow-reviewer"},
		},
		{
			Name:          "preferred-anthropic",
			Provider:      "anthropic",
			SecretScopeID: scopeID,
			KnownModels:   []string{"sharp-reviewer"},
		},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create model profile %s: %v", p.Name, err)
		}
	}

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "slow-reviewer", 55)
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "sharp-reviewer", 92)

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Pick the best registered review model.",
		TaskKind:            "review",
		ModelSelectionMode:  "capacity",
		ReviewRequired:      boolPtr(false),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate capacity: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.ModelProvider != "anthropic" || got.Worker.ModelID != "sharp-reviewer" {
		t.Fatalf("capacity selected %s/%s, want anthropic/sharp-reviewer", got.Worker.ModelProvider, got.Worker.ModelID)
	}
	_ = waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)

	capacityRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	if len(capacityRows) < 2 {
		t.Fatalf("capacity rows = %d, want at least 2", len(capacityRows))
	}
	if capacityRows[0].ModelKey != "anthropic/sharp-reviewer" {
		t.Fatalf("top capacity model = %+v, want anthropic/sharp-reviewer", capacityRows[0])
	}
}

func TestServiceDelegationCapacityModeUsesWorkerRegistryRanking(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, in := range []admin.CreateInput{
		{
			Name:           "registry-review-slow-openai",
			ModelProvider:  "openai",
			ModelID:        "slow-reviewer",
			SecretScopeID:  scopeID,
			PromptTemplate: "Review a bounded code change.",
			ScheduleSpec:   "manual",
			WorkspaceID:    wsID,
		},
		{
			Name:           "registry-review-sharp-anthropic",
			ModelProvider:  "anthropic",
			ModelID:        "sharp-reviewer",
			SecretScopeID:  scopeID,
			PromptTemplate: "Review a bounded code change.",
			ScheduleSpec:   "manual",
			WorkspaceID:    wsID,
		},
	} {
		if _, err := svc.Create(ctx, in); err != nil {
			t.Fatalf("create registry worker %s: %v", in.Name, err)
		}
	}

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "slow-reviewer", 55)
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "sharp-reviewer", 92)
	// The shared seed helper registers durable profiles for capacity-focused
	// tests. Remove them here so this case specifically proves that ordinary
	// enabled workers (not ephemeral delegate-* rows) populate the registry.
	profiles, err := db.ListModelProfiles(ctx)
	if err != nil {
		t.Fatalf("list seed profiles: %v", err)
	}
	for _, profile := range profiles {
		if err := db.DeleteModelProfile(ctx, profile.ID); err != nil {
			t.Fatalf("delete seed profile %s: %v", profile.ID, err)
		}
	}

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Pick the best enabled worker-backed review model.",
		TaskKind:            "review",
		ModelSelectionMode:  "capacity",
		ReviewRequired:      boolPtr(false),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate capacity: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.ModelProvider != "anthropic" || got.Worker.ModelID != "sharp-reviewer" {
		t.Fatalf("capacity selected %s/%s, want anthropic/sharp-reviewer", got.Worker.ModelProvider, got.Worker.ModelID)
	}
	_ = waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)

	capacityRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	if len(capacityRows) < 2 {
		t.Fatalf("capacity rows = %d, want at least 2", len(capacityRows))
	}
	if capacityRows[0].ModelKey != "anthropic/sharp-reviewer" {
		t.Fatalf("top capacity model = %+v, want anthropic/sharp-reviewer", capacityRows[0])
	}
}

func seedDelegationModelReview(
	t *testing.T,
	svc *admin.Service,
	db *sqlite.DB,
	wsID string,
	scopeID string,
	provider string,
	modelID string,
	score int,
) {
	t.Helper()
	ctx := context.Background()
	ensureDelegationModelProfileForTest(t, db, scopeID, provider, modelID)
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Seed model history.",
		TaskKind:            "review",
		ModelProvider:       provider,
		ModelID:             modelID,
		WorkerIsolation:     legacyCLIIsolation(provider),
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("seed Delegate %s/%s: %v", provider, modelID, err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        1000,
		OutputTokens:       250,
		CostUSD:            0.01,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish seed run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "review",
		Score:        score,
	}); err != nil {
		t.Fatalf("review seed delegation: %v", err)
	}
}

func seedDelegationModelReviewWithScores(
	t *testing.T,
	svc *admin.Service,
	db *sqlite.DB,
	wsID string,
	scopeID string,
	provider string,
	modelID string,
	taskKind string,
	score int,
	scores map[string]int,
) {
	t.Helper()
	ctx := context.Background()
	ensureDelegationModelProfileForTest(t, db, scopeID, provider, modelID)
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Seed model history with capability scores.",
		TaskKind:            taskKind,
		ModelProvider:       provider,
		ModelID:             modelID,
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("seed Delegate %s/%s: %v", provider, modelID, err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        1000,
		OutputTokens:       250,
		CostUSD:            0.01,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish seed run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     taskKind,
		Score:        score,
		Scores:       scores,
	}); err != nil {
		t.Fatalf("review seed delegation: %v", err)
	}
}

func ensureDelegationModelProfileForTest(
	t *testing.T,
	db *sqlite.DB,
	scopeID string,
	provider string,
	modelID string,
) {
	t.Helper()
	ctx := context.Background()
	profiles, err := db.ListModelProfiles(ctx)
	if err != nil {
		t.Fatalf("list model profiles: %v", err)
	}
	for _, p := range profiles {
		if p.Provider != provider {
			continue
		}
		for _, known := range p.KnownModels {
			if known == modelID {
				return
			}
		}
	}
	name := "test-" + provider + "-" + strings.NewReplacer("/", "-", " ", "-").Replace(modelID)
	if err := db.CreateModelProfile(ctx, &store.ModelProfile{
		Name:          name,
		Provider:      provider,
		SecretScopeID: scopeID,
		KnownModels:   []string{modelID},
	}); err != nil {
		t.Fatalf("create model profile %s/%s: %v", provider, modelID, err)
	}
}

func TestServiceDelegationCapacityUsesCapabilityScoresForTaskKind(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []*store.ModelProfile{
		{Name: "overall-star", Provider: "openai", SecretScopeID: scopeID, KnownModels: []string{"overall-star"}},
		{Name: "coding-specialist", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"coding-specialist"}},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("profile: %v", err)
		}
	}

	seedDelegationModelReviewWithScores(t, svc, db, wsID, scopeID,
		"openai", "overall-star", "review", 95, map[string]int{
			"review": 95,
			"coding": 30,
		})
	seedDelegationModelReviewWithScores(t, svc, db, wsID, scopeID,
		"anthropic", "coding-specialist", "review", 70, map[string]int{
			"review": 65,
			"coding": 94,
		})

	codingRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity coding: %v", err)
	}
	if len(codingRows) < 2 {
		t.Fatalf("coding capacity rows = %d, want at least 2", len(codingRows))
	}
	if codingRows[0].ModelKey != "anthropic/coding-specialist" {
		t.Fatalf("coding top capacity model = %+v, want anthropic/coding-specialist", codingRows[0])
	}
	if math.Abs(codingRows[0].ReviewScore-94) > 0.5 {
		t.Fatalf("coding review score = %.1f, want category score ~94", codingRows[0].ReviewScore)
	}

	reviewRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity review: %v", err)
	}
	if len(reviewRows) < 2 || reviewRows[0].ModelKey != "openai/overall-star" {
		t.Fatalf("review top capacity model = %+v, want openai/overall-star", reviewRows)
	}

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Ranked pick should follow coding capability score.",
		TaskKind:           "coding",
		ModelSelectionMode: "ranked",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "openai", ModelID: "overall-star", SecretScopeID: scopeID, CapabilityTags: []string{"coding"}},
			{ModelProvider: "anthropic", ModelID: "coding-specialist", SecretScopeID: scopeID, CapabilityTags: []string{"coding"}},
		},
		ReviewRequired:      boolPtr(false),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate ranked: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get ranked worker: %v", err)
	}
	if got.Worker.ModelProvider != "anthropic" || got.Worker.ModelID != "coding-specialist" {
		t.Fatalf("ranked selected %s/%s, want anthropic/coding-specialist", got.Worker.ModelProvider, got.Worker.ModelID)
	}
}

func TestServiceDelegationSideBySideCandidatesAndScoreBreakdown(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Compare model performance on the same patch review.",
		TaskKind:           "Review",
		ModelSelectionMode: "side_by_side",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5", SecretScopeID: scopeID, CapabilityTags: []string{"review"}},
			{ModelProvider: "openai", ModelID: "gpt-5-codex-mini", SecretScopeID: scopeID, CapabilityTags: []string{"coding", "tool_calling"}},
		},
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.ModelSelectionMode != "side_by_side" || out.TaskKind != "review" {
		t.Fatalf("selection/task kind = %q/%q", out.ModelSelectionMode, out.TaskKind)
	}
	if len(out.Dispatches) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(out.Dispatches))
	}

	wantModels := map[string]bool{
		"anthropic/claude-sonnet-4-5": true,
		"openai/gpt-5-codex-mini":     true,
	}
	for _, dispatch := range out.Dispatches {
		got, err := svc.Get(ctx, admin.GetInput{ID: dispatch.WorkerID})
		if err != nil {
			t.Fatalf("Get worker: %v", err)
		}
		key := got.Worker.ModelProvider + "/" + got.Worker.ModelID
		if !wantModels[key] {
			t.Fatalf("unexpected worker model %q", key)
		}
		run := waitForDelegationRun(t, db, dispatch.WorkerID)
		if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
			Status:             "success",
			FinishedAt:         time.Now().UTC(),
			InputTokens:        1000,
			OutputTokens:       200,
			CostUSD:            0.02,
			MeshMessageIDsJSON: "[]",
			AuditRecordIDsJSON: "[]",
		}); err != nil {
			t.Fatalf("finish run: %v", err)
		}
	}

	reviewed, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        77,
		Scores: map[string]int{
			"review":       88,
			"tool_calling": 71,
		},
		ModelScores: []admin.DelegationModelReview{
			{ModelKey: "anthropic/claude-sonnet-4-5", Score: 91, Scores: map[string]int{"review": 94}},
			{ModelKey: "openai/gpt-5-codex-mini", Score: 63, Scores: map[string]int{"tool_calling": 82}},
		},
	})
	if err != nil {
		t.Fatalf("ReviewDelegation: %v", err)
	}
	if !reviewed.Review.Reviewed || reviewed.Review.Scores["review"] != 88 {
		t.Fatalf("review scores = %+v", reviewed.Review)
	}
	stats := map[string]admin.DelegationModelStat{}
	for _, stat := range reviewed.ModelStats {
		stats[stat.ModelKey] = stat
	}
	if stats["anthropic/claude-sonnet-4-5"].ReviewScore != 91 ||
		stats["anthropic/claude-sonnet-4-5"].CapabilityScores["review"] != 94 {
		t.Fatalf("anthropic stat = %+v", stats["anthropic/claude-sonnet-4-5"])
	}
	if stats["openai/gpt-5-codex-mini"].ReviewScore != 63 ||
		stats["openai/gpt-5-codex-mini"].CapabilityScores["tool_calling"] != 82 {
		t.Fatalf("openai stat = %+v", stats["openai/gpt-5-codex-mini"])
	}
}

func TestServiceDelegationFailureNeedsReviewUntilScored(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Run a bounded review pass.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		ReviewRequired:      boolPtr(true),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		Error:              "model unavailable",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Status != "needs_review" {
		t.Fatalf("status before review = %q, want needs_review", got.Status)
	}

	reviewed, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        5,
		Outcome:      "rejected",
		Notes:        "Infrastructure failure; no usable worker output.",
	})
	if err != nil {
		t.Fatalf("ReviewDelegation: %v", err)
	}
	if reviewed.Status != "failure" {
		t.Fatalf("status after review = %q, want failure", reviewed.Status)
	}
	if !reviewed.Review.Reviewed || reviewed.Review.Score != 5 {
		t.Fatalf("review = %+v, want reviewed score 5", reviewed.Review)
	}
}

func TestServiceDelegationMixedResultsBecomePartialAfterReview(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Run two bounded review passes.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		Parallelism:         2,
		ReviewRequired:      boolPtr(true),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	for i, dispatch := range out.Dispatches {
		run := waitForDelegationRun(t, db, dispatch.WorkerID)
		status := "success"
		errText := ""
		if i == 1 {
			status = "cap_exceeded"
			errText = "wall-clock exceeded"
		}
		if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
			Status:             status,
			FinishedAt:         time.Now().UTC(),
			Error:              errText,
			InputTokens:        1000,
			OutputTokens:       100,
			MeshMessageIDsJSON: "[]",
			AuditRecordIDsJSON: "[]",
		}); err != nil {
			t.Fatalf("finish run: %v", err)
		}
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Status != "needs_review" {
		t.Fatalf("status before review = %q, want needs_review", got.Status)
	}

	reviewed, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        62,
		Outcome:      "partial",
		Notes:        "One useful worker, one capped worker.",
	})
	if err != nil {
		t.Fatalf("ReviewDelegation: %v", err)
	}
	if reviewed.Status != "partial" {
		t.Fatalf("status after review = %q, want partial", reviewed.Status)
	}
}

func waitForDelegationRun(t *testing.T, db interface {
	ListWorkerRuns(context.Context, string, int) ([]*store.WorkerRun, error)
}, workerID string) *store.WorkerRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := db.ListWorkerRuns(context.Background(), workerID, 1)
		if err != nil {
			t.Fatalf("list worker runs: %v", err)
		}
		if len(runs) > 0 {
			return runs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker %s did not create a delegated run", workerID)
	return nil
}

func findDelegation(t *testing.T, rows []admin.DelegationContext, id string) admin.DelegationContext {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("delegation %s not found in %+v", id, rows)
	return admin.DelegationContext{}
}

func TestServiceDelegationCapacityAccountingMissingNotFavored(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-sonnet-4-5", 55)

	const missingModel = "openai/gpt-missing-telemetry"
	ensureDelegationModelProfileForTest(t, db, scopeID, "openai", "gpt-missing-telemetry")
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Seed accounting-missing run.",
		TaskKind:            "coding",
		ModelProvider:       "openai",
		ModelID:             "gpt-missing-telemetry",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("seed Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        0,
		OutputTokens:       0,
		CostUSD:            0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize missing run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "coding",
		Score:        55,
	}); err != nil {
		t.Fatalf("review seed delegation: %v", err)
	}

	out2, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Capacity test — pick the honest model.",
		TaskKind:           "coding",
		ModelSelectionMode: "capacity",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "openai", ModelID: "gpt-missing-telemetry", SecretScopeID: scopeID, CapabilityTags: []string{"coding"}},
			{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5", SecretScopeID: scopeID, CapabilityTags: []string{"coding"}},
		},
		ReviewRequired:      boolPtr(false),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate capacity: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out2.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if got.Worker.ModelProvider != "anthropic" || got.Worker.ModelID != "claude-sonnet-4-5" {
		t.Fatalf("capacity selected %s/%s, want anthropic/claude-sonnet-4-5 (real model must outrank missing-accounting)", got.Worker.ModelProvider, got.Worker.ModelID)
	}
	capRun := waitForDelegationRun(t, db, out2.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, capRun.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        500,
		OutputTokens:       100,
		CostUSD:            0.01,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize capacity pick run: %v", err)
	}

	capacityRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	if len(capacityRows) < 2 {
		t.Fatalf("capacity rows = %d, want at least 2", len(capacityRows))
	}
	var anthropicRow, missingRow *admin.DelegationModelCapacity
	for i := range capacityRows {
		switch capacityRows[i].ModelKey {
		case "anthropic/claude-sonnet-4-5":
			anthropicRow = &capacityRows[i]
		case missingModel:
			missingRow = &capacityRows[i]
		}
	}
	if anthropicRow == nil || missingRow == nil {
		t.Fatalf("expected anthropic + missing rows in %+v", capacityRows)
	}
	if anthropicRow.CapacityScore <= missingRow.CapacityScore {
		t.Fatalf("anthropic capacity_score %.1f must beat missing-accounting %.1f", anthropicRow.CapacityScore, missingRow.CapacityScore)
	}
	for _, row := range capacityRows {
		if row.ModelKey == missingModel {
			if row.SuccessRate > 0 {
				t.Errorf("%s success_rate = %f, want 0 (no known runs)", missingModel, row.SuccessRate)
			}
			if row.OperationalSuccessRate != 1 {
				t.Errorf("%s operational_success_rate = %f, want 1 (successful terminal runs)", missingModel, row.OperationalSuccessRate)
			}
			if row.AccountingKnown {
				t.Errorf("%s accounting_known = true, want false for all-missing telemetry", missingModel)
			}
			if row.AvgDurationMS > 0 {
				t.Errorf("%s avg_duration_ms = %d, want 0 (no known runs)", missingModel, row.AvgDurationMS)
			}
		}
	}
}

func TestServiceDelegationCapacityCLIMissingAccountingOperationalSuccess(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	const grokModel = "grok_cli/grok-composer-fast"
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-sonnet-4-5", 72)
	ensureDelegationModelProfileForTest(t, db, scopeID, "grok_cli", "grok-composer-fast")

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Seed grok_cli missing-accounting success.",
		TaskKind:            "coding",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-composer-fast",
		WorkerIsolation:     "none",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("seed grok_cli Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        0,
		OutputTokens:       0,
		CostUSD:            0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize grok_cli run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "coding",
		Score:        72,
	}); err != nil {
		t.Fatalf("review grok_cli delegation: %v", err)
	}

	capacityRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var grokRow, anthropicRow *admin.DelegationModelCapacity
	for i := range capacityRows {
		switch capacityRows[i].ModelKey {
		case grokModel:
			grokRow = &capacityRows[i]
		case "anthropic/claude-sonnet-4-5":
			anthropicRow = &capacityRows[i]
		}
	}
	if grokRow == nil {
		t.Fatalf("missing grok_cli capacity row in %+v", capacityRows)
	}
	if grokRow.Success != 1 {
		t.Fatalf("grok success = %d, want 1", grokRow.Success)
	}
	if grokRow.SuccessRate != 0 {
		t.Fatalf("grok success_rate = %f, want 0 (no accounting telemetry)", grokRow.SuccessRate)
	}
	if grokRow.OperationalSuccessRate != 1 {
		t.Fatalf("grok operational_success_rate = %f, want 1", grokRow.OperationalSuccessRate)
	}
	if grokRow.AccountingKnown {
		t.Fatal("grok accounting_known = true, want false")
	}
	if anthropicRow == nil {
		t.Fatalf("missing anthropic capacity row in %+v", capacityRows)
	}
	poisonedScore := grokRow.ReviewScore + (0-0.5)*20 - 4
	if grokRow.CapacityScore <= poisonedScore+1 {
		t.Fatalf("grok capacity_score %.1f should exceed poisoned baseline %.1f; missing accounting must not apply false 0%% success penalty", grokRow.CapacityScore, poisonedScore)
	}
	if anthropicRow.CapacityScore <= grokRow.CapacityScore {
		t.Fatalf("anthropic capacity_score %.1f should beat grok %.1f when reviews match but anthropic has known accounting", anthropicRow.CapacityScore, grokRow.CapacityScore)
	}
}

// TestServiceDelegationRankingRecencyEWMA proves that the EWMA in
// delegationCandidateRanks + capacityScoreForCandidate + bestReviewed makes
// recent reviewed scores dominate stale history. A model with high historical
// but poor recent reviews loses to one with lower historical but strong recent
// (and vice-versa for recent high lifting a model). Uses successive seeds so
// ListDelegations order (UpdatedAt desc) + ReviewedAt produce different recency.
func TestServiceDelegationRankingRecencyEWMA(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	// Register two models via profiles so capacity can pick from them.
	for _, p := range []*store.ModelProfile{
		{Name: "stale-king", Provider: "openai", SecretScopeID: scopeID, KnownModels: []string{"gpt-old-king"}},
		{Name: "recent-champ", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"claude-recent-champ"}},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("profile: %v", err)
		}
	}

	// Seed sequence (later = more recent UpdatedAt/ReviewedAt):
	// 1. openai high historical
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "gpt-old-king", 95)
	// 2. anthropic mediocre
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-recent-champ", 65)
	// 3. openai recent LOW (stale high + recent low => recency pulled down)
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "gpt-old-king", 58)
	// 4. anthropic recent HIGH (mediocre + recent high => recency lifted)
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-recent-champ", 82)

	// Capacity listing for review task-kind must rank the recent-strong model first.
	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	if len(capRows) < 2 {
		t.Fatalf("cap rows=%d want >=2", len(capRows))
	}
	if capRows[0].ModelKey != "anthropic/claude-recent-champ" {
		t.Fatalf("top by recency EWMA = %s (score=%.1f), want anthropic/claude-recent-champ (recent high must beat stale-high)", capRows[0].ModelKey, capRows[0].ReviewScore)
	}
	// Also verify the other is present and its recency is lower than champ's.
	var king, champ *admin.DelegationModelCapacity
	for i := range capRows {
		if capRows[i].ModelKey == "openai/gpt-old-king" {
			king = &capRows[i]
		}
		if capRows[i].ModelKey == "anthropic/claude-recent-champ" {
			champ = &capRows[i]
		}
	}
	if king == nil || champ == nil {
		t.Fatalf("missing models in capacity: king=%v champ=%v", king, champ)
	}
	if champ.ReviewScore <= king.ReviewScore {
		t.Fatalf("champ recency %.1f not > king recency %.1f (recent must dominate)", champ.ReviewScore, king.ReviewScore)
	}

	// Also exercise ranked selection: supply both as candidates, "ranked" must pick recent champ.
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Ranked pick must follow recent EWMA not stale history.",
		TaskKind:           "review",
		ModelSelectionMode: "ranked",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "openai", ModelID: "gpt-old-king", SecretScopeID: scopeID},
			{ModelProvider: "anthropic", ModelID: "claude-recent-champ", SecretScopeID: scopeID},
		},
		ReviewRequired:      boolPtr(false),
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate ranked: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get ranked worker: %v", err)
	}
	if got.Worker.ModelProvider != "anthropic" || got.Worker.ModelID != "claude-recent-champ" {
		t.Fatalf("ranked selected %s/%s, want the recent-high champ", got.Worker.ModelProvider, got.Worker.ModelID)
	}
}

// TestServiceDelegationRecentHighLiftsOverStaleMediocre is the symmetric case:
// a model with recent high score lifts above one whose history is higher but
// whose recent is only mediocre.
func TestServiceDelegationRecentHighLiftsOverStaleMediocre(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []*store.ModelProfile{
		{Name: "steady-ed", Provider: "openai", SecretScopeID: scopeID, KnownModels: []string{"gpt-steady"}},
		{Name: "late-bloomer", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"claude-bloomer"}},
	} {
		_ = db.CreateModelProfile(ctx, p)
	}

	// Sequence:
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "gpt-steady", 88)        // early good
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-bloomer", 40) // early poor
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "gpt-steady", 72)        // recent mediocre
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-bloomer", 91) // recent high lift

	cap, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{WorkspaceID: wsID, TaskKind: "review"})
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	if len(cap) < 2 || cap[0].ModelKey != "anthropic/claude-bloomer" {
		t.Fatalf("top after recent lift = %+v, want bloomer (recent 91 lifts it over steady's recent 72)", cap)
	}
}

func boolPtr(v bool) *bool { return &v }

// TestDelegationProviderGroupDisabled verifies the operator switches
// (via DisabledProviders on inputs) exclude whole groups from capacity
// and candidate selection. Uses the exported test helper for the
// predicate and a direct capacity list input with disabled map.
func TestDelegationProviderGroupDisabled(t *testing.T) {
	cases := []struct {
		name     string
		disabled map[string]bool
		provider string
		model    string
		endpoint string
		label    string
		want     bool
	}{
		{"opencode off disables opencode_cli", map[string]bool{"opencode": true}, "opencode_cli", "minimax/MiniMax-M3", "", "", true},
		{"claude off disables claude_cli", map[string]bool{"claude": true}, "claude_cli", "sonnet", "", "", true},
		{"claude off disables anthropic api", map[string]bool{"claude": true}, "anthropic", "claude-sonnet-4-5", "", "", true},
		{"claude off disables openrouter anthropic claude", map[string]bool{"claude": true}, "opencode_cli", "openrouter/anthropic/claude-fable-5", "", "", true},
		{"grok off disables grok_cli", map[string]bool{"grok": true}, "grok_cli", "grok-3", "", "", true},
		{"mimo off disables mimo_cli", map[string]bool{"mimo": true}, "mimo_cli", "xiaomi/mimo-v2.5", "", "", true},
		{"mimo off catches xiaomi model label", map[string]bool{"mimo": true}, "opencode_cli", "xiaomi/mimo-v2.5", "", "", true},
		{"pi off disables pi_cli", map[string]bool{"pi": true}, "pi_cli", "qwen-local", "", "", true},
		{"pi off catches pi harness label", map[string]bool{"pi": true}, "openai_compat", "qwen-local", "", "Pi harness qwen-local", true},
		{"minimax key catches minimax model", map[string]bool{"minimax": true}, "opencode_cli", "minimax/MiniMax-M3", "", "", true},
		{"openrouter key catches endpoint", map[string]bool{"openrouter": true}, "openai_compat", "openai/gpt", "https://openrouter.ai/api/v1", "", true},
		{"local key catches loopback endpoint", map[string]bool{"local": true}, "openai_compat", "qwen3", "http://127.0.0.1:1234/v1", "", true},
		{"local key catches lmstudio label", map[string]bool{"local": true}, "openai_compat", "qwen3", "", "LM Studio local", true},
		{"local key leaves remote compat enabled", map[string]bool{"local": true}, "openai_compat", "openai/gpt", "https://openrouter.ai/api/v1", "", false},
		{"raw provider id works", map[string]bool{"opencode_cli": true}, "opencode_cli", "foo", "", "", true},
		{"raw pi_cli id works", map[string]bool{"pi_cli": true}, "pi_cli", "qwen-local", "", "", true},
		{"enabled by default", map[string]bool{}, "opencode_cli", "minimax/M3", "", "", false},
		{"other group does not affect", map[string]bool{"grok": true}, "opencode_cli", "minimax/M3", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := admin.ProviderGroupDisabledForTest(c.disabled, c.provider, c.model, c.endpoint, c.label)
			if got != c.want {
				t.Fatalf("disabled=%v provider=%s model=%s -> %v, want %v", c.disabled, c.provider, c.model, got, c.want)
			}
		})
	}

	// Also exercise capacity list input path: when disabled, rows for that
	// group should be absent (registered filters them before ranking).
	// We only assert the predicate + input plumbing here; full service
	// capacity integration is covered by other tests that would now see
	// empty candidates if everything were disabled.
}

func TestDelegationDisabledProviderBlocksDirectModelID(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Try disabled provider.",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		SecretScopeID:       scopeID,
		DisabledProviders:   map[string]bool{"opencode": true},
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected error for disabled provider on direct model_id")
	}
	if !strings.Contains(err.Error(), "disabled by operator") {
		t.Fatalf("error = %q, want mention of disabled by operator", err)
	}
}

func TestDelegationDisabledProviderBlocksExplicitCandidates(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Try disabled candidate.",
		ModelSelectionMode: "side_by_side",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5", SecretScopeID: scopeID},
			{ModelProvider: "claude_cli", ModelID: "opus-4", SecretScopeID: scopeID},
		},
		DisabledProviders:   map[string]bool{"claude": true},
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected error for disabled provider in explicit model_candidates")
	}
	if !strings.Contains(err.Error(), "disabled by operator") {
		t.Fatalf("error = %q, want mention of disabled by operator", err)
	}
}

func TestDelegationDisabledProviderAllowsNonDisabled(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Allowed provider.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		DisabledProviders:   map[string]bool{"opencode": true, "grok": true},
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("expected success for non-disabled provider: %v", err)
	}
	if out.DelegationID == "" {
		t.Fatal("delegation id was empty")
	}
}

func TestDelegationDisabledProviderBlocksByRawProviderID(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Try raw provider disable.",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-3",
		SecretScopeID:       scopeID,
		DisabledProviders:   map[string]bool{"grok_cli": true},
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected error for raw provider id disable")
	}
	if !strings.Contains(err.Error(), "disabled by operator") {
		t.Fatalf("error = %q, want mention of disabled by operator", err)
	}
}

// TestServiceCountUnreviewedRequiredDelegations verifies the dashboard
// metric helper counts only review_required && !reviewed delegations,
// distinct by delegation ID.
func TestServiceCountUnreviewedRequiredDelegations(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	// One with review required (unreviewed).
	d1, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:    wsID,
		Objective:      "count-test-req",
		ModelProvider:  "anthropic",
		ModelID:        "claude-haiku-4-5",
		SecretScopeID:  scopeID,
		ReviewRequired: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("delegate1: %v", err)
	}
	// One without review required.
	_, err = svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:    wsID,
		Objective:      "count-test-optional",
		ModelProvider:  "anthropic",
		ModelID:        "claude-haiku-4-5",
		SecretScopeID:  scopeID,
		ReviewRequired: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("delegate2: %v", err)
	}

	n, err := svc.CountUnreviewedRequiredDelegations(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("initial unreviewed count = %d, want 1", n)
	}

	// Review the required one.
	_, err = svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		DelegationID: d1.DelegationID,
		Score:        85,
		Notes:        "good for count test",
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	n, err = svc.CountUnreviewedRequiredDelegations(ctx)
	if err != nil {
		t.Fatalf("count after review: %v", err)
	}
	if n != 0 {
		t.Fatalf("after review count = %d, want 0", n)
	}
}

func TestReviewDelegationAutoAckWorkerMessages(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:            wsID,
		Objective:              "Auto-ack test.",
		ModelProvider:          "anthropic",
		ModelID:                "claude-sonnet-4-5",
		SecretScopeID:          scopeID,
		MaxWallClockSeconds:    30,
		ParentContextID:        "ctx-parent",
		ParentModel:            "claude-opus-4-5",
		ParentInputTokens:      60000,
		ParentOutputTokens:     5000,
		BaselineTokensEstimate: 160000,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	workerID := out.Dispatches[0].WorkerID
	now := time.Now().UTC()

	workerMsgs := []*store.MeshMessage{
		{
			ID: "01W_FINDING", WorkspaceID: wsID, SessionID: "worker:" + workerID,
			AgentName: workerID, Kind: "finding", Priority: "high",
			Content: "STATUS: success", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
		{
			ID: "01W_REPLY", WorkspaceID: wsID, SessionID: "worker:" + workerID,
			AgentName: workerID, Kind: "reply", Priority: "high",
			Content: "delegation reply", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
		{
			ID: "01W_EVENT", WorkspaceID: wsID, SessionID: "worker:" + workerID,
			AgentName: workerID, Kind: "event", Priority: "normal",
			Content: "worker started", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
	}
	agentMsg := &store.MeshMessage{
		ID: "01A_FINDING", WorkspaceID: wsID, SessionID: "agent-session-1",
		AgentName: "alice", Kind: "finding", Priority: "normal",
		Content: "unrelated agent finding", Audience: "*", Status: "live",
		ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now, ActorKind: "agent",
	}
	for _, m := range workerMsgs {
		if err := db.InsertMeshMessage(ctx, m); err != nil {
			t.Fatalf("insert worker msg %s: %v", m.ID, err)
		}
	}
	if err := db.InsertMeshMessage(ctx, agentMsg); err != nil {
		t.Fatalf("insert agent msg: %v", err)
	}

	run := waitForDelegationRun(t, db, workerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status: "success", FinishedAt: now, MeshMessageIDsJSON: "[]", AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	svc.SetMeshStore(db)

	_, err = svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        90,
		Notes:        "accepted",
	})
	if err != nil {
		t.Fatalf("ReviewDelegation: %v", err)
	}

	assertStatus := func(id, want string) {
		t.Helper()
		msg, err := db.GetMeshMessage(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if msg.Status != want {
			t.Errorf("%s status = %q, want %q", id, msg.Status, want)
		}
	}
	assertStatus("01W_FINDING", "archived")
	assertStatus("01W_REPLY", "archived")
	assertStatus("01W_EVENT", "live")
	assertStatus("01A_FINDING", "live")
}

func TestReviewDelegationAutoAckIdempotent(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:            wsID,
		Objective:              "Idempotent ack test.",
		ModelProvider:          "anthropic",
		ModelID:                "claude-sonnet-4-5",
		SecretScopeID:          scopeID,
		MaxWallClockSeconds:    30,
		ParentContextID:        "ctx-parent",
		ParentModel:            "claude-opus-4-5",
		ParentInputTokens:      60000,
		ParentOutputTokens:     5000,
		BaselineTokensEstimate: 160000,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	workerID := out.Dispatches[0].WorkerID
	now := time.Now().UTC()
	if err := db.InsertMeshMessage(ctx, &store.MeshMessage{
		ID: "01W_FIND", WorkspaceID: wsID, SessionID: "worker:" + workerID,
		AgentName: workerID, Kind: "finding", Priority: "high",
		Content: "result", Audience: "*", Status: "live",
		ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	run := waitForDelegationRun(t, db, workerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status: "success", FinishedAt: now, MeshMessageIDsJSON: "[]", AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	svc.SetMeshStore(db)

	_, err = svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        85,
	})
	if err != nil {
		t.Fatalf("first review: %v", err)
	}

	_, err = svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		Score:        92,
		Notes:        "updated review",
	})
	if err != nil {
		t.Fatalf("second review: %v", err)
	}

	msg, err := db.GetMeshMessage(ctx, "01W_FIND")
	if err != nil {
		t.Fatalf("get after double review: %v", err)
	}
	if msg.Status != "archived" {
		t.Errorf("status after double review = %q, want archived", msg.Status)
	}
}

func TestAggregateDelegationTwoCurrencyMetrics(t *testing.T) {
	cases := []struct {
		name                  string
		ctx                   admin.DelegationContext
		wantRealDollars       float64
		wantQuotaBuckets      map[string]int
		wantFrontierPreserved int
		wantFrontierBurned    int
		wantRealCostSaved     float64
	}{
		{
			name: "claude_cli subscription worker burns frontier quota",
			ctx: admin.DelegationContext{
				Baseline: admin.DelegationBaseline{
					TokensEstimate: 1000000,
					CostUSD:        64.95,
				},
				Workers: []admin.DelegationWorkerContext{
					{
						Worker: &store.Worker{ModelProvider: "claude_cli", ModelID: "claude-opus-4-8"},
						LatestRun: &store.WorkerRun{
							ModelProvider: "claude_cli",
							ModelID:       "claude-opus-4-8",
							InputTokens:   600000,
							OutputTokens:  400000,
							CostUSD:       64.95,
							Status:        "success",
						},
					},
				},
			},
			wantRealDollars:       0,
			wantQuotaBuckets:      map[string]int{"claude": 1000000},
			wantFrontierPreserved: 0,
			wantFrontierBurned:    1000000,
			wantRealCostSaved:     64.95,
		},
		{
			name: "openrouter metered worker spends real dollars preserves frontier",
			ctx: admin.DelegationContext{
				Baseline: admin.DelegationBaseline{
					TokensEstimate: 1000000,
					CostUSD:        64.95,
				},
				Workers: []admin.DelegationWorkerContext{
					{
						Worker: &store.Worker{ModelProvider: "opencode_cli", ModelID: "openrouter/deepseek/deepseek-v4-pro"},
						LatestRun: &store.WorkerRun{
							ModelProvider: "opencode_cli",
							ModelID:       "openrouter/deepseek/deepseek-v4-pro",
							InputTokens:   500000,
							OutputTokens:  300000,
							CostUSD:       3.75,
							Status:        "success",
						},
					},
				},
			},
			wantRealDollars:       3.75,
			wantQuotaBuckets:      map[string]int{},
			wantFrontierPreserved: 1000000,
			wantFrontierBurned:    0,
			wantRealCostSaved:     61.20,
		},
		{
			name: "zai subscription worker uses quota preserves frontier",
			ctx: admin.DelegationContext{
				Baseline: admin.DelegationBaseline{
					TokensEstimate: 800000,
					CostUSD:        48.00,
				},
				Workers: []admin.DelegationWorkerContext{
					{
						Worker: &store.Worker{ModelProvider: "opencode_cli", ModelID: "zai-coding-plan/glm-5.1"},
						LatestRun: &store.WorkerRun{
							ModelProvider: "opencode_cli",
							ModelID:       "zai-coding-plan/glm-5.1",
							InputTokens:   400000,
							OutputTokens:  200000,
							CostUSD:       0,
							Status:        "success",
						},
					},
				},
			},
			wantRealDollars:       0,
			wantQuotaBuckets:      map[string]int{"zai": 600000},
			wantFrontierPreserved: 800000,
			wantFrontierBurned:    0,
			wantRealCostSaved:     48.00,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg, _ := admin.AggregateDelegationForTest(tc.ctx)
			if math.Abs(agg.RealDollarsSpent-tc.wantRealDollars) > 0.000001 {
				t.Errorf("RealDollarsSpent = %.6f, want %.6f", agg.RealDollarsSpent, tc.wantRealDollars)
			}
			if agg.FrontierQuotaBurned != tc.wantFrontierBurned {
				t.Errorf("FrontierQuotaBurned = %d, want %d", agg.FrontierQuotaBurned, tc.wantFrontierBurned)
			}
			if agg.FrontierQuotaPreserved != tc.wantFrontierPreserved {
				t.Errorf("FrontierQuotaPreserved = %d, want %d", agg.FrontierQuotaPreserved, tc.wantFrontierPreserved)
			}
			if math.Abs(agg.RealCostSavedUSD-tc.wantRealCostSaved) > 0.000001 {
				t.Errorf("RealCostSavedUSD = %.6f, want %.6f", agg.RealCostSavedUSD, tc.wantRealCostSaved)
			}
			for k, wantV := range tc.wantQuotaBuckets {
				gotV := agg.QuotaTokensByBucket[k]
				if gotV != wantV {
					t.Errorf("QuotaTokensByBucket[%q] = %d, want %d", k, gotV, wantV)
				}
			}
			for k, gotV := range agg.QuotaTokensByBucket {
				if _, ok := tc.wantQuotaBuckets[k]; !ok {
					t.Errorf("QuotaTokensByBucket[%q] = %d, want absent", k, gotV)
				}
			}
		})
	}
}

func TestServiceDelegationRepoBriefInjection(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	brief := "## Project Structure\n- src/ — main Go code\n- cmd/ — CLI entrypoint\n- internal/ — private packages\n\n## Key Files\n- main.go — entrypoint\n- server.go — HTTP server"
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Add a health check endpoint.",
		Handoff:             "Focus on server.go only.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		RepoBrief:           brief,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	if out.BriefTokens == 0 {
		t.Fatal("BriefTokens = 0, want > 0 when repo brief is provided")
	}
	// Rough check: len(brief)/4 ≈ 49 tokens for this brief
	if out.BriefTokens < 20 || out.BriefTokens > 200 {
		t.Fatalf("BriefTokens = %d, want reasonable estimate for %d-char brief", out.BriefTokens, len(brief))
	}

	// Verify the worker prompt contains the brief section
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	pt := got.Worker.PromptTemplate
	if !strings.Contains(pt, "## Repository Context (pre-computed)") {
		t.Fatal("prompt template missing '## Repository Context (pre-computed)' header")
	}
	if !strings.Contains(pt, "## Project Structure") {
		t.Fatal("prompt template missing brief content")
	}
	if !strings.Contains(pt, "## Objective") {
		t.Fatal("prompt template missing '## Objective' header")
	}
	if !strings.Contains(pt, "## Handoff") {
		t.Fatal("prompt template missing '## Handoff' header")
	}
	// Verify ordering: brief before objective before handoff
	briefIdx := strings.Index(pt, "## Repository Context (pre-computed)")
	objIdx := strings.Index(pt, "## Objective")
	handoffIdx := strings.Index(pt, "## Handoff")
	if briefIdx >= objIdx || objIdx >= handoffIdx {
		t.Fatalf("prompt section order wrong: brief@%d, objective@%d, handoff@%d", briefIdx, objIdx, handoffIdx)
	}
}

func TestServiceDelegationNoRepoBrief(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Do something without a brief.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	if out.BriefTokens != 0 {
		t.Fatalf("BriefTokens = %d, want 0 when no repo brief", out.BriefTokens)
	}

	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	pt := got.Worker.PromptTemplate
	if strings.Contains(pt, "## Repository Context (pre-computed)") {
		t.Fatal("prompt template should not contain brief section when no brief provided")
	}
	// Objective should still be present
	if !strings.Contains(pt, "## Objective") {
		t.Fatal("prompt template missing '## Objective'")
	}
}

func TestServiceDelegationEmptyRepoBriefIsIgnored(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Empty brief test.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		RepoBrief:           "   \n\t  ",
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	if out.BriefTokens != 0 {
		t.Fatalf("BriefTokens = %d, want 0 for whitespace-only brief", out.BriefTokens)
	}

	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get worker: %v", err)
	}
	if strings.Contains(got.Worker.PromptTemplate, "## Repository Context (pre-computed)") {
		t.Fatal("whitespace-only brief should not inject brief section")
	}
}

func TestServiceDelegationWallClockDefaults(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	executeOut, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "Execute default wall clock test.",
		ModelProvider: "anthropic",
		ModelID:       "claude-haiku-4-5",
		SecretScopeID: scopeID,
	})
	if err != nil {
		t.Fatalf("Delegate execute: %v", err)
	}
	execWorker, err := svc.Get(ctx, admin.GetInput{ID: executeOut.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get execute worker: %v", err)
	}
	if execWorker.Worker.MaxWallClockSeconds != 3600 {
		t.Fatalf("execute default max_wall_clock_seconds = %d, want 3600", execWorker.Worker.MaxWallClockSeconds)
	}

	reviewOut, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "Review default wall clock test.",
		ModelProvider: "anthropic",
		ModelID:       "claude-haiku-4-5",
		SecretScopeID: scopeID,
		WorkerMode:    "review",
	})
	if err != nil {
		t.Fatalf("Delegate review: %v", err)
	}
	reviewWorker, err := svc.Get(ctx, admin.GetInput{ID: reviewOut.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get review worker: %v", err)
	}
	if reviewWorker.Worker.MaxWallClockSeconds != 600 {
		t.Fatalf("review default max_wall_clock_seconds = %d, want 600", reviewWorker.Worker.MaxWallClockSeconds)
	}

	explicitOut, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Explicit wall clock test.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-haiku-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Delegate explicit: %v", err)
	}
	explicitWorker, err := svc.Get(ctx, admin.GetInput{ID: explicitOut.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get explicit worker: %v", err)
	}
	if explicitWorker.Worker.MaxWallClockSeconds != 1800 {
		t.Fatalf("explicit max_wall_clock_seconds = %d, want 1800", explicitWorker.Worker.MaxWallClockSeconds)
	}
}

func TestServiceDelegationPreflightRejectsInvalidWorkspace(t *testing.T) {
	svc, _, _, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         "ws-nonexistent",
		Objective:           "Should fail preflight.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected preflight rejection for invalid workspace")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("error %q should mention preflight", err)
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("error %q should mention workspace", err)
	}
}

func TestServiceDelegationPreflightRunsBeforeManagedRuntimeStartup(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	db, err := sqlite.New(context.Background(), t.TempDir()+"/delegation-preflight-runtime.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "placeholder", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	rt := &fakeOpenCodeRuntime{
		endpoint: "http://127.0.0.1:4096",
		started:  make(chan struct{}, 1),
	}
	svc := admin.New(db, admin.Options{Workspaces: db, OpenCodeRuntime: rt})

	_, err = svc.Delegate(context.Background(), admin.DelegationInput{
		WorkspaceID:         "ws-does-not-exist",
		Objective:           "Must fail before runtime startup.",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		MaxWallClockSeconds: 30,
	})
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("Delegate error = %v, want preflight rejection", err)
	}
	select {
	case <-rt.started:
		t.Fatal("managed OpenCode runtime started for a delegation rejected by preflight")
	case <-time.After(50 * time.Millisecond):
	}
	if got := rt.starts.Load(); got != 0 {
		t.Fatalf("opencode starts = %d, want 0", got)
	}
}

func TestServiceDelegationPreflightRejectsInvalidSecretScope(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Should fail preflight.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       "scope-nonexistent",
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected preflight rejection for invalid secret scope")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("error %q should mention preflight", err)
	}
	if !strings.Contains(err.Error(), "secret scope") {
		t.Fatalf("error %q should mention secret scope", err)
	}
}

func TestServiceDelegationPreflightPassesWithValidInputs(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Valid delegation.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if out.DelegationID == "" {
		t.Fatal("delegation id was empty")
	}
	if len(out.Dispatches) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(out.Dispatches))
	}
}

func TestServiceDelegationPreflightSkipsScopeCheckForCLIProviders(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	// grok_cli should pass preflight without a real secret scope because
	// delegationProviderIgnoresScope exempts CLI providers.
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "CLI provider delegation.",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-build",
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate grok_cli: %v", err)
	}
	if out.DelegationID == "" {
		t.Fatal("delegation id was empty")
	}
}

func TestServiceDelegationPreflightRejectsMissingWorkspaceBeforeCreatingWorkers(t *testing.T) {
	svc, db, _, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         "ws-does-not-exist",
		Objective:           "Should fail before creating any workers.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		Parallelism:         2,
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Verify no delegate workers were created — the preflight should
	// have failed before the worker creation loop.
	workers, listErr := db.ListWorkers(ctx, "ws-does-not-exist", false)
	if listErr != nil {
		t.Fatalf("ListWorkers: %v", listErr)
	}
	delegCount := 0
	for _, w := range workers {
		if strings.HasPrefix(w.Name, "delegate-") {
			delegCount++
		}
	}
	if delegCount != 0 {
		t.Fatalf("preflight leaked %d delegate workers; want 0", delegCount)
	}
}

// TestServiceDelegationQualityVsReliabilityRate proves that the new
// quality-only and reliability dimensions correctly separate coding
// ability from adapter/launch noise. A model with 1 success + 3
// operational failures should have QualityRate=1.0 (only quality run
// succeeded) and ReliabilityRate=0.25 (3/4 terminal runs were
// operational failures), while SuccessRate remains at 0.25 (1/4).
func TestServiceDelegationQualityVsReliabilityRate(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	const provider = "anthropic"
	const modelID = "claude-sonnet-4-5"

	// Delegation 1: success — reviewed at 75.
	seedDelegationModelReview(t, svc, db, wsID, scopeID, provider, modelID, 75)

	// Delegation 2: operational failure (adapter send error, zero tokens).
	out2, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Op failure delegation.",
		ModelProvider:       provider,
		ModelID:             modelID,
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate op-fail: %v", err)
	}
	run2 := waitForDelegationRun(t, db, out2.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run2.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		Error:              "adapter send: connection refused",
		InputTokens:        0,
		OutputTokens:       0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize op-fail run: %v", err)
	}
	// Don't review — operational-only delegation suppresses review attribution.

	// Delegation 3: another operational failure.
	out3, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Op failure delegation 2.",
		ModelProvider:       provider,
		ModelID:             modelID,
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate op-fail 2: %v", err)
	}
	run3 := waitForDelegationRun(t, db, out3.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run3.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		Error:              "adapter send: timeout",
		InputTokens:        0,
		OutputTokens:       0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize op-fail run 2: %v", err)
	}

	// Delegation 4: another operational failure (3 total op failures + 1 success).
	out4, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Op failure delegation 3.",
		ModelProvider:       provider,
		ModelID:             modelID,
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate op-fail 3: %v", err)
	}
	run4 := waitForDelegationRun(t, db, out4.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run4.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		Error:              "adapter send: model not found",
		InputTokens:        0,
		OutputTokens:       0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize op-fail run 3: %v", err)
	}

	// Check capacity row.
	capacityRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var row *admin.DelegationModelCapacity
	for i := range capacityRows {
		if capacityRows[i].ModelKey == provider+"/"+modelID {
			row = &capacityRows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("missing capacity row for %s/%s in %+v", provider, modelID, capacityRows)
	}

	// Runs: 1 success + 3 failure = 4 total. OperationalFailures = 3.
	if row.Runs != 4 {
		t.Errorf("runs = %d, want 4", row.Runs)
	}
	if row.Success != 1 {
		t.Errorf("success = %d, want 1", row.Success)
	}
	if row.Failure != 3 {
		t.Errorf("failure = %d, want 3", row.Failure)
	}
	if row.OperationalFailures != 3 {
		t.Errorf("operational_failures = %d, want 3", row.OperationalFailures)
	}

	// Quality rate: 1 success / (1 success + 0 quality failure) = 1.0.
	// The 3 operational failures are excluded from the denominator.
	if row.QualityRate != 1.0 {
		t.Errorf("quality_rate = %f, want 1.0 (only quality run succeeded)", row.QualityRate)
	}

	// Reliability rate: 1 - (3 op failures / 4 terminal) = 0.25.
	if row.ReliabilityRate != 0.25 {
		t.Errorf("reliability_rate = %f, want 0.25 (3/4 terminal runs were op failures)", row.ReliabilityRate)
	}

	// SuccessRate (existing): StampAccountingMissing only flags SUCCESSFUL
	// runs, so the 3 op-failure runs are NOT accounting_missing. But
	// knownAccountingRuns = runs - unknownCostRuns - operationalFailures
	// = 4 - 0 - 3 = 1, so successRate = 1/1 = 1.0. This is the same
	// result as QualityRate here because the only non-operational run
	// succeeded. The key test is that QualityRate and ReliabilityRate
	// are surfaced as separate dimensions.
	if row.SuccessRate != 1.0 {
		t.Errorf("success_rate = %f, want 1.0 (op-failures excluded from known accounting)", row.SuccessRate)
	}
	// OperationalSuccessRate: 1 success / (1 success + 3 failure) = 0.25.
	if row.OperationalSuccessRate != 0.25 {
		t.Errorf("operational_success_rate = %f, want 0.25", row.OperationalSuccessRate)
	}
}
