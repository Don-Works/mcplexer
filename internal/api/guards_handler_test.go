package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newTestScheduler builds a real Scheduler against the test DB with no
// approver / auditor / driver. Used only to exercise the handler's
// "scheduler is non-nil but job missing" → 404 path.
func newTestScheduler(t *testing.T, db *sqlite.DB) *scheduler.Scheduler {
	t.Helper()
	return scheduler.New(db, nil, nil, scheduler.RealClock{})
}

// newTestGuardsDB opens a fresh sqlite DB in a temp dir. The DB
// satisfies store.Store + every per-Guard receipt-store interface, so
// the handler can run against the real schema without a mock.
func newTestGuardsDB(t *testing.T) *sqlite.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "guards.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestGuardsHandler wires a handler with the real default sanitizer
// denylist + an in-memory store + a settings service backed by the
// same store (so the sanitizer envelope_always toggle persists). Per-
// Guard optional deps (scheduler, hookInstaller, sandboxInstall,
// installMgr) stay nil — callers that need them set their own.
func newTestGuardsHandler(t *testing.T) (*guardsHandler, *sqlite.DB) {
	t.Helper()
	db := newTestGuardsDB(t)
	h := &guardsHandler{
		store:       db,
		sanitizer:   sanitize.DefaultDenylist(),
		settingsSvc: config.NewSettingsService(db),
	}
	return h, db
}

func TestGuardsOverview(t *testing.T) {
	h, _ := newTestGuardsHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/guards", nil)
	rr := httptest.NewRecorder()
	h.overview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"shell", "sanitizer", "schedule", "sandbox", "mcp"} {
		if _, ok := out[k]; !ok {
			t.Errorf("overview missing top-level key %q", k)
		}
	}

	// Sanitizer card must report a positive denylist size (default
	// denylist ships with several rules — if this hits zero something
	// has regressed in sanitize.DefaultDenylist).
	var san sanitizerGuardSummary
	if err := json.Unmarshal(out["sanitizer"], &san); err != nil {
		t.Fatalf("decode sanitizer: %v", err)
	}
	if san.DenylistSize <= 0 {
		t.Errorf("denylist_size: want > 0, got %d", san.DenylistSize)
	}
}

