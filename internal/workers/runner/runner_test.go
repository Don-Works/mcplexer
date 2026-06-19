package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// --- fakes ---

type fakeAdapter struct {
	mu        sync.Mutex
	responses []models.SendResponse
	err       error
	panicVal  any
	calls     int
	// lastWorkspacePath records the WorkspacePath of the most recent
	// SendRequest so tests can assert the worker's bound CWD is threaded
	// through to the adapter (which sets it as the subprocess cmd.Dir).
	lastWorkspacePath string
	requests          []models.SendRequest
}

func (f *fakeAdapter) Send(_ context.Context, req models.SendRequest) (*models.SendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastWorkspacePath = req.WorkspacePath
	recorded := req
	recorded.Messages = append([]models.Message(nil), req.Messages...)
	recorded.Tools = append([]models.ToolSchema(nil), req.Tools...)
	f.requests = append(f.requests, recorded)
	if f.panicVal != nil {
		panic(f.panicVal)
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.calls >= len(f.responses) {
		return &models.SendResponse{Text: "", StopReason: models.StopEndTurn}, nil
	}
	resp := f.responses[f.calls]
	f.calls++
	return &resp, nil
}

type fakeDispatcher struct {
	tools      []models.ToolSchema
	results    map[string]runner.ToolCallResult
	dispatched []runner.ToolCallRequest
	err        error
	// writeTools — set of tool names this fake reports as write-class
	// from Classify (pre-dispatch gate). Mirrors the WriteClass flag the
	// fake later returns from DispatchTool so tests stay consistent.
	writeTools map[string]bool
}

func (f *fakeDispatcher) ListTools(_ context.Context, _ []string) ([]models.ToolSchema, error) {
	return f.tools, nil
}

func (f *fakeDispatcher) Classify(name string) bool {
	if f.writeTools != nil && f.writeTools[name] {
		return true
	}
	if r, ok := f.results[name]; ok {
		return r.WriteClass
	}
	return false
}

func (f *fakeDispatcher) DispatchTool(_ context.Context, req runner.ToolCallRequest) (runner.ToolCallResult, error) {
	f.dispatched = append(f.dispatched, req)
	if f.err != nil {
		return runner.ToolCallResult{}, f.err
	}
	if r, ok := f.results[req.Name]; ok {
		return r, nil
	}
	return runner.ToolCallResult{OutputJSON: `{"ok":true}`}, nil
}

type fakeMesh struct {
	mu   sync.Mutex
	sent []runner.MeshOutbound
	err  error
}

func (f *fakeMesh) Send(_ context.Context, msg runner.MeshOutbound) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, msg)
	return "msg-" + msg.Kind + "-" + msg.Tags, nil
}

func (f *fakeMesh) tags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, m := range f.sent {
		out = append(out, m.Tags)
	}
	return out
}

type fakeSecrets struct {
	values map[string]map[string][]byte
	err    error
}

func (f *fakeSecrets) Get(_ context.Context, scopeID, key string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if scope, ok := f.values[scopeID]; ok {
		if v, ok := scope[key]; ok {
			return v, nil
		}
	}
	// Catch-all so tests that don't care about the exact secret value
	// still get a non-empty string the model adapter accepts.
	return []byte("test-key"), nil
}

type fakeSkills struct {
	bodies map[string]string
}

func (f *fakeSkills) GetSkillBody(_ context.Context, _, name, _ string) (string, error) {
	if b, ok := f.bodies[name]; ok {
		return b, nil
	}
	return "", errors.New("skill not found")
}

type fakeClock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.current
	c.current = c.current.Add(c.step)
	return now
}

// --- helpers ---

func newTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// setupFKs creates the workspace + auth_scope FKs every Worker needs so
// CreateWorker satisfies the schema constraints. Returns (workspaceID,
// authScopeID).
func setupFKs(t *testing.T, db *sqlite.DB) (string, string) {
	t.Helper()
	ws := &store.Workspace{Name: "test-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	as := &store.AuthScope{Name: "test-scope", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), as); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	return ws.ID, as.ID
}

func createWorker(t *testing.T, db *sqlite.DB, w *store.Worker) {
	t.Helper()
	if err := db.CreateWorker(context.Background(), w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
}

func sampleWorker(workspaceID, scopeID string) *store.Worker {
	return &store.Worker{
		Name:           "demo",
		ModelProvider:  models.ProviderAnthropic,
		ModelID:        "claude-sonnet-4-6",
		SecretScopeID:  scopeID,
		WorkspaceID:    workspaceID,
		PromptTemplate: "Review {topic} for accuracy",
		ParametersJSON: `{"topic":"reddit ads"}`,
		ScheduleSpec:   "@daily",
		ExecMode:       runner.ExecModeAutonomous,
		Enabled:        true,
	}
}

func makeRunner(t *testing.T, db *sqlite.DB, adapter models.ModelAdapter, disp *fakeDispatcher, mesh *fakeMesh) *runner.Runner {
	t.Helper()
	return runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       mesh,
		Secrets:    &fakeSecrets{values: map[string]map[string][]byte{
			// scopeID-agnostic catch-all so the test fakeSecrets returns
			// a value for any scope without a per-test map. The Manager
			// shim in production reads scopeID + key; here we accept any.
		}, err: nil},
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
}

// --- tests ---

func TestRunWithOptsDisabledWorkerDoesNotStart(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.Enabled = false
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "should not run", StopReason: models.StopEndTurn},
	}}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	runID, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{})
	if !errors.Is(err, runner.ErrWorkerDisabled) {
		t.Fatalf("err = %v, want ErrWorkerDisabled", err)
	}
	if runID != "" {
		t.Fatalf("runID = %q, want empty", runID)
	}
	runs, err := db.ListWorkerRuns(context.Background(), w.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("created %d run rows for disabled worker, want 0", len(runs))
	}
	adapter.mu.Lock()
	requests := len(adapter.requests)
	adapter.mu.Unlock()
	if requests != 0 {
		t.Fatalf("adapter received %d requests, want 0", requests)
	}
}

func TestRun_TextOnlySuccess(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "All clear, no issues found.", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if run.OutputText != "All clear, no issues found." {
		t.Fatalf("output = %q", run.OutputText)
	}
	if !strings.Contains(strings.Join(mesh.tags(), "|"), "started") {
		t.Fatalf("expected worker.started signal, got %v", mesh.tags())
	}
	if !strings.Contains(strings.Join(mesh.tags(), "|"), "finished") {
		t.Fatalf("expected worker.finished signal, got %v", mesh.tags())
	}
}

func TestRun_MultiTurnToolUse(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ToolAllowlistJSON = `["search"]`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "search", Input: map[string]any{"q": "abc"}}}, StopReason: models.StopToolUse},
		{Text: "Found 3 results.", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"search": {OutputJSON: `{"hits":3}`},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if run.ToolCallsCount != 1 {
		t.Fatalf("tool_calls_count = %d, want 1", run.ToolCallsCount)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0].Name != "search" {
		t.Fatalf("dispatched = %v", disp.dispatched)
	}
}

func TestRun_CapExceededIterations(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	// Always returns a tool call so the loop never ends naturally.
	adapter := &fakeAdapter{responses: []models.SendResponse{}}
	infinite := []models.SendResponse{}
	for range 50 {
		infinite = append(infinite, models.SendResponse{
			ToolCalls:  []models.ToolCall{{ID: "t", Name: "noop"}},
			StopReason: models.StopToolUse,
		})
	}
	adapter.responses = infinite
	disp := &fakeDispatcher{}
	mesh := &fakeMesh{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       mesh,
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Caps:       runner.Caps{MaxIterations: 3, MaxToolCalls: 100, MaxWallClock: time.Hour},
	})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "iterations") {
		t.Fatalf("error = %q, want mention of iterations", run.Error)
	}
}

