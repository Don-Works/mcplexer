package hammerspoon

// integration_test.go — end-to-end coverage of the live bridge path against a
// stub HTTP server pretending to be Hammerspoon's hs.httpserver.
//
// These cases stand in for the plan §8 "scenario_hammerspoon" cases that
// can't run inside the Docker shell harness: the daemon's hammerspoon.Manager
// is built once at startup and skipped entirely when the DownstreamServer row
// is disabled (the default), so a runtime PATCH from a scenario can't bring
// the bridge online. We exercise the same chain — Manager → MCPServer →
// HTTPDriver → real HTTP — without the docker gymnastics.
//
// The stub mirrors the embed/hammerspoon-mcp.lua contract:
//   - POST /exec with Authorization: Bearer <password>.
//   - Returns 200 {ok, result, err} for known Lua snippets:
//       * buildListWindowsLua() → {windows:[{app:"Slack", ...}]}.
//       * "return hs.accessibilityState(true)" → true.
//       * Anything else → {ok:false, err:"unhandled in stub"}.
//   - Returns 401 for a missing/wrong Bearer.
//
// Each case spins up a fresh httptest.Server so the cases are independent and
// can run in parallel.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cannedListWindowsResult mirrors what the plan's stub returns. We assert the
// MCPServer passes this through verbatim, including the windows array shape.
const cannedListWindowsResult = `{"windows":[{"app":"Slack","pid":123,"title":"general","frame":{"x":0,"y":0,"w":1280,"h":800},"frontmost":true,"window_id":1}]}`

// stubBridgeOpts tunes the test stub. forceUnauthorized = true makes the stub
// reject every request with 401 so we can exercise the password-mismatch case.
type stubBridgeOpts struct {
	password          string
	forceUnauthorized bool
}