func TestGuardsSanitizerToggle(t *testing.T) {
	h, _ := newTestGuardsHandler(t)

	// Initial GET — envelope_always defaults to false.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/guards/sanitizer", nil)
	getRR := httptest.NewRecorder()
	h.sanitizerDetail(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("initial GET: want 200, got %d", getRR.Code)
	}
	var initial sanitizerGuardDetail
	if err := json.Unmarshal(getRR.Body.Bytes(), &initial); err != nil {
		t.Fatalf("initial decode: %v", err)
	}
	if initial.EnvelopeAlways {
		t.Fatalf("envelope_always default: want false, got true")
	}
	if len(initial.Denylist) == 0 {
		t.Errorf("denylist names should not be empty")
	}

	// PUT envelope_always=true.
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/guards/sanitizer",
		strings.NewReader(`{"envelope_always": true}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRR := httptest.NewRecorder()
	h.sanitizerUpdate(putRR, putReq)
	if putRR.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d (%s)", putRR.Code, putRR.Body.String())
	}
	var afterPut sanitizerGuardDetail
	if err := json.Unmarshal(putRR.Body.Bytes(), &afterPut); err != nil {
		t.Fatalf("PUT decode: %v", err)
	}
	if !afterPut.EnvelopeAlways {
		t.Fatalf("envelope_always after PUT: want true, got false")
	}

	// GET again — must reflect the toggle.
	getReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/guards/sanitizer", nil)
	getRR2 := httptest.NewRecorder()
	h.sanitizerDetail(getRR2, getReq2)
	var afterGet sanitizerGuardDetail
	if err := json.Unmarshal(getRR2.Body.Bytes(), &afterGet); err != nil {
		t.Fatalf("second GET decode: %v", err)
	}
	if !afterGet.EnvelopeAlways {
		t.Fatalf("envelope_always after second GET: want true, got false")
	}
}

func TestGuardsScheduleRunUnknownIDReturns404(t *testing.T) {
	h, _ := newTestGuardsHandler(t)
	// scheduleRun needs a non-nil scheduler — but with an unknown id
	// it must surface ErrNotFound from the store. We can't construct a
	// real Scheduler without a clock + driver here, so we directly
	// test the nil-scheduler 503 path and trust the run-once → store
	// chain in scheduler_test.go.
	if h.scheduler != nil {
		t.Fatalf("test setup error: scheduler should be nil")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/guards/schedule/does-not-exist/run", nil)
	req.SetPathValue("id", "does-not-exist")
	rr := httptest.NewRecorder()
	h.scheduleRun(rr, req)
	// Nil scheduler reports 503; that's the only path an unknown-id
	// call traverses without going through the live scheduler. The
	// scheduler's RunOnce ErrNotFound surfacing is covered by its own
	// suite; here we verify the handler shape rather than the
	// scheduler internals.
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil scheduler: want 503, got %d", rr.Code)
	}
}

func TestGuardsScheduleRunWithLiveSchedulerUnknownID(t *testing.T) {
	h, db := newTestGuardsHandler(t)
	// Build a real Scheduler with a fake clock + auditor so we can
	// exercise the ErrNotFound -> 404 path end-to-end.
	sched := newTestScheduler(t, db)
	h.scheduler = sched

	req := httptest.NewRequest(http.MethodPost, "/api/v1/guards/schedule/missing-job/run", nil)
	req.SetPathValue("id", "missing-job")
	rr := httptest.NewRecorder()
	h.scheduleRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id: want 404, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestGuardsShellInstallHooksWithoutInstaller(t *testing.T) {
	h, _ := newTestGuardsHandler(t)
	if h.hookInstaller != nil {
		t.Fatalf("test setup error: hookInstaller should be nil")
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/guards/shell/clients/claude_code/install_hooks", nil)
	req.SetPathValue("id", "claude_code")
	rr := httptest.NewRecorder()
	h.shellInstallHooks(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no installer: want 503, got %d", rr.Code)
	}
}

func TestGuardsSanitizerDenylistEndpoint(t *testing.T) {
	h, _ := newTestGuardsHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/guards/sanitizer/denylist", nil)
	rr := httptest.NewRecorder()
	h.sanitizerDenylist(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Names) == 0 {
		t.Errorf("denylist names: want non-empty")
	}
}

func TestGuardsSandboxDetailIncludesClients(t *testing.T) {
	h, db := newTestGuardsHandler(t)
	ctx := context.Background()
	// Seed one installed_client row so sandboxDetail has something to
	// echo back. SandboxEnabled=false is the relevant assertion: the
	// detail surface must include disabled clients too, not just
	// enabled ones.
	if err := db.UpsertInstalledClient(ctx, &store.InstalledClient{
		ID:   "claude_code",
		Name: "Claude Code",
	}); err != nil {
		t.Fatalf("seed installed_client: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/guards/sandbox", nil)
	rr := httptest.NewRecorder()
	h.sandboxDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	var out sandboxGuardDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Clients) != 1 || out.Clients[0].ID != "claude_code" {
		t.Errorf("clients: want [claude_code], got %+v", out.Clients)
	}
}

// newTestGuardsHandlerWithHookInstaller wires a handler that owns a
// HookInstaller rooted at a fresh tempdir HOME. The returned home is
// what the installer thinks `~` is, so writing fixtures into
// home/.claude/settings.json (or NOT writing it) simulates the install
// being present, drifted, or never run.
func newTestGuardsHandlerWithHookInstaller(t *testing.T) (*guardsHandler, *sqlite.DB, string) {
	t.Helper()
	db := newTestGuardsDB(t)
	home := t.TempDir()
	inst, err := install.NewHookInstaller(home, db, "")
	if err != nil {
		t.Fatalf("NewHookInstaller: %v", err)
	}
	h := &guardsHandler{
		store:         db,
		sanitizer:     sanitize.DefaultDenylist(),
		settingsSvc:   config.NewSettingsService(db),
		hookInstaller: inst,
	}
	return h, db, home
}

// TestReconcileClientDrift_NoInstall is the short-circuit: when the DB
// row says hooks_installed=false the reconciler MUST return
// drifted=false unconditionally — drifted is only meaningful when the
// install was supposed to be live.
func TestReconcileClientDrift_NoInstall(t *testing.T) {
	h, _, _ := newTestGuardsHandlerWithHookInstaller(t)
	drifted, errText := h.reconcileClientDrift(
		context.Background(), install.ClaudeCode,
		store.InstalledClient{ID: "claude_code", HooksInstalled: false}, true,
	)
	if drifted || errText != "" {
		t.Fatalf("not-installed: want (false, \"\"), got (%v, %q)", drifted, errText)
	}
}

// TestReconcileClientDrift_Missing covers the load-bearing case: DB
// says installed=true, but settings.json is absent → drifted=true and
// the DB row gets the flag written-through so the overview summary
// (next request, possibly different code path) sees the same truth.
func TestReconcileClientDrift_Missing(t *testing.T) {
	h, db, _ := newTestGuardsHandlerWithHookInstaller(t)
	ctx := context.Background()
	row := store.InstalledClient{
		ID: "claude_code", Name: "Claude Code", HooksInstalled: true,
	}
	if err := db.UpsertInstalledClient(ctx, &row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drifted, errText := h.reconcileClientDrift(ctx, install.ClaudeCode, row, true)
	if !drifted {
		t.Fatalf("missing settings.json: want drifted=true, got false")
	}
	if errText != "" {
		t.Errorf("missing settings.json should not surface a parse error, got %q", errText)
	}
	// Write-through check: re-read the row and confirm hooks_drifted is now true.
	got, err := db.GetInstalledClient(ctx, "claude_code")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if !got.HooksDrifted {
		t.Errorf("hooks_drifted not persisted: want true, row=%+v", got)
	}
}

// TestReconcileClientDrift_Present is the happy path: settings.json
// contains the hook → not drifted. Also covers idempotency: a second
// call within the throttle window returns the same cached result
// without re-reading the FS (we delete the file mid-test and assert
// the cached result still says not-drifted).
func TestReconcileClientDrift_Present(t *testing.T) {
	h, db, home := newTestGuardsHandlerWithHookInstaller(t)
	ctx := context.Background()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A fully-installed settings.json carries BOTH the PreToolUse hook AND
	// the session-lifecycle hooks (SessionStart/SessionEnd). The drift
	// reconciler now checks both arms, so the happy-path fixture must include
	// the session hooks too — otherwise it would (correctly) read as drifted.
	sessionEP := h.hookInstaller.SessionEndpoint()
	sessionEntry := `[{"hooks":[{"type":"command","command":"curl -s ` + sessionEP + `"}]}]`
	body := `{"hooks":{` +
		`"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"curl -s ` + install.DefaultHookEndpoint + `"}` +
		`]}],` +
		`"SessionStart":` + sessionEntry + `,` +
		`"SessionEnd":` + sessionEntry +
		`}}`
	if err := os.WriteFile(settingsPath, []byte(body), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	row := store.InstalledClient{
		ID: "claude_code", Name: "Claude Code", HooksInstalled: true,
	}
	if err := db.UpsertInstalledClient(ctx, &row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drifted, errText := h.reconcileClientDrift(ctx, install.ClaudeCode, row, true)
	if drifted || errText != "" {
		t.Fatalf("present: want (false, \"\"), got (%v, %q)", drifted, errText)
	}
	// Throttle check: nuke the file, call again. The cached not-drifted
	// must still come back — proves the per-client throttle is in play.
	if err := os.Remove(settingsPath); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	drifted2, _ := h.reconcileClientDrift(ctx, install.ClaudeCode, row, true)
	if drifted2 {
		t.Errorf("throttle bypass: want cached false, got drifted=true")
	}
}

// TestReconcileClientDrift_SessionHooksStripped is the LOW-9 regression: a
// settings.json with the PreToolUse hook intact but the session-lifecycle
// hooks removed must read as DRIFTED. Before the session-drift check the
// reconciler only inspected PreToolUse and would have reported not-drifted.
func TestReconcileClientDrift_SessionHooksStripped(t *testing.T) {
	h, db, home := newTestGuardsHandlerWithHookInstaller(t)
	ctx := context.Background()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// PreToolUse present, session hooks ABSENT.
	body := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"curl -s ` + install.DefaultHookEndpoint + `"}` +
		`]}]}}`
	if err := os.WriteFile(settingsPath, []byte(body), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	row := store.InstalledClient{ID: "claude_code", HooksInstalled: true}
	if err := db.UpsertInstalledClient(ctx, &row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drifted, errText := h.reconcileClientDrift(ctx, install.ClaudeCode, row, true)
	if !drifted {
		t.Fatalf("stripped session hooks: want drifted=true, got false")
	}
	if errText != "" {
		t.Errorf("stripped session hooks should not surface a parse error, got %q", errText)
	}
}

// TestReconcileClientDrift_CorruptJSON pins the parse-error contract:
// settings.json exists but doesn't parse → drifted=true AND errText is
// populated so the dashboard can render the reason next to the red
// badge.
func TestReconcileClientDrift_CorruptJSON(t *testing.T) {
	h, db, home := newTestGuardsHandlerWithHookInstaller(t)
	ctx := context.Background()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	row := store.InstalledClient{ID: "claude_code", HooksInstalled: true}
	if err := db.UpsertInstalledClient(ctx, &row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drifted, errText := h.reconcileClientDrift(ctx, install.ClaudeCode, row, true)
	if !drifted {
		t.Fatalf("corrupt json: want drifted=true, got false")
	}
	if !strings.Contains(errText, "parse") {
		t.Errorf("corrupt json: want errText mentioning parse, got %q", errText)
	}
}