func TestRun_CapExceededWallClock(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t", Name: "noop"}}, StopReason: models.StopToolUse},
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{}
	// fakeClock returns t, t+1s, t+10s... so the second cap check trips.
	startedAt := time.Now()
	clock := &fakeClock{current: startedAt, step: 5 * time.Second}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Clock:      clock,
		Caps:       runner.Caps{MaxIterations: 100, MaxToolCalls: 100, MaxWallClock: 3 * time.Second},
	})

	runID, _ := r.Run(context.Background(), w.ID)
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", run.Status)
	}
	if !strings.Contains(run.Error, "wall-clock") {
		t.Fatalf("error = %q, want mention of wall-clock", run.Error)
	}
}

// blockingAdapter blocks inside Send until ctx is cancelled or `done`
// is closed. Models the case where a subprocess hangs mid-iteration —
// the between-iteration wall-clock check never gets a chance to fire,
// so the only way to kill the run is via ctx.
type blockingAdapter struct {
	done chan struct{}
}

func (b *blockingAdapter) Send(ctx context.Context, _ models.SendRequest) (*models.SendResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.done:
		return &models.SendResponse{Text: "done", StopReason: models.StopEndTurn}, nil
	}
}

// TestRun_WallClockKillsStuckAdapter — verify that a wall-clock budget
// fires INSIDE adapter.Send (via context.WithDeadline) rather than only
// between iterations. Before this fix a hung subprocess would stall the
// run indefinitely; the between-iteration check only ran at iteration
// boundaries, which the adapter never reached.
func TestRun_WallClockKillsStuckAdapter(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &blockingAdapter{done: make(chan struct{})}
	disp := &fakeDispatcher{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Caps:       runner.Caps{MaxIterations: 100, MaxToolCalls: 100, MaxWallClock: 250 * time.Millisecond},
	})

	start := time.Now()
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("run took %s, wall-clock cap did not kill adapter mid-iteration", elapsed)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded (run.Error=%q)", run.Status, run.Error)
	}
	if !strings.Contains(run.Error, "wall-clock") {
		t.Fatalf("error = %q, want mention of wall-clock", run.Error)
	}
}

func TestRun_ProposeModeBlocksWriteTool(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"post_message": {OutputJSON: `{"would_post":true}`, WriteClass: true},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusAwaitingApproval {
		t.Fatalf("status = %q, want awaiting_approval", run.Status)
	}
	tagJoin := strings.Join(mesh.tags(), "|")
	if !strings.Contains(tagJoin, "awaiting_approval") {
		t.Fatalf("expected worker.awaiting_approval signal, got %v", mesh.tags())
	}
}

// TestRun_ProposeModeNeverDispatchesWriteTool — the SECURITY contract:
// in propose mode the runner MUST consult Classify BEFORE DispatchTool,
// so a write-class tool never executes its side effects. This test
// asserts disp.dispatched stays empty even though the model offered a
// write-class tool call.
func TestRun_ProposeModeNeverDispatchesWriteTool(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
	}}
	// writeTools makes Classify return true for "post_message" so the
	// pre-dispatch gate fires BEFORE DispatchTool is invoked.
	disp := &fakeDispatcher{
		writeTools: map[string]bool{"post_message": true},
		results:    map[string]runner.ToolCallResult{"post_message": {WriteClass: true}},
	}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusAwaitingApproval {
		t.Fatalf("status = %q, want awaiting_approval", run.Status)
	}
	if len(disp.dispatched) != 0 {
		t.Fatalf("SECURITY VIOLATION: write tool was dispatched %d times in propose mode; want 0", len(disp.dispatched))
	}
}