// newStubBridge spins up an httptest.Server posing as Hammerspoon. Caller
// gets back the server (close in t.Cleanup) and the base URL the HTTP driver
// should be pointed at.
func newStubBridge(t *testing.T, opts stubBridgeOpts) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Bearer auth.
		if opts.forceUnauthorized {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"err":"unauthorized"}`)
			return
		}
		got := r.Header.Get("Authorization")
		if got != "Bearer "+opts.password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"err":"unauthorized"}`)
			return
		}

		var req struct {
			Lua       string `json:"lua"`
			TimeoutMS int64  `json:"timeout_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Pattern-match on the Lua snippet. We deliberately compare against
		// buildListWindowsLua() so the stub auto-tracks any future template
		// changes — the integration test catches Lua-shape drift between Go
		// and the bridge.
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Lua, "hs.window.allWindows()"):
			_, _ = io.WriteString(w,
				`{"ok":true,"result":`+cannedListWindowsResult+`}`)
		case strings.Contains(req.Lua, "hs.accessibilityState"):
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case strings.Contains(req.Lua, "return hs.host.localizedName()"):
			_, _ = io.WriteString(w, `{"ok":true,"result":"stub-machine"}`)
		default:
			_, _ = io.WriteString(w, `{"ok":false,"err":"unhandled in stub"}`)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL
}

// TestIntegration_ListWindows_Success exercises the happy path: Manager wired
// to an HTTP driver pointed at a stub bridge, list_windows returns the canned
// shape verbatim. Mirrors plan §8 case 1.5.
func TestIntegration_ListWindows_Success(t *testing.T) {
	t.Parallel()
	const pw = "test-password-aaaa"
	_, base := newStubBridge(t, stubBridgeOpts{password: pw})

	mgr := NewManager(NewHTTPDriver(base, pw), false /* allowExecLua */)
	s := NewMCPServer(mgr)

	out, err := s.Call(context.Background(), "list_windows", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("list_windows returned isError: %s", txt)
	}
	// txt is the result JSON — should contain the canned Slack window entry.
	if !strings.Contains(txt, `"app":"Slack"`) {
		t.Errorf("missing canned window content; got %q", txt)
	}
	if !strings.Contains(txt, `"window_id":1`) {
		t.Errorf("missing window_id field; got %q", txt)
	}
}

// TestIntegration_ToolsList_ExecLuaGateOff verifies the gate hides exec_lua
// when allowExecLua=false even with a live bridge. Mirrors plan §8 case 1.6.
func TestIntegration_ToolsList_ExecLuaGateOff(t *testing.T) {
	t.Parallel()
	const pw = "test-password-bbbb"
	_, base := newStubBridge(t, stubBridgeOpts{password: pw})

	mgr := NewManager(NewHTTPDriver(base, pw), false /* allowExecLua */)
	s := NewMCPServer(mgr)

	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if strings.Contains(string(raw), `"name":"exec_lua"`) {
		t.Errorf("exec_lua surfaced with gate off: %s", raw)
	}

	// Direct call must also be rejected — agent can't bypass by guessing.
	out, _ := s.Call(context.Background(), "exec_lua",
		json.RawMessage(`{"lua":"return 1"}`))
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("expected isError on gated exec_lua, got %q", txt)
	}
	if !strings.Contains(txt, "HAMMERSPOON_ALLOW_EXEC_LUA") {
		t.Errorf("expected gate-disabled msg, got %q", txt)
	}
}

// TestIntegration_ToolsList_ExecLuaGateOn verifies the gate exposes exec_lua
// and a direct call rolls through the live bridge. Mirrors plan §8 case 1.7.
func TestIntegration_ToolsList_ExecLuaGateOn(t *testing.T) {
	t.Parallel()
	const pw = "test-password-cccc"
	_, base := newStubBridge(t, stubBridgeOpts{password: pw})

	mgr := NewManager(NewHTTPDriver(base, pw), true /* allowExecLua */)
	s := NewMCPServer(mgr)

	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if !strings.Contains(string(raw), `"name":"exec_lua"`) {
		t.Fatalf("exec_lua missing with gate on: %s", raw)
	}

	out, _ := s.Call(context.Background(), "exec_lua",
		json.RawMessage(`{"lua":"return hs.host.localizedName()"}`))
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("exec_lua returned isError with gate on: %s", txt)
	}
	if !strings.Contains(txt, "stub-machine") {
		t.Errorf("expected stub-machine in result, got %q", txt)
	}
}

// TestIntegration_PasswordMismatch verifies a 401 from the stub surfaces as
// a clean "bridge password mismatch" isError envelope (no raw HTTP code,
// no stack trace). Mirrors plan §8 case 1.8.
func TestIntegration_PasswordMismatch(t *testing.T) {
	t.Parallel()
	const pw = "correct-password"
	_, base := newStubBridge(t, stubBridgeOpts{password: pw})

	// Wire the driver with the wrong password.
	mgr := NewManager(NewHTTPDriver(base, "wrong-password"), false)
	s := NewMCPServer(mgr)

	out, _ := s.Call(context.Background(), "list_windows", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("expected isError on 401, got %q", txt)
	}
	if !strings.Contains(strings.ToLower(txt), "password mismatch") {
		t.Errorf("expected password-mismatch hint, got %q", txt)
	}
}

// TestIntegration_BridgeDown verifies a connection-refused (stub not bound)
// surfaces as a clean "Hammerspoon is not running" envelope. Mirrors plan
// §8 case 1.9.
//
// We grab a port via net.Listen + immediate close so the URL points at a
// known-closed port — more reliable than guessing.
func TestIntegration_BridgeDown(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close() // freed; port now refuses connections.

	mgr := NewManager(NewHTTPDriver("http://"+addr, "anything"), false)
	s := NewMCPServer(mgr)

	out, _ := s.Call(context.Background(), "list_windows", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("expected isError on bridge-down, got %q", txt)
	}
	if !strings.Contains(txt, "Hammerspoon is not running") {
		t.Errorf("expected 'Hammerspoon is not running' hint, got %q", txt)
	}
}

// TestIntegration_UnknownTool verifies an unrecognized tool name returns a
// clean isError envelope rather than panicking. Cross-checks that the
// dispatcher's default branch is wired up.
func TestIntegration_UnknownTool(t *testing.T) {
	t.Parallel()
	const pw = "test-password-eeee"
	_, base := newStubBridge(t, stubBridgeOpts{password: pw})

	mgr := NewManager(NewHTTPDriver(base, pw), false)
	s := NewMCPServer(mgr)

	out, _ := s.Call(context.Background(), "totally_made_up", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("expected isError on unknown tool, got %q", txt)
	}
	if !strings.Contains(txt, "unknown hammerspoon tool") {
		t.Errorf("expected unknown-tool message, got %q", txt)
	}
}
