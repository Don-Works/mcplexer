package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/hammerspoon"
	"github.com/don-works/mcplexer/internal/store"
)

// --- fakes ---------------------------------------------------------------

// hsFakeSecrets is an in-memory hammerspoonSecretStore. The handler tests
// don't care about encryption — they only verify the key is set / readable
// under the right scope id.
type hsFakeSecrets struct {
	mu     sync.Mutex
	data   map[string]map[string][]byte
	putErr error
	getErr error
}

func newFakeSecrets() *hsFakeSecrets {
	return &hsFakeSecrets{data: map[string]map[string][]byte{}}
}

func (f *hsFakeSecrets) Put(_ context.Context, scope, key string, val []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	if _, ok := f.data[scope]; !ok {
		f.data[scope] = map[string][]byte{}
	}
	f.data[scope][key] = append([]byte(nil), val...)
	return nil
}

func (f *hsFakeSecrets) Get(_ context.Context, scope, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	m, ok := f.data[scope]
	if !ok {
		return nil, store.ErrNotFound
	}
	v, ok := m[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (f *hsFakeSecrets) snapshot(scope string) map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range f.data[scope] {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// hsFakeStore captures UpdateCapabilitiesCache calls so the probe test can
// assert what was persisted.
type hsFakeStore struct {
	mu     sync.Mutex
	last   json.RawMessage
	lastID string
	updErr error
}

func (s *hsFakeStore) UpdateCapabilitiesCache(_ context.Context, id string, cache json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updErr != nil {
		return s.updErr
	}
	s.lastID = id
	s.last = append(json.RawMessage(nil), cache...)
	return nil
}

//nolint:unused // retained for focused assertions when cache persistence changes.
func (s *hsFakeStore) lastCache() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(json.RawMessage(nil), s.last...)
}

// hsFakeBridge implements hammerspoon.Bridge for handler-level tests. Lets
// the test pre-program responses keyed by a Lua substring match — the
// probe makes two calls (auth + smoke) with distinct snippets so a
// single-shot envelope wouldn't cover both.
type hsFakeBridge struct {
	calls       []hsFakeBridgeCall
	responses   []hsFakeBridgeResponse
	defaultResp hsFakeBridgeResponse
}

type hsFakeBridgeCall struct {
	lua     string
	timeout time.Duration
}

type hsFakeBridgeResponse struct {
	match string // substring of lua; empty matches anything
	env   hammerspoon.Envelope
	err   error
}

func (b *hsFakeBridge) Exec(_ context.Context, lua string, timeout time.Duration) (hammerspoon.Envelope, error) {
	b.calls = append(b.calls, hsFakeBridgeCall{lua: lua, timeout: timeout})
	for _, r := range b.responses {
		if r.match == "" || strings.Contains(lua, r.match) {
			return r.env, r.err
		}
	}
	return b.defaultResp.env, b.defaultResp.err
}

// hsFakeAuditor captures Record calls — used to verify install/probe never
// leak the generated password.
type hsFakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
}

func (a *hsFakeAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := *rec
	a.records = append(a.records, &cp)
	return nil
}

func (a *hsFakeAuditor) snapshot() []*store.AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*store.AuditRecord, len(a.records))
	copy(out, a.records)
	return out
}

// --- install -------------------------------------------------------------

func TestInstall_GOOSLinux_RefusedWith400(t *testing.T) {
	h := &hammerspoonHandler{
		secrets: newFakeSecrets(),
		goos:    "linux",
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	var body installErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Error, "macOS-only") {
		t.Errorf("Error = %q, want substring 'macOS-only'", body.Error)
	}
	if body.Step != "platform" {
		t.Errorf("Step = %q, want 'platform'", body.Step)
	}
}

