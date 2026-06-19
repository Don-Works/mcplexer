package downstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestShouldIsolatePerSession(t *testing.T) {
	cases := []struct {
		name string
		srv  store.DownstreamServer
		want bool
	}{
		{"playwright by id", store.DownstreamServer{ID: "playwright", ToolNamespace: "playwright"}, true},
		{"agent_browser by id", store.DownstreamServer{ID: "agent_browser", ToolNamespace: "agent_browser"}, true},
		{"puppeteer by namespace", store.DownstreamServer{ID: "p1", ToolNamespace: "puppeteer"}, true},
		{"browser-use by command", store.DownstreamServer{ID: "auto", ToolNamespace: "auto", Command: "uvx", Args: json.RawMessage(`["browser-use-mcp"]`)}, true},
		{"playwright by args", store.DownstreamServer{ID: "auto2", ToolNamespace: "auto2", Command: "npx", Args: json.RawMessage(`["-y","@playwright/mcp@latest","--headless"]`)}, true},
		{"chrome-devtools by id", store.DownstreamServer{ID: "chrome-devtools"}, true},
		{"github not isolated", store.DownstreamServer{ID: "github", ToolNamespace: "github", Command: "npx", Args: json.RawMessage(`["-y","@modelcontextprotocol/server-github"]`)}, false},
		{"linear not isolated", store.DownstreamServer{ID: "linear", ToolNamespace: "linear"}, false},
		{"empty server not isolated", store.DownstreamServer{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldIsolatePerSession(tc.srv); got != tc.want {
				t.Fatalf("ShouldIsolatePerSession(%+v) = %v, want %v", tc.srv, got, tc.want)
			}
		})
	}
}

// browserEchoServer is an httptest MCP server that counts initialize calls
// (one per distinct downstream instance) and answers tools/call.
func browserEchoServer(t *testing.T, initCount *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			initCount.Add(1)
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/call":
			writeRPCResult(t, w, req.ID, `{"content":[{"type":"text","text":"ok"}],"isError":false}`)
		default:
			writeRPCResult(t, w, req.ID, `{"ok":true}`)
		}
	}))
}

func countSessionInstances(m *Manager) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for key := range m.instances {
		out[key.SessionID]++
	}
	return out
}

func TestManagerCall_BrowserServerIsolatesPerSession(t *testing.T) {
	m := newHTTPManager(t)
	var inits atomic.Int32
	ts := browserEchoServer(t, &inits)
	defer ts.Close()
	// id contains "browser" => ShouldIsolatePerSession is true.
	registerHTTPServer(t, m, "agent_browser", ts.URL)

	call := func(sessionID string) {
		ctx := WithBrowserSessionID(context.Background(), sessionID)
		if _, err := m.Call(ctx, "agent_browser", "", "browser_navigate", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Call(session=%s): %v", sessionID, err)
		}
	}

	// Session A makes two calls — must reuse one browser instance.
	call("sessA")
	call("sessA")
	// Session B gets its own browser.
	call("sessB")

	if got := inits.Load(); got != 2 {
		t.Fatalf("initialize calls = %d, want 2 (one browser per session, reused within a session)", got)
	}
	bySession := countSessionInstances(m)
	if bySession["sessA"] != 1 || bySession["sessB"] != 1 {
		t.Fatalf("per-session instance counts = %v, want sessA=1 sessB=1", bySession)
	}
	if _, sharedExists := bySession[""]; sharedExists {
		t.Fatalf("unexpected shared (empty-session) browser instance: %v", bySession)
	}
}

func TestManagerCall_NonBrowserServerStaysShared(t *testing.T) {
	m := newHTTPManager(t)
	var inits atomic.Int32
	ts := browserEchoServer(t, &inits)
	defer ts.Close()
	// id has no browser hint => shared single instance regardless of session.
	registerHTTPServer(t, m, "github", ts.URL)

	for _, sid := range []string{"sessA", "sessB", "sessC"} {
		ctx := WithBrowserSessionID(context.Background(), sid)
		if _, err := m.Call(ctx, "github", "", "list_issues", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Call(session=%s): %v", sid, err)
		}
	}

	if got := inits.Load(); got != 1 {
		t.Fatalf("initialize calls = %d, want 1 (non-browser server shared across sessions)", got)
	}
	bySession := countSessionInstances(m)
	if len(bySession) != 1 || bySession[""] != 1 {
		t.Fatalf("instance map = %v, want a single shared (empty-session) instance", bySession)
	}
}

