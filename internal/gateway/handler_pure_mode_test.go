package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

// TestPureMode_DefaultOff locks the no-go rule: the new PureMode field
// must default to false so a fresh install / existing row is unchanged.
// Anything that flips default behaviour on by accident breaks this test.
func TestPureMode_DefaultOff(t *testing.T) {
	s := config.DefaultSettings()
	if s.PureMode {
		t.Fatalf("PureMode default = true, want false (this is a baseline-behaviour break)")
	}
}

// TestPureMode_EnvVarTruthyEnables locks the env-var path: with no
// SettingsService wired (so the DB row cannot be reached), setting
// MCPLEXER_PURE_MODE=1 makes the handler drop the tool surface.
func TestPureMode_EnvVarTruthyEnables(t *testing.T) {
	t.Setenv("MCPLEXER_PURE_MODE", "1")

	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	// Intentionally do NOT set h.settingsSvc — exercise the env-only path.
	h.settingsSvc = nil

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	names := toolNames(result)
	if len(names) != 0 {
		t.Fatalf("Pure Mode via env: expected empty tools/list, got %v", names)
	}
}

// TestPureMode_EnvVarUnsetKeepsSurface locks the inverse: with no
// SettingsService and no env var, the surface is intact. Catches a
// future regression where the env helper accidentally defaults to true.
func TestPureMode_EnvVarUnsetKeepsSurface(t *testing.T) {
	t.Setenv("MCPLEXER_PURE_MODE", "")

	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.settingsSvc = nil

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	names := toolNames(result)
	if len(names) == 0 {
		t.Fatalf("Pure Mode off (no env): expected non-empty tools/list, got empty")
	}
	// Slim-surface keepers must be present.
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mcpx__execute_code"] {
		t.Errorf("missing mcpx__execute_code in baseline, got %v", names)
	}
	if !found["mcpx__search_tools"] {
		t.Errorf("missing mcpx__search_tools in baseline, got %v", names)
	}
}

// TestPureMode_ToolsListEmpty confirms that with PureMode=true in DB,
// handleToolsList returns an empty payload regardless of which built-ins
// would otherwise be advertised. The test handler wires up no extra
// services, but the empty surface is enforced at the gate, so the result
// is independent of buildAllBuiltinTools.
func TestPureMode_ToolsListEmpty(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	settings := config.DefaultSettings()
	settings.PureMode = true
	if err := h.settingsSvc.Save(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	names := toolNames(result)
	if len(names) != 0 {
		t.Fatalf("expected empty tools/list under PureMode=true, got %v", names)
	}

	// Sanity: the raw payload is the canonical `{"tools":[]}` shape, not
	// e.g. an empty object. Modern harnesses (Claude Code, Codex,
	// OpenCode) treat both the same, but pinning the shape here means
	// any future refactor that drops the `tools` key trips a test.
	var parsed struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("parse tools/list payload: %v", err)
	}
	if string(parsed.Tools) != "[]" {
		t.Fatalf("expected tools array to be [], got %s", string(parsed.Tools))
	}
}

// TestPureMode_ToolsCallDenied confirms the dispatch gate: a tools/call
// for a built-in (mcpx__execute_code) returns an RPC error with the
// "Pure Mode" message and the standard invalid-request code. Catches a
// regression where the gate is moved below the call-name normalisation
// and a hand-crafted JSON-RPC envelope could bypass it.
func TestPureMode_ToolsCallDenied(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	settings := config.DefaultSettings()
	settings.PureMode = true
	if err := h.settingsSvc.Save(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code":"print(1)"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr == nil {
		t.Fatalf("expected RPC error under PureMode=true, got result=%s", string(result))
	}
	if rpcErr.Code != CodeInvalidRequest {
		t.Errorf("RPC error code = %d, want %d (CodeInvalidRequest)", rpcErr.Code, CodeInvalidRequest)
	}
	if rpcErr.Message == "" {
		t.Errorf("RPC error message is empty; users need the recovery hint")
	}
}

// TestPureMode_ToolsCallBuiltinAlsoDenied proves the gate sits above
// the isBuiltinTool check — secret__prompt and friends get the same
// refusal. This is the load-bearing test that distinguishes Pure Mode
// from the existing "direct external calls are disabled" gate (which
// allows built-ins through).
func TestPureMode_ToolsCallBuiltinAlsoDenied(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	settings := config.DefaultSettings()
	settings.PureMode = true
	if err := h.settingsSvc.Save(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	for _, name := range []string{"secret__prompt", "secret__list_refs", "mcpx__search_tools"} {
		t.Run(name, func(t *testing.T) {
			params, _ := json.Marshal(CallToolRequest{
				Name:      name,
				Arguments: json.RawMessage(`{}`),
			})
			result, rpcErr := h.handleToolsCall(context.Background(), params)
			if rpcErr == nil {
				t.Fatalf("expected RPC error under PureMode=true for %s, got result=%s",
					name, string(result))
			}
			if rpcErr.Code != CodeInvalidRequest {
				t.Errorf("RPC error code for %s = %d, want %d",
					name, rpcErr.Code, CodeInvalidRequest)
			}
		})
	}
}

// TestPureMode_EnvBeatsDB locks the recovery path: an operator who
// accidentally flipped PureMode=true in the DB can recover with
// MCPLEXER_PURE_MODE=0 in the launch plist / systemd unit, without
// editing the DB. Env override is the last line of defence against
// "I'm locked out of my own gateway".
func TestPureMode_EnvBeatsDB(t *testing.T) {
	t.Setenv("MCPLEXER_PURE_MODE", "0")

	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	settings := config.DefaultSettings()
	settings.PureMode = true // DB says ON
	if err := h.settingsSvc.Save(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	// Env=0 should win → tools/list returns the full surface.
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	names := toolNames(result)
	if len(names) == 0 {
		t.Fatalf("env=0 must beat DB=true; expected non-empty tools/list, got empty")
	}
}

// Sanity: store package import is used by the SettingsService above;
// keep the import non-nil so go vet / staticcheck don't complain.
var _ = store.ErrNotFound