func TestInstall_HappyPath_FreshInstall(t *testing.T) {
	tmp := t.TempDir()
	fakeSecrets := newFakeSecrets()
	aud := &hsFakeAuditor{}
	h := &hammerspoonHandler{
		secrets:        fakeSecrets,
		auditor:        aud,
		homeDir:        tmp,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		goos:           "darwin",
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 30, 0, 0, time.UTC) },
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp installResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("OK = false, want true")
	}
	if !resp.InitLuaModified {
		t.Errorf("InitLuaModified = false, want true (no prior init.lua)")
	}
	if resp.InitLuaBackup != "" {
		t.Errorf("InitLuaBackup = %q, want empty (fresh install)", resp.InitLuaBackup)
	}
	if len(resp.FilesWritten) != 2 {
		t.Errorf("FilesWritten = %v, want 2 entries", resp.FilesWritten)
	}

	// Snippet and password files exist with the expected modes.
	snippet := filepath.Join(tmp, ".hammerspoon", "hammerspoon-mcp.lua")
	pw := filepath.Join(tmp, ".hammerspoon", ".mcp-password")
	initLua := filepath.Join(tmp, ".hammerspoon", "init.lua")

	if data, err := os.ReadFile(snippet); err != nil || !bytes.Contains(data, []byte("hs.httpserver")) {
		t.Errorf("snippet not written or wrong contents: err=%v", err)
	}
	st, err := os.Stat(pw)
	if err != nil {
		t.Fatalf("stat pw: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("pw mode = %o, want 0600", st.Mode().Perm())
	}
	pwBytes, err := os.ReadFile(pw)
	if err != nil {
		t.Fatalf("read pw: %v", err)
	}
	plaintext := strings.TrimSpace(string(pwBytes))
	if len(plaintext) != 64 {
		t.Errorf("password length = %d, want 64 hex chars", len(plaintext))
	}

	// Same password mirrored into the secrets store.
	stored, ok := fakeSecrets.snapshot("hammerspoon-bridge")["HAMMERSPOON_BRIDGE_PASSWORD"]
	if !ok {
		t.Fatalf("password not stored in secrets")
	}
	if string(stored) != plaintext {
		t.Errorf("secrets password mismatch with file")
	}

	// init.lua created with exactly the require line.
	initContents, err := os.ReadFile(initLua)
	if err != nil {
		t.Fatalf("read init.lua: %v", err)
	}
	if !strings.Contains(string(initContents), `require("hammerspoon-mcp")`) {
		t.Errorf("init.lua missing require: %q", string(initContents))
	}

	// Audit row emitted, with no password leak.
	recs := aud.snapshot()
	if len(recs) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(recs))
	}
	if recs[0].ToolName != "hammerspoon.bridge.installed" {
		t.Errorf("audit ToolName = %q, want hammerspoon.bridge.installed", recs[0].ToolName)
	}
	rec, _ := json.Marshal(recs[0])
	if bytes.Contains(rec, []byte(plaintext)) {
		t.Errorf("audit record leaked the bridge password")
	}
}