// TestRun_ProposeModePreApprovedSkipsGate — pre-approved tools bypass
// the pre-dispatch gate so the resume-after-approval flow can actually
// execute the previously-flagged write.
func TestRun_ProposeModePreApprovedSkipsGate(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{
		writeTools: map[string]bool{"post_message": true},
		results:    map[string]runner.ToolCallResult{"post_message": {OutputJSON: `{"posted":true}`, WriteClass: true}},
	}
	r := makeRunner(t, db, adapter, disp, &fakeMesh{})

	runID, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{
		PreApprovedTools: []string{"post_message"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if len(disp.dispatched) != 1 {
		t.Fatalf("dispatched = %d, want 1 (pre-approved should execute)", len(disp.dispatched))
	}
}

func TestRun_AutonomousModeExecutesWriteTool(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModeAutonomous
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
		{Text: "Done.", StopReason: models.StopEndTurn},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"post_message": {OutputJSON: `{"posted":true}`, WriteClass: true},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, _ := r.Run(context.Background(), w.ID)
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success (autonomous executes)", run.Status)
	}
	if len(disp.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(disp.dispatched))
	}
}

func TestRun_AdapterErrorFails(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{err: errors.New("boom")}
	disp := &fakeDispatcher{}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, disp, mesh)

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusFailure {
		t.Fatalf("status = %q, want failure", run.Status)
	}
	if !strings.Contains(run.Error, "boom") {
		t.Fatalf("error = %q", run.Error)
	}
}

func TestRun_PanicFinalizesFailure(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{panicVal: "adapter exploded"}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, err := db.GetWorkerRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get worker run: %v", err)
	}
	if run.Status != runner.StatusFailure {
		t.Fatalf("status = %q, want failure", run.Status)
	}
	if !strings.Contains(run.Error, "runner panic: adapter exploded") {
		t.Fatalf("error = %q", run.Error)
	}
}

func TestRun_OutputToFile(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	outputsRoot := t.TempDir()
	// Path is RELATIVE — file channel jails everything under outputsRoot.
	w.OutputChannelsJSON = `[{"type":"file","path":"out.md","mode":"overwrite"}]`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "hello-from-worker", StopReason: models.StopEndTurn},
	}}
	r := makeRunnerWithOutputsDir(t, db, adapter, &fakeDispatcher{}, &fakeMesh{}, outputsRoot)
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outputsRoot, "out.md"))
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(body) != "hello-from-worker" {
		t.Fatalf("file content = %q", string(body))
	}
}

// TestRun_OutputToFile_EscapeRejected verifies the file-channel sandbox.
// A worker that tries to write outside the configured outputs root must
// fail emission (the writeFileOutput error path reports it via a mesh
// alert; the run still completes successfully because output emission
// is best-effort).
//
// We avoid hardcoded `/tmp` paths so a stale file from a previous run
// can't mask a regression. The escape target lives under a SECOND
// t.TempDir() that the file channel is NOT configured to write to —
// rooted in cleaned test storage so the assertion is deterministic and
// self-cleaning.
func TestRun_OutputToFile_EscapeRejected(t *testing.T) {
	t.Run("relative_dotdot_escape", func(t *testing.T) {
		db := newTestStore(t)
		wsID, scopeID := setupFKs(t, db)
		w := sampleWorker(wsID, scopeID)
		outputsRoot := t.TempDir()
		// Build an escape path that climbs three levels then dives into
		// a known sibling temp dir. Both directories live under
		// t.TempDir() so the assertion has a controlled, cleaned root.
		escapeParent := t.TempDir()
		// Compute the number of ".." segments needed to escape out of
		// outputsRoot to escapeParent's level. Use filepath.Rel so we
		// don't hardcode platform-specific separators.
		rel, err := filepath.Rel(outputsRoot, filepath.Join(escapeParent, "escape.txt"))
		if err != nil {
			t.Fatalf("compute rel: %v", err)
		}
		w.OutputChannelsJSON = `[{"type":"file","path":` +
			mustJSONString(rel) + `,"mode":"overwrite"}]`
		createWorker(t, db, w)

		adapter := &fakeAdapter{responses: []models.SendResponse{
			{Text: "should-not-escape", StopReason: models.StopEndTurn},
		}}
		mesh := &fakeMesh{}
		r := makeRunnerWithOutputsDir(t, db, adapter, &fakeDispatcher{}, mesh, outputsRoot)
		if _, err := r.Run(context.Background(), w.ID); err != nil {
			t.Fatalf("run: %v", err)
		}
		escapeTarget := filepath.Join(escapeParent, "escape.txt")
		if _, err := os.Stat(escapeTarget); err == nil {
			t.Fatalf("path-escape was NOT blocked: %s exists", escapeTarget)
		}
		assertEscapeReportedToMesh(t, mesh, "file")
		assertNoEscapeArtifact(t, outputsRoot)
	})

	t.Run("absolute_path_rejected", func(t *testing.T) {
		db := newTestStore(t)
		wsID, scopeID := setupFKs(t, db)
		w := sampleWorker(wsID, scopeID)
		outputsRoot := t.TempDir()
		// An absolute path outside the outputs root must also be
		// rejected — resolveOutputPath jails on filepath.Rel + the
		// "../" prefix scan regardless of how the user wrote the path.
		// We point at a path that DOES NOT EXIST so a successful write
		// would fail-loud at os.Stat.
		escapeParent := t.TempDir()
		escapeTarget := filepath.Join(escapeParent, "absolute-escape.txt")
		w.OutputChannelsJSON = `[{"type":"file","path":` +
			mustJSONString(escapeTarget) + `,"mode":"overwrite"}]`
		createWorker(t, db, w)

		adapter := &fakeAdapter{responses: []models.SendResponse{
			{Text: "should-not-escape-abs", StopReason: models.StopEndTurn},
		}}
		mesh := &fakeMesh{}
		r := makeRunnerWithOutputsDir(t, db, adapter, &fakeDispatcher{}, mesh, outputsRoot)
		if _, err := r.Run(context.Background(), w.ID); err != nil {
			t.Fatalf("run: %v", err)
		}
		if _, err := os.Stat(escapeTarget); err == nil {
			t.Fatalf("absolute-path escape was NOT blocked: %s exists", escapeTarget)
		}
		assertEscapeReportedToMesh(t, mesh, "file")
	})
}

