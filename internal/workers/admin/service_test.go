package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// newTestService spins up an in-memory sqlite store with a workspace +
// auth scope pre-seeded and returns the bound admin.Service plus the
// (workspaceID, scopeID) the caller threads into Create inputs.
func newTestService(t *testing.T) (*admin.Service, *sqlite.DB, string, string) {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/admin-test.db")
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

	svc := admin.New(db, admin.Options{
		Workspaces: db,
		// ScheduleValidator omitted — defaults to "non-empty" so tests
		// don't have to depend on the scheduler package.
	})
	return svc, db, ws.ID, scope.ID
}

// baseCreate produces a CreateInput with every required field populated
// so tests can copy + tweak per case.
func baseCreate(wsID, scopeID string) admin.CreateInput {
	return admin.CreateInput{
		Name:           "digest-bot",
		ModelProvider:  "anthropic",
		ModelID:        "claude-opus-4-7",
		SecretScopeID:  scopeID,
		PromptTemplate: "Summarise {topic}.",
		ScheduleSpec:   "0 9 * * *",
		WorkspaceID:    wsID,
	}
}

func TestServiceCreateAppliesDefaults(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	in := baseCreate(wsID, scopeID)

	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.ID == "" {
		t.Error("ID not generated")
	}
	if w.ExecMode != "propose" {
		t.Errorf("exec_mode default = %q, want propose", w.ExecMode)
	}
	if w.ConcurrencyPolicy != "skip" {
		t.Errorf("concurrency_policy default = %q, want skip", w.ConcurrencyPolicy)
	}
	if w.ToolAllowlistJSON != "[]" {
		t.Errorf("tool_allowlist_json default = %q, want []", w.ToolAllowlistJSON)
	}
	if !strings.Contains(w.OutputChannelsJSON, `"mesh"`) {
		t.Errorf("output_channels_json default = %q (want mesh sink)", w.OutputChannelsJSON)
	}
	if !w.Enabled {
		t.Error("enabled default = false, want true")
	}
}

func TestServiceCreateValidation(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		mut        func(*admin.CreateInput)
		errSub     string
		exampleSub string // optional: substring expected in the "Example:" hint
	}{
		{"missing name", func(c *admin.CreateInput) { c.Name = "" }, "name required", ""},
		// model_provider/missing and model_id/missing share an example
		// (opencode_cli + minimax/MiniMax-M3) so the cheap-model repair
		// path is obvious regardless of which field the user blanked.
		{"missing provider", func(c *admin.CreateInput) { c.ModelProvider = "" }, "model_provider required", `Example: {"name":"digest-bot", "model_provider":"opencode_cli"`},
		{"missing model_id", func(c *admin.CreateInput) { c.ModelID = "" }, "model_id required", `Example: {"name":"digest-bot", "model_provider":"opencode_cli", "model_id":"minimax/MiniMax-M3"`},
		{"missing prompt", func(c *admin.CreateInput) { c.PromptTemplate = "" }, "prompt_template required", ""},
		{"missing schedule", func(c *admin.CreateInput) { c.ScheduleSpec = "" }, "schedule_spec required", ""},
		{"missing workspace", func(c *admin.CreateInput) { c.WorkspaceID = "" }, "workspace_id required", ""},
		{"missing scope", func(c *admin.CreateInput) { c.SecretScopeID = "" }, "secret_scope_id required", `Example: {"name":"digest-bot", "model_provider":"anthropic", "model_id":"claude-sonnet-4-5", "secret_scope_id":"scope-anthropic-prod"`},
		{"prompt too large", func(c *admin.CreateInput) {
			c.PromptTemplate = strings.Repeat("p", 128*1024+1)
		}, "prompt_template max", ""},
		{"bad provider", func(c *admin.CreateInput) { c.ModelProvider = "huggingface" }, "model_provider", ""},
		{"compat needs endpoint", func(c *admin.CreateInput) {
			c.ModelProvider = "openai_compat"
		}, "model_endpoint_url required", ""},
		{"bad exec_mode", func(c *admin.CreateInput) { c.ExecMode = "yolo" }, "exec_mode", ""},
		{"bad policy", func(c *admin.CreateInput) { c.ConcurrencyPolicy = "burst" }, "concurrency_policy", ""},
		// SECURITY: malformed tool_allowlist_json must be rejected at
		// create time so operators get immediate feedback instead of a
		// silently-deny-everything worker (the runner-side parser fails
		// closed for defence-in-depth).
		{"bad allowlist json", func(c *admin.CreateInput) {
			c.ToolAllowlistJSON = "not-json"
		}, "tool_allowlist_json", ""},
		{"allowlist not array", func(c *admin.CreateInput) {
			c.ToolAllowlistJSON = `{"oops":"object"}`
		}, "tool_allowlist_json", ""},
		{"allowlist empty entry", func(c *admin.CreateInput) {
			c.ToolAllowlistJSON = `["valid", ""]`
		}, "empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseCreate(wsID, scopeID)
			c.mut(&in)
			_, err := svc.Create(ctx, in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.errSub)
			}
			if !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not contain %q", err, c.errSub)
			}
			if c.exampleSub != "" && !strings.Contains(err.Error(), c.exampleSub) {
				t.Errorf("error %q does not contain example %q", err, c.exampleSub)
			}
		})
	}
}