func TestManagerReleaseSession_StopsOnlyThatSession(t *testing.T) {
	m := newHTTPManager(t)
	var inits atomic.Int32
	ts := browserEchoServer(t, &inits)
	defer ts.Close()
	registerHTTPServer(t, m, "playwright", ts.URL)

	for _, sid := range []string{"sessA", "sessB"} {
		ctx := WithBrowserSessionID(context.Background(), sid)
		if _, err := m.Call(ctx, "playwright", "", "browser_navigate", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Call(session=%s): %v", sid, err)
		}
	}
	if got := len(countSessionInstances(m)); got != 2 {
		t.Fatalf("instance count before release = %d, want 2", got)
	}

	m.ReleaseSession("sessA")

	bySession := countSessionInstances(m)
	if _, ok := bySession["sessA"]; ok {
		t.Fatalf("sessA instance still present after ReleaseSession: %v", bySession)
	}
	if bySession["sessB"] != 1 {
		t.Fatalf("sessB instance count = %d, want 1 (unaffected by sessA release)", bySession["sessB"])
	}

	// Releasing the empty id is a no-op and must not touch live instances.
	m.ReleaseSession("")
	if bySession := countSessionInstances(m); bySession["sessB"] != 1 {
		t.Fatalf("sessB lost after ReleaseSession(\"\"): %v", bySession)
	}
}

func TestMaxInstancesForServer(t *testing.T) {
	cases := []struct {
		name string
		srv  *store.DownstreamServer
		want int
	}{
		{"nil server", nil, 0},
		{"browser default", &store.DownstreamServer{ID: "playwright", ToolNamespace: "playwright"}, DefaultBrowserMaxInstances},
		{"browser explicit cap wins", &store.DownstreamServer{ID: "playwright", ToolNamespace: "playwright", MaxInstances: 2}, 2},
		{"non-browser uncapped", &store.DownstreamServer{ID: "github", ToolNamespace: "github"}, 0},
		{"non-browser explicit cap", &store.DownstreamServer{ID: "github", ToolNamespace: "github", MaxInstances: 3}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxInstancesForServer(tc.srv); got != tc.want {
				t.Fatalf("maxInstancesForServer = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestManagerCall_BrowserInstanceCapEvictsOldest pins the leak fix: when more
// distinct sessions drive a browser server than its cap allows, the oldest
// session's browser is evicted so the live process count stays bounded — the
// "Chrome going crazy" regression from per-session isolation without a ceiling.
func TestManagerCall_BrowserInstanceCapEvictsOldest(t *testing.T) {
	orig := DefaultBrowserMaxInstances
	DefaultBrowserMaxInstances = 2
	defer func() { DefaultBrowserMaxInstances = orig }()

	m := newHTTPManager(t)
	var inits atomic.Int32
	ts := browserEchoServer(t, &inits)
	defer ts.Close()
	registerHTTPServer(t, m, "playwright", ts.URL) // browser-class by id

	call := func(sessionID string) {
		ctx := WithBrowserSessionID(context.Background(), sessionID)
		if _, err := m.Call(ctx, "playwright", "", "browser_navigate", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Call(session=%s): %v", sessionID, err)
		}
	}

	// Three distinct sessions, cap of 2 → the oldest (sessA) is evicted when
	// sessC spawns, leaving exactly the two most-recent sessions live.
	call("sessA")
	call("sessB")
	call("sessC")

	bySession := countSessionInstances(m)
	if len(bySession) != 2 {
		t.Fatalf("live instance count = %d (%v), want 2 (cap enforced)", len(bySession), bySession)
	}
	if _, ok := bySession["sessA"]; ok {
		t.Fatalf("sessA (oldest) should have been evicted, still present: %v", bySession)
	}
	if bySession["sessB"] != 1 || bySession["sessC"] != 1 {
		t.Fatalf("expected sessB + sessC live, got %v", bySession)
	}
}