// mustJSONString returns the JSON-quoted form of s. Keeps the worker
// config string literal readable when the path itself contains slashes
// or special characters.
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// assertEscapeReportedToMesh fails the test when the file-channel
// emission failure didn't surface as a high-priority mesh alert with
// the "error" tag. That's the runner's contract: a write that was
// blocked at jail time still has to be visible to the operator.
func assertEscapeReportedToMesh(t *testing.T, mesh *fakeMesh, channel string) {
	t.Helper()
	for _, tags := range mesh.tags() {
		if strings.Contains(tags, channel) && strings.Contains(tags, "error") {
			return
		}
	}
	t.Fatalf("expected mesh alert tagged %q+error; got tags=%v", channel, mesh.tags())
}

// assertNoEscapeArtifact verifies the outputs root has no
// "escape.txt"-named entry — `..`-collapsed paths must NOT silently
// land inside the jail either.
func assertNoEscapeArtifact(t *testing.T, outputsRoot string) {
	t.Helper()
	entries, err := os.ReadDir(outputsRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == "escape.txt" {
			t.Fatalf("path-escape collapsed into in-jail file: %s", e.Name())
		}
	}
}

// makeRunnerWithOutputsDir builds the runner with an explicit file-channel
// jail directory. Used by file-output tests so the writeFileOutput
// security guard has a real root to enforce.
func makeRunnerWithOutputsDir(
	t *testing.T, db *sqlite.DB, adapter models.ModelAdapter,
	disp *fakeDispatcher, mesh *fakeMesh, outputsDir string,
) *runner.Runner {
	t.Helper()
	return runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       mesh,
		Secrets:    &fakeSecrets{},
		OutputsDir: outputsDir,
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
}

func TestRun_OutputToMesh(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.OutputChannelsJSON = `[{"type":"mesh","priority":"normal","tags":"daily,review"}]`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "summary text", StopReason: models.StopEndTurn},
	}}
	mesh := &fakeMesh{}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, mesh)
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	var foundOutput bool
	for _, m := range mesh.sent {
		if m.Kind == "finding" && strings.Contains(m.Content, "summary text") {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatalf("expected finding-kind mesh output, got %d messages", len(mesh.sent))
	}
}