// TestServiceDelegationValidationExamples pins the example substring
// on the three delegation_models.go errors (model_provider required,
// model_id required, secret_scope_id required). These are surfaced
// when delegate_worker is called without one of the required fields,
// so a cheap model that forgets one gets an in-line corrected example
// derived from the accepted enum in admin/validate.go
// (anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli)
// and a real model id from the test corpus.
func TestServiceDelegationValidationExamples(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		mut        func(*admin.DelegationInput)
		errSub     string
		exampleSub string
	}{
		{
			name: "missing model_provider",
			mut:  func(in *admin.DelegationInput) { in.ModelProvider = "" },
			// base create populates scope + model_id, so the only
			// missing field in the delegation path is the provider.
			errSub:     "model_provider required",
			exampleSub: `Example: {"objective":"summarise recent changes", "model_provider":"opencode_cli", "model_id":"minimax/MiniMax-M3"`,
		},
		{
			name:       "missing model_id",
			mut:        func(in *admin.DelegationInput) { in.ModelID = "" },
			errSub:     "model_id required",
			exampleSub: `Example: {"objective":"summarise recent changes", "model_provider":"opencode_cli", "model_id":"minimax/MiniMax-M3"`,
		},
		{
			name: "missing secret_scope_id",
			// Use a provider that REQUIRES a secret_scope_id (anthropic)
			// and blank the scope so the third error path fires. CLI
			// providers get the scope auto-filled, so we can't reuse the
			// base provider here.
			mut: func(in *admin.DelegationInput) {
				in.ModelProvider = "anthropic"
				in.ModelID = "claude-sonnet-4-5"
				in.SecretScopeID = ""
			},
			errSub:     "secret_scope_id required",
			exampleSub: `Example: {"objective":"summarise recent changes", "model_provider":"anthropic", "model_id":"claude-sonnet-4-5", "secret_scope_id":"scope-anthropic-prod"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := admin.DelegationInput{
				WorkspaceID:   wsID,
				Objective:     "summarise recent changes",
				ModelProvider: "opencode_cli",
				ModelID:       "minimax/MiniMax-M3",
				SecretScopeID: scopeID,
			}
			c.mut(&in)
			_, err := svc.Delegate(ctx, in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.errSub)
			}
			if !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not contain %q", err, c.errSub)
			}
			if c.exampleSub != "" && !strings.Contains(err.Error(), c.exampleSub) {
				t.Errorf("error %q does not contain example %q", err, c.exampleSub)
			}
		})
	}
}

// TestServiceManualScheduleAcceptedAtCreateAndUpdate covers the
// "schedule_spec=manual" sentinel: workers that fire only via mesh
// triggers / RunNow. The default validator must accept it at create
// and flipping an existing worker from a cron spec to "manual" must
// succeed.
func TestServiceManualScheduleAcceptedAtCreateAndUpdate(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	in := baseCreate(wsID, scopeID)
	in.Name = "mesh-only-bot"
	in.ScheduleSpec = "manual"
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create with manual schedule: %v", err)
	}
	if w.ScheduleSpec != "manual" {
		t.Errorf("schedule_spec = %q, want manual", w.ScheduleSpec)
	}

	// Flip an existing scheduled worker to manual.
	sched := baseCreate(wsID, scopeID)
	sched.Name = "flip-to-manual"
	scheduled, err := svc.Create(ctx, sched)
	if err != nil {
		t.Fatalf("create scheduled worker: %v", err)
	}
	manual := "manual"
	updated, err := svc.Update(ctx, admin.UpdateInput{
		ID:           scheduled.ID,
		ScheduleSpec: &manual,
	})
	if err != nil {
		t.Fatalf("update to manual: %v", err)
	}
	if updated.ScheduleSpec != "manual" {
		t.Errorf("schedule_spec post-update = %q, want manual", updated.ScheduleSpec)
	}
}

func TestServiceUpdatePartial(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Update only the description; every other field must stay intact.
	newDesc := "updated"
	updated, err := svc.Update(ctx, admin.UpdateInput{
		ID:          w.ID,
		Description: &newDesc,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Description != newDesc {
		t.Errorf("description = %q, want %q", updated.Description, newDesc)
	}
	if updated.PromptTemplate != w.PromptTemplate {
		t.Errorf("prompt unexpectedly changed: was %q, now %q",
			w.PromptTemplate, updated.PromptTemplate)
	}
	if updated.ScheduleSpec != w.ScheduleSpec {
		t.Errorf("schedule unexpectedly changed: was %q, now %q",
			w.ScheduleSpec, updated.ScheduleSpec)
	}
	if !updated.UpdatedAt.After(w.UpdatedAt) && !updated.UpdatedAt.Equal(w.UpdatedAt) {
		t.Errorf("updated_at should not move backwards (was %v, now %v)",
			w.UpdatedAt, updated.UpdatedAt)
	}
}

// TestServiceUpdateValidation pins the Update-side re-validation of the
// load-bearing whitelists + safety caps. Create rejects these (see
// TestServiceCreateValidation), but Update historically applied them
// verbatim — exec_mode/concurrency_policy bypassed the whitelist and
// negative caps defeated the budget guard, because the SQLite columns
// have no CHECK constraint and applyWorkerDefaults only touches EMPTY
// values. Each case mutates one field on an UpdateInput against a
// freshly created baseline worker and asserts a rejection.
func TestServiceUpdateValidation(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	negTokens := -1
	negToolCalls := -5
	negWall := -10
	negFailures := -1
	negCost := -1.0
	badMode := "yolo"
	badPolicy := "burst"

	cases := []struct {
		name   string
		mut    func(*admin.UpdateInput)
		errSub string
	}{
		{"bad exec_mode", func(u *admin.UpdateInput) { u.ExecMode = &badMode }, "exec_mode"},
		{"bad concurrency_policy", func(u *admin.UpdateInput) { u.ConcurrencyPolicy = &badPolicy }, "concurrency_policy"},
		{"negative max_input_tokens", func(u *admin.UpdateInput) { u.MaxInputTokens = &negTokens }, "max_input_tokens"},
		{"negative max_tool_calls", func(u *admin.UpdateInput) { u.MaxToolCalls = &negToolCalls }, "max_tool_calls"},
		{"negative max_wall_clock_seconds", func(u *admin.UpdateInput) { u.MaxWallClockSeconds = &negWall }, "max_wall_clock_seconds"},
		{"negative max_consecutive_failures", func(u *admin.UpdateInput) { u.MaxConsecutiveFailures = &negFailures }, "max_consecutive_failures"},
		{"negative max_monthly_cost_usd", func(u *admin.UpdateInput) { u.MaxMonthlyCostUSD = &negCost }, "max_monthly_cost_usd"},
		{"prompt too large", func(u *admin.UpdateInput) {
			prompt := strings.Repeat("p", 128*1024+1)
			u.PromptTemplate = &prompt
		}, "prompt_template max"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := baseCreate(wsID, scopeID)
			base.Name = "update-validation-" + c.name
			w, err := svc.Create(ctx, base)
			if err != nil {
				t.Fatalf("create baseline: %v", err)
			}
			in := admin.UpdateInput{ID: w.ID}
			c.mut(&in)
			_, err = svc.Update(ctx, in)
			if err == nil {
				t.Fatalf("expected error (case %q), got nil", c.name)
			}
			if c.errSub != "" && !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not contain %q", err, c.errSub)
			}
		})
	}
}

func TestServiceListFilters(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	// Two workers: one enabled, one paused.
	a := baseCreate(wsID, scopeID)
	a.Name = "alpha"
	if _, err := svc.Create(ctx, a); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	b := baseCreate(wsID, scopeID)
	b.Name = "beta"
	disabled := false
	b.Enabled = &disabled
	if _, err := svc.Create(ctx, b); err != nil {
		t.Fatalf("create beta: %v", err)
	}

	// Add a second workspace + worker to exercise the cross-workspace path.
	ws2 := &store.Workspace{Name: "other", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws2); err != nil {
		t.Fatalf("create ws2: %v", err)
	}
	g := baseCreate(ws2.ID, scopeID)
	g.Name = "gamma"
	if _, err := svc.Create(ctx, g); err != nil {
		t.Fatalf("create gamma: %v", err)
	}

	all, err := svc.List(ctx, admin.ListInput{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("list all len = %d, want 3", len(all))
	}

	enabledOnly, _ := svc.List(ctx, admin.ListInput{EnabledOnly: true})
	if len(enabledOnly) != 2 {
		t.Errorf("enabled-only len = %d, want 2", len(enabledOnly))
	}

	scoped, _ := svc.List(ctx, admin.ListInput{WorkspaceID: ws2.ID})
	if len(scoped) != 1 || scoped[0].Name != "gamma" {
		t.Errorf("ws2 scope = %+v, want one gamma row", scoped)
	}

	matched, _ := svc.List(ctx, admin.ListInput{NamePattern: "ALPHA"})
	if len(matched) != 1 || matched[0].Name != "alpha" {
		t.Errorf("pattern match = %+v, want one alpha row", matched)
	}
}

func TestServiceArchiveHidesFromDefaultList(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	archived, err := svc.Archive(ctx, w.ID, "stale one-shot")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Enabled {
		t.Fatal("archived worker is still enabled")
	}
	if archived.ArchivedAt == nil {
		t.Fatal("archived_at was not stamped")
	}
	if _, err := svc.Resume(ctx, w.ID); !errors.Is(err, store.ErrWorkerArchived) {
		t.Fatalf("resume archived err = %v, want ErrWorkerArchived", err)
	}

	rows, err := svc.List(ctx, admin.ListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("default list returned archived rows: %+v", rows)
	}

	rows, err = svc.List(ctx, admin.ListInput{WorkspaceID: wsID, IncludeArchived: true})
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(rows) != 1 || !rows[0].Archived {
		t.Fatalf("include_archived rows = %+v, want archived row", rows)
	}
}

func TestServiceListMarksDelegationWorkersEphemeral(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	svc, _, wsID, _ := newTestService(t)
	svc.SetRunnerForTest(&fakeRunner{runID: "run-delegation"})
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:     wsID,
		Objective:       "Review workers page realtime behavior",
		TaskID:          "task-123",
		TaskKind:        "code-review",
		WorkerMode:      "review",
		ModelProvider:   "opencode_cli",
		ModelID:         "minimax/MiniMax-M3",
		MaxToolCalls:    1,
		MaxOutputTokens: 1,
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	rows, err := svc.List(ctx, admin.ListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("list len = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if !row.Ephemeral {
		t.Fatalf("Ephemeral = false, want true: %+v", row)
	}
	if row.DelegationID != out.DelegationID {
		t.Fatalf("DelegationID = %q, want %q", row.DelegationID, out.DelegationID)
	}
	if row.DelegationObjective != "Review workers page realtime behavior" {
		t.Fatalf("DelegationObjective = %q", row.DelegationObjective)
	}
	if row.DelegationTaskID != "task-123" || row.DelegationTaskKind != "code_review" {
		t.Fatalf("task metadata = %q/%q", row.DelegationTaskID, row.DelegationTaskKind)
	}
	if row.DelegationWorkerMode != "review" {
		t.Fatalf("DelegationWorkerMode = %q, want review", row.DelegationWorkerMode)
	}
}

func TestServicePauseResumeIdempotent(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	paused, err := svc.SetEnabled(ctx, w.ID, false)
	if err != nil || paused.Enabled {
		t.Fatalf("pause failed: enabled=%v err=%v", paused.Enabled, err)
	}
	// Idempotent — second pause is a no-op.
	if _, err := svc.SetEnabled(ctx, w.ID, false); err != nil {
		t.Fatalf("second pause errored: %v", err)
	}
	resumed, _ := svc.SetEnabled(ctx, w.ID, true)
	if !resumed.Enabled {
		t.Error("resume did not flip enabled back on")
	}
}

func TestServiceGetMissing(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.Get(context.Background(), admin.GetInput{ID: "wkr-nope"})
	if !errors.Is(err, store.ErrWorkerNotFound) {
		t.Errorf("get missing: got %v, want ErrWorkerNotFound", err)
	}
}

// TestServiceGetRecentRunsEmptyArray locks in the JSON contract: a
// freshly created worker with no runs MUST serialise recent_runs as an
// empty JSON array, not null. The dashboard / MCP clients iterate the
// slice directly and crash on null.
func TestServiceGetRecentRunsEmptyArray(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: w.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RecentRuns == nil {
		t.Fatal("RecentRuns is nil; want empty (non-nil) slice")
	}
	if len(got.RecentRuns) != 0 {
		t.Fatalf("RecentRuns len = %d, want 0", len(got.RecentRuns))
	}
}

func TestServiceDelete(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, admin.GetInput{ID: w.ID}); !errors.Is(err, store.ErrWorkerNotFound) {
		t.Errorf("expected not-found after delete, got %v", err)
	}
}