func TestInstall_Idempotent_RequireAlreadyPresent(t *testing.T) {
	tmp := t.TempDir()
	hsDir := filepath.Join(tmp, ".hammerspoon")
	if err := os.MkdirAll(hsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	priorInit := "-- user config\nhs.alert.show('hi')\nrequire(\"hammerspoon-mcp\")\n"
	if err := os.WriteFile(filepath.Join(hsDir, "init.lua"), []byte(priorInit), 0o644); err != nil {
		t.Fatalf("seed init.lua: %v", err)
	}

	h := &hammerspoonHandler{
		secrets:        newFakeSecrets(),
		homeDir:        tmp,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		goos:           "darwin",
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 30, 0, 0, time.UTC) },
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp installResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.InitLuaModified {
		t.Errorf("InitLuaModified = true, want false (require line already present)")
	}
	if resp.InitLuaBackup != "" {
		t.Errorf("InitLuaBackup = %q, want empty (no modification = no backup)", resp.InitLuaBackup)
	}

	// init.lua untouched, bit-for-bit.
	got, err := os.ReadFile(filepath.Join(hsDir, "init.lua"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != priorInit {
		t.Errorf("init.lua was modified despite idempotency")
	}
}

func TestInstall_AppendsToExistingInitWithBackup(t *testing.T) {
	tmp := t.TempDir()
	hsDir := filepath.Join(tmp, ".hammerspoon")
	if err := os.MkdirAll(hsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	priorInit := "-- user config\nhs.alert.show('hi')\n"
	if err := os.WriteFile(filepath.Join(hsDir, "init.lua"), []byte(priorInit), 0o644); err != nil {
		t.Fatalf("seed init.lua: %v", err)
	}

	h := &hammerspoonHandler{
		secrets:        newFakeSecrets(),
		homeDir:        tmp,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		goos:           "darwin",
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 30, 0, 0, time.UTC) },
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp installResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.InitLuaModified {
		t.Errorf("InitLuaModified = false, want true")
	}
	if resp.InitLuaBackup == "" {
		t.Errorf("InitLuaBackup is empty, want timestamped backup name")
	}
	if !strings.HasPrefix(resp.InitLuaBackup, "init.lua.mcplexer-bak.") {
		t.Errorf("InitLuaBackup = %q, want prefix init.lua.mcplexer-bak.", resp.InitLuaBackup)
	}

	// Backup file contains prior contents bit-for-bit.
	backupPath := filepath.Join(hsDir, resp.InitLuaBackup)
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != priorInit {
		t.Errorf("backup contents != prior init.lua")
	}

	// New init.lua has prior contents + require line.
	newInit, err := os.ReadFile(filepath.Join(hsDir, "init.lua"))
	if err != nil {
		t.Fatalf("read init.lua: %v", err)
	}
	if !strings.HasPrefix(string(newInit), priorInit) {
		t.Errorf("init.lua doesn't start with prior contents")
	}
	if !strings.Contains(string(newInit), `require("hammerspoon-mcp")`) {
		t.Errorf("init.lua missing require line")
	}
}

func TestInstall_PasswordStoreFailureIsSurfaced(t *testing.T) {
	tmp := t.TempDir()
	fakeSecrets := newFakeSecrets()
	fakeSecrets.putErr = errors.New("disk full")

	h := &hammerspoonHandler{
		secrets:        fakeSecrets,
		homeDir:        tmp,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		goos:           "darwin",
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 30, 0, 0, time.UTC) },
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp installErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Step != "gen_password" {
		t.Errorf("Step = %q, want gen_password", resp.Step)
	}

	// No password file on disk — the encrypted store failed first so we
	// never reached the disk-write step.
	if _, err := os.Stat(filepath.Join(tmp, ".hammerspoon", ".mcp-password")); !os.IsNotExist(err) {
		t.Errorf("password file unexpectedly created: %v", err)
	}
}

func TestInstall_NoSecretsManager_503(t *testing.T) {
	h := &hammerspoonHandler{
		secrets: nil,
		goos:    "darwin",
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/install", nil)
	w := httptest.NewRecorder()
	h.install(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
}

// --- snippet -------------------------------------------------------------

func TestSnippet_ReturnsEmbeddedLua(t *testing.T) {
	h := &hammerspoonHandler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/hammerspoon/snippet", nil)
	w := httptest.NewRecorder()
	h.snippet(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-lua") {
		t.Errorf("Content-Type = %q, want text/x-lua…", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "hammerspoon-mcp.lua") {
		t.Errorf("Content-Disposition = %q, want filename=hammerspoon-mcp.lua", cd)
	}
	if !strings.Contains(w.Body.String(), "hs.httpserver") {
		t.Errorf("body missing expected substring 'hs.httpserver'")
	}
}

// --- probe ---------------------------------------------------------------

func TestProbe_BridgeUnreachable_DegradesGracefully(t *testing.T) {
	st := &hsFakeStore{}
	fakeSecrets := newFakeSecrets()
	// no password = bridge isn't really configured; nullBridge will be
	// used. We deliberately don't wire a HammerspoonManager so the auth
	// check reports "bridge not configured".
	aud := &hsFakeAuditor{}
	h := &hammerspoonHandler{
		manager:        nil,
		store:          st,
		secrets:        fakeSecrets,
		auditor:        aud,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 42, 0, 0, time.UTC) },
	}

	// Point bridge URL at a free port so TCP connect fails.
	port := pickFreePort(t)
	_ = fakeSecrets.Put(context.Background(), "hammerspoon-bridge", "HAMMERSPOON_BRIDGE_URL",
		[]byte("http://127.0.0.1:"+port))

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/probe", nil)
	w := httptest.NewRecorder()
	h.probe(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp probeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Health != "broken" {
		t.Errorf("Health = %q, want broken (bridge unreachable + no auth)", resp.Health)
	}
	if resp.Checks["bridge_reachable"].OK {
		t.Errorf("bridge_reachable.OK = true, want false")
	}
	if resp.Checks["auth_ok"].OK {
		t.Errorf("auth_ok.OK = true, want false")
	}
	if len(resp.Remediation) == 0 {
		t.Errorf("Remediation empty, want at least one entry")
	}

	// Cache persisted to the fake store.
	if st.lastID != "hammerspoon" {
		t.Errorf("store.lastID = %q, want hammerspoon", st.lastID)
	}
	if len(st.last) == 0 {
		t.Errorf("cache not persisted")
	}

	// Audit row.
	if len(aud.snapshot()) == 0 {
		t.Errorf("no audit row recorded")
	}
}

func TestProbe_AllOK_HealthOK(t *testing.T) {
	st := &hsFakeStore{}
	fakeSecrets := newFakeSecrets()

	// Stand up a TCP listener that just accepts + drops, so bridge_reachable passes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_ = fakeSecrets.Put(context.Background(), "hammerspoon-bridge", "HAMMERSPOON_BRIDGE_URL",
		[]byte("http://"+ln.Addr().String()))

	// Wire a HammerspoonManager backed by a fake bridge that returns:
	//  - accessibility = true for "accessibilityState" calls
	//  - {"windows":[{}]} for list_windows calls
	bridge := &hsFakeBridge{
		responses: []hsFakeBridgeResponse{
			{match: "accessibilityState", env: hammerspoon.Envelope{Ok: true, Result: json.RawMessage(`true`)}},
			{match: "hs.window.allWindows", env: hammerspoon.Envelope{Ok: true, Result: json.RawMessage(`{"windows":[{"app":"Finder"}]}`)}},
		},
	}
	mgr := hammerspoon.NewManager(bridge, false)

	// Stub `pgrep` via PATH so app_running passes even on the CI host that
	// (probably) isn't running Hammerspoon.
	stubBinDir := writeStubPgrep(t, "Hammerspoon")
	t.Setenv("PATH", stubBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	aud := &hsFakeAuditor{}
	h := &hammerspoonHandler{
		manager:        mgr,
		store:          st,
		secrets:        fakeSecrets,
		auditor:        aud,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 42, 0, 0, time.UTC) },
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/probe", nil)
	w := httptest.NewRecorder()
	h.probe(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var resp probeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Health != "ok" {
		t.Errorf("Health = %q, want ok (got checks=%+v)", resp.Health, resp.Checks)
	}
	if !resp.Checks["app_running"].OK {
		t.Errorf("app_running.OK = false (stub pgrep didn't match)")
	}
	if !resp.Checks["bridge_reachable"].OK {
		t.Errorf("bridge_reachable.OK = false")
	}
	if !resp.Checks["auth_ok"].OK {
		t.Errorf("auth_ok.OK = false: detail=%q", resp.Checks["auth_ok"].Detail)
	}
	if !resp.Checks["accessibility"].OK {
		t.Errorf("accessibility.OK = false")
	}
	if !resp.Checks["smoke"].OK {
		t.Errorf("smoke.OK = false: detail=%q", resp.Checks["smoke"].Detail)
	}
	if len(resp.Remediation) != 0 {
		t.Errorf("Remediation = %v, want empty on all-ok", resp.Remediation)
	}
}

func TestProbe_AccessibilityDenied_Degraded(t *testing.T) {
	st := &hsFakeStore{}
	fakeSecrets := newFakeSecrets()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_ = fakeSecrets.Put(context.Background(), "hammerspoon-bridge", "HAMMERSPOON_BRIDGE_URL",
		[]byte("http://"+ln.Addr().String()))

	bridge := &hsFakeBridge{
		responses: []hsFakeBridgeResponse{
			{match: "accessibilityState", env: hammerspoon.Envelope{Ok: true, Result: json.RawMessage(`false`)}},
			{match: "hs.window.allWindows", env: hammerspoon.Envelope{Ok: true, Result: json.RawMessage(`{"windows":[]}`)}},
		},
	}
	mgr := hammerspoon.NewManager(bridge, false)

	stubBinDir := writeStubPgrep(t, "Hammerspoon")
	t.Setenv("PATH", stubBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := &hammerspoonHandler{
		manager:        mgr,
		store:          st,
		secrets:        fakeSecrets,
		hsScopeID:      "hammerspoon-bridge",
		reloadDisabled: true,
		nowFn:          func() time.Time { return time.Date(2026, 5, 25, 17, 42, 0, 0, time.UTC) },
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/hammerspoon/probe", nil)
	w := httptest.NewRecorder()
	h.probe(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var resp probeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Health != "degraded" {
		t.Errorf("Health = %q, want degraded (auth ok but AX denied)", resp.Health)
	}
	if resp.Checks["accessibility"].OK {
		t.Errorf("accessibility.OK = true, want false")
	}
	if !resp.Checks["auth_ok"].OK {
		t.Errorf("auth_ok.OK = false")
	}
	foundAX := false
	for _, r := range resp.Remediation {
		if r.Check == "accessibility" {
			foundAX = true
			break
		}
	}
	if !foundAX {
		t.Errorf("Remediation missing accessibility entry: %+v", resp.Remediation)
	}
}

// --- ensureRequireLine unit tests ---------------------------------------

func TestEnsureRequireLine_NoFile_CreatesIt(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "init.lua")
	modified, backup, err := ensureRequireLine(path, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !modified {
		t.Errorf("modified = false, want true")
	}
	if backup != "" {
		t.Errorf("backup = %q, want empty (no prior file)", backup)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `require("hammerspoon-mcp")`) {
		t.Errorf("init.lua missing require line: %q", string(got))
	}
}

func TestEnsureRequireLine_AlreadyPresentVariants(t *testing.T) {
	cases := []string{
		`require("hammerspoon-mcp")`,
		`require('hammerspoon-mcp')`,
		`  require("hammerspoon-mcp")  `,
		`require("hammerspoon-mcp") -- bridge`,
		`require("hammerspoon-mcp");`,
	}
	for _, body := range cases {
		t.Run(strings.ReplaceAll(body, " ", "_"), func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "init.lua")
			seed := "-- header\n" + body + "\n-- footer\n"
			if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			modified, _, err := ensureRequireLine(path, time.Now())
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if modified {
				t.Errorf("modified = true for %q, want false", body)
			}
		})
	}
}

// --- helpers -------------------------------------------------------------

// pickFreePort grabs an ephemeral port that's free at call time. The TCP
// connect probe will fail against it because nothing's listening.
func pickFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	return itoa(addr.Port)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = digits[i%10]
		i /= 10
	}
	return string(buf[n:])
}

// writeStubPgrep writes a shell stub named `pgrep` that always succeeds and
// prints a fake pid when called with `-x <wantName>`. Returns the directory
// it was placed in so the caller can prepend it to PATH.
func writeStubPgrep(t *testing.T, wantName string) string {
	t.Helper()
	dir := t.TempDir()
	stub := `#!/bin/sh
# Test stub for pgrep -x <name>. Matches a single hardcoded target and
# prints a fake pid; everything else exits 1.
if [ "$1" = "-x" ] && [ "$2" = "` + wantName + `" ]; then
    echo 12345
    exit 0
fi
exit 1
`
	path := filepath.Join(dir, "pgrep")
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return dir
}