func TestRenderPrompt(t *testing.T) {
	cases := []struct {
		name    string
		tmpl    string
		params  string
		want    string
		wantErr bool
	}{
		{"substitutes known", "Hello {who}", `{"who":"world"}`, "Hello world", false},
		{"leaves unknown", "Use {literal} braces", `{"who":"x"}`, "Use {literal} braces", false},
		{"empty params", "Plain text", "", "Plain text", false},
		{"empty {} object", "Plain text", "{}", "Plain text", false},
		{"int value", "n={n}", `{"n":42}`, "n=42", false},
		{"bad json", "x", "not json", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runner.RenderPromptForTest(tc.tmpl, tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestResolveWorkspacePath_EnsuresDir is the regression guard for the
// telegram-responder failure: a worker bound to a workspace whose
// root_path doesn't exist (e.g. an ephemeral /tmp path wiped on reboot)
// must not hard-fail at the subprocess chdir. resolveWorkspacePath
// MkdirAll's the path before binding, and falls back to "" (daemon CWD)
// when it can't.
func TestResolveWorkspacePath_EnsuresDir(t *testing.T) {
	db := newTestStore(t)
	r := makeRunner(t, db, &fakeAdapter{}, &fakeDispatcher{}, &fakeMesh{})
	ctx := context.Background()

	t.Run("creates missing dir and returns it", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "nested", "telegram-workspace")
		ws := &store.Workspace{Name: "tg", RootPath: root, DefaultPolicy: "allow"}
		if err := db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		got := r.ResolveWorkspacePathForTest(ctx, ws.ID)
		if got != root {
			t.Fatalf("path = %q, want %q", got, root)
		}
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			t.Fatalf("expected %q to exist as a dir, stat err=%v", root, err)
		}
	})

	t.Run("falls back to empty when creation impossible", func(t *testing.T) {
		// Rooting under a regular file makes MkdirAll fail (ENOTDIR).
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		ws := &store.Workspace{Name: "bad", RootPath: filepath.Join(file, "sub"), DefaultPolicy: "allow"}
		if err := db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		if got := r.ResolveWorkspacePathForTest(ctx, ws.ID); got != "" {
			t.Fatalf("path = %q, want \"\" (daemon CWD fallback)", got)
		}
	})

	t.Run("sentinel root returns empty without touching disk", func(t *testing.T) {
		ws := &store.Workspace{Name: "root", RootPath: "/", DefaultPolicy: "allow"}
		if err := db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		if got := r.ResolveWorkspacePathForTest(ctx, ws.ID); got != "" {
			t.Fatalf("path = %q, want \"\"", got)
		}
	})
}

// TestRun_MissingWorkspaceDirCreatedEndToEnd is the end-to-end guard for
// the telegram-responder regression: a full Run() against a worker bound
// to a workspace whose root_path does not exist must succeed (the dir is
// created), and the bound path must reach the adapter as the subprocess
// CWD instead of producing a "chdir: no such file or directory" failure.
func TestRun_MissingWorkspaceDirCreatedEndToEnd(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "ephemeral", "telegram-workspace")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("precondition: %q should not exist yet", root)
	}
	ws := &store.Workspace{Name: "tg-ws", RootPath: root, DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	as := &store.AuthScope{Name: "tg-scope", Type: "env"}
	if err := db.CreateAuthScope(ctx, as); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	w := sampleWorker(ws.ID, as.ID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "sent", StopReason: models.StopEndTurn},
	}}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})

	runID, err := r.Run(ctx, w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, err := db.GetWorkerRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != runner.StatusSuccess {
		t.Fatalf("status = %q, want success", run.Status)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("expected workspace dir %q to be created, stat err=%v", root, err)
	}
	if adapter.lastWorkspacePath != root {
		t.Fatalf("adapter WorkspacePath = %q, want %q", adapter.lastWorkspacePath, root)
	}
}

func TestRun_SkillBodyLoaded(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.SkillName = "fact-check"
	w.SkillVersion = "latest"
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Skills:     &fakeSkills{bodies: map[string]string{"fact-check": "## system instructions"}},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if adapter.lastSystem != "## system instructions" {
		t.Fatalf("system = %q", adapter.lastSystem)
	}
}

// captureAdapter records the last system/messages so prompt-build tests can
// assert on what the model would have seen.
type captureAdapter struct {
	response   models.SendResponse
	lastSystem string
	lastMsgs   []models.Message
}

func (c *captureAdapter) Send(_ context.Context, req models.SendRequest) (*models.SendResponse, error) {
	c.lastSystem = req.System
	c.lastMsgs = req.Messages
	resp := c.response
	return &resp, nil
}

// fakeAuditor records every audit emission so tests can assert on the
// exact sequence the runner produced.
type fakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
}

func (f *fakeAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeAuditor) toolNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.records))
	for _, r := range f.records {
		out = append(out, r.ToolName)
	}
	return out
}

// TestRun_EmitsAuditRecords verifies that the runner emits the expected
// audit-record sequence over a successful tool-using run. This is the
// "every tool call, prompt, and approval is recorded" SECURITY claim
// in test form.
func TestRun_EmitsAuditRecords(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ToolAllowlistJSON = `["search"]`
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			ToolCalls:    []models.ToolCall{{ID: "t1", Name: "search", Input: map[string]any{"q": "x"}}},
			StopReason:   models.StopToolUse,
			InputTokens:  100,
			OutputTokens: 25,
		},
		{
			Text:         "Found 3 results.",
			StopReason:   models.StopEndTurn,
			InputTokens:  120,
			OutputTokens: 40,
		},
	}}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"search": {OutputJSON: `{"hits":3}`},
	}}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	names := auditor.toolNames()
	// Expected at minimum: started, two model.send, one tool.dispatch,
	// run.finished. Order matters.
	wantContains := []string{
		"worker_run.started",
		"worker_model.send",
		"worker_tool.dispatch",
		"worker_model.send",
		"worker_run.finished",
	}
	if !sequenceMatches(names, wantContains) {
		t.Fatalf("audit sequence mismatch:\n got=%v\nwant prefix of=%v", names, wantContains)
	}
}

// sequenceMatches verifies that `want` appears as a subsequence inside
// `got` (in order, not necessarily contiguous).
func sequenceMatches(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestRun_PropagatesCorrelationID verifies the run.ID is stamped as
// correlation_id on every audit record the runner emits — the central
// "join slog + audit on one key per logical operation" contract.
func TestRun_PropagatesCorrelationID(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			Text:         "done",
			StopReason:   models.StopEndTurn,
			InputTokens:  10,
			OutputTokens: 5,
		},
	}}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})

	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if runID == "" {
		t.Fatal("empty runID")
	}

	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	if len(auditor.records) == 0 {
		t.Fatal("no audit records captured")
	}
	for _, rec := range auditor.records {
		if rec.CorrelationID != runID {
			t.Fatalf("rec %s correlation_id = %q, want %q",
				rec.ToolName, rec.CorrelationID, runID)
		}
	}
}

// TestRun_CorrelationOverridesAmbient checks that a pre-seeded
// correlation_id on the inbound ctx is replaced by run.ID for the
// runner's own emissions. The scheduler seeds a tick-level id; the
// runner is the more specific correlation and the override is the
// documented contract.
func TestRun_CorrelationOverridesAmbient(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{Text: "done", StopReason: models.StopEndTurn},
	}}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return adapter, nil
		},
	})

	ctx := audit.WithCorrelation(context.Background(), "sched:job-1:42")
	runID, err := r.RunWithOpts(ctx, w.ID, runner.RunOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	for _, rec := range auditor.records {
		if rec.CorrelationID != runID {
			t.Fatalf("rec %s correlation_id = %q, want runID %q (ambient must be overridden)",
				rec.ToolName, rec.CorrelationID, runID)
		}
	}
}
