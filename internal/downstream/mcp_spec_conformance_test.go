package downstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// MCP Spec Conformance — Transport Layer
//
// These tests probe the gaps identified in the 2025-11-25 spec audit:
//
//   • SSE readSSEResponse returns the first matching JSON-RPC result while
//     forwarding interleaved notifications/progress/logging messages.
//   • Stdio forwardNotification preserves params.
//   • Manager.handleDownstreamNotify routes tools/list_changed and journals
//     all downstream notification methods.
//   • Gateway ServerCapability advertises only Tools (listChanged) — no
//     Resources, Prompts, Completion, Logging, or Sampling capability.
//
// Tests are assertion-based: they pin the CURRENT behaviour (including gaps)
// so any future change to close a gap is forced to update the test, and any
// regression that re-opens a gap is caught.
// ---------------------------------------------------------------------------

// --- fake SSE helpers -------------------------------------------------------

// sseEvent formats a JSON-RPC message as an SSE "data:" line.
func sseEvent(msg any) string {
	data, _ := json.Marshal(msg)
	return "data: " + string(data) + "\n\n"
}

// sseResponse builds a text/event-stream body from a sequence of SSE lines.
func sseResponse(lines ...string) string {
	return strings.Join(lines, "")
}

// makeHTTPInstance creates a minimal HTTPInstance for unit-testing
// readSSEResponse without spinning up a real httptest.Server.
func makeHTTPInstance() *HTTPInstance {
	return newHTTPInstance(InstanceKey{}, "http://test", 0, nil, nil)
}

// --- SSE: notification stripping -------------------------------------------

// TestReadSSEResponse_NotificationsForwarded verifies an SSE stream that
// interleaves notifications/progress and notifications/message (logging)
// before the result forwards those notifications, then returns the matching
// result.
//
// Spec ref: 2025-11-25/basic/transports — Streamable HTTP servers MAY
// interleave server-initiated notifications on the SSE stream. The client
// surfaces them through onNotify and the manager event journal.
func TestReadSSEResponse_NotificationsForwarded(t *testing.T) {
	h := makeHTTPInstance()
	var got []struct {
		method string
		params json.RawMessage
	}
	h.onNotify = func(method string, params json.RawMessage) {
		got = append(got, struct {
			method string
			params json.RawMessage
		}{method: method, params: params})
	}
	body := strings.NewReader(sseResponse(
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progressToken": "tok-1", "progress": 50, "total": 100},
		}),
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/message",
			"params":  map[string]any{"level": "info", "data": "working..."},
		}),
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"tools": []any{}},
		}),
	))

	result, err := h.readSSEResponse(body, json.RawMessage(`1`))
	if err != nil {
		t.Fatalf("readSSEResponse: %v", err)
	}
	if result == nil {
		t.Fatal("readSSEResponse returned nil result")
	}
	// Verify the result is the tools list (the third event).
	var parsed struct {
		Tools []any `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d notifications, want 2", len(got))
	}
	if got[0].method != "notifications/progress" || !strings.Contains(string(got[0].params), `"progressToken"`) {
		t.Fatalf("progress notification not forwarded with params: %+v", got[0])
	}
	if got[1].method != "notifications/message" || !strings.Contains(string(got[1].params), `"working..."`) {
		t.Fatalf("message notification not forwarded with params: %+v", got[1])
	}
}

// TestReadSSEResponse_OnlyFirstResult verifies that when multiple result-bearing
// messages appear in an SSE stream, only the FIRST is returned. Subsequent
// results are never read because readSSEResponse returns immediately.
//
// Spec ref: 2025-11-25/basic/transports — a response to a single request
// SHOULD produce exactly one result, but a spec-loose server could send
// multiple. MCPlexer takes the first and ignores the rest.
func TestReadSSEResponse_OnlyFirstResult(t *testing.T) {
	h := makeHTTPInstance()
	body := strings.NewReader(sseResponse(
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"value": "first"},
		}),
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"value": "second"},
		}),
	))

	result, err := h.readSSEResponse(body, json.RawMessage(`1`))
	if err != nil {
		t.Fatalf("readSSEResponse: %v", err)
	}
	var parsed struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Value != "first" {
		t.Errorf("expected first result, got %q", parsed.Value)
	}
}

// TestReadSSEResponse_EmptyStreamReturnsError verifies that an SSE stream
// with no result-bearing message returns an error.
func TestReadSSEResponse_EmptyStreamReturnsError(t *testing.T) {
	h := makeHTTPInstance()
	body := strings.NewReader(sseResponse(
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progress": 10},
		}),
	))

	_, err := h.readSSEResponse(body, json.RawMessage(`1`))
	if err == nil {
		t.Fatal("expected error for SSE stream with no result, got nil")
	}
}

// TestReadSSEResponse_RPCErrorPropagated verifies that an SSE event carrying
// a JSON-RPC error is surfaced as a Go error.
func TestReadSSEResponse_RPCErrorPropagated(t *testing.T) {
	h := makeHTTPInstance()
	body := strings.NewReader(sseResponse(
		sseEvent(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]any{"code": -32601, "message": "method not found"},
		}),
	))

	_, err := h.readSSEResponse(body, json.RawMessage(`1`))
	if err == nil {
		t.Fatal("expected error for RPC error in SSE, got nil")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error should contain RPC message, got: %v", err)
	}
}

// --- HTTP transport: Accept header conformance -----------------------------

// TestHTTPInstance_AcceptHeaderConformance verifies the downstream HTTP
// client sends Accept: application/json, text/event-stream as required by
// the Streamable HTTP transport spec.
//
// Spec ref: 2025-11-25/basic/transports — clients MUST include
// "text/event-stream" in the Accept header.
func TestHTTPInstance_AcceptHeaderConformance(t *testing.T) {
	var gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		// Always return a valid JSON-RPC response.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "test-accept"}, ts.URL, 0, nil, nil)
	if err := h.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if !strings.Contains(gotAccept, "application/json") {
		t.Errorf("Accept header missing application/json: %q", gotAccept)
	}
	if !strings.Contains(gotAccept, "text/event-stream") {
		t.Errorf("Accept header missing text/event-stream: %q", gotAccept)
	}
}

// --- HTTP transport: SSE vs JSON routing -----------------------------------

// TestHTTPInstance_RoutesSSEAndJSON verifies the HTTP instance correctly
// parses both application/json and text/event-stream responses.
func TestHTTPInstance_RoutesSSEAndJSON(t *testing.T) {
	t.Run("JSON response", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
		}))
		defer ts.Close()

		h := newHTTPInstance(InstanceKey{ServerID: "json-srv"}, ts.URL, 0, nil, nil)
		if err := h.start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		result, err := h.Call(context.Background(), "tools/call", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		var parsed struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !parsed.OK {
			t.Error("expected ok=true in JSON response")
		}
	})

	t.Run("SSE response", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
		}))
		defer ts.Close()

		h := newHTTPInstance(InstanceKey{ServerID: "sse-srv"}, ts.URL, 0, nil, nil)
		if err := h.start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		result, err := h.Call(context.Background(), "tools/call", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		var parsed struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !parsed.OK {
			t.Error("expected ok=true in SSE response")
		}
	})
}

// --- Stdio: forwardNotification preserves params ---------------------------

// TestMCPConformanceForwardNotification_PreservesParams verifies the stdio downstream's
// forwardNotification extracts the method and preserves params for progress
// tokens, log data, and resource update payloads.
//
// Spec ref: 2025-11-25/basic/utilities/progress — progress notifications
// carry params: {progressToken, progress, total, message}.
func TestMCPConformanceForwardNotification_PreservesParams(t *testing.T) {
	var capturedMethod string
	var capturedParams json.RawMessage
	var callCount int
	inst := &Instance{
		key: InstanceKey{ServerID: "test-notif"},
		onNotify: func(method string, params json.RawMessage) {
			capturedMethod = method
			capturedParams = params
			callCount++
		},
	}

	// A progress notification with full params.
	progressNotif := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"tok-1","progress":50,"total":100}}`)
	inst.forwardNotification(progressNotif)

	if callCount != 1 {
		t.Fatalf("expected onNotify called once, got %d", callCount)
	}
	if capturedMethod != "notifications/progress" {
		t.Errorf("method = %q, want notifications/progress", capturedMethod)
	}
	if !strings.Contains(string(capturedParams), `"progressToken"`) ||
		!strings.Contains(string(capturedParams), `"progress":50`) {
		t.Fatalf("params were not preserved: %s", capturedParams)
	}
}

// TestForwardNotification_NilOnNotify verifies that a nil onNotify callback
// does not panic.
func TestForwardNotification_NilOnNotify(t *testing.T) {
	inst := &Instance{key: InstanceKey{ServerID: "test"}}
	// Should not panic.
	inst.forwardNotification([]byte(`{"jsonrpc":"2.0","method":"notifications/progress"}`))
}

// TestForwardNotification_MalformedJSON verifies malformed JSON is silently
// swallowed (returns without calling onNotify).
func TestForwardNotification_MalformedJSON(t *testing.T) {
	var called bool
	inst := &Instance{
		onNotify: func(method string, params json.RawMessage) { called = true },
	}
	inst.forwardNotification([]byte(`not valid json`))
	if called {
		t.Error("onNotify should not be called for malformed JSON")
	}
}

// --- Stdio: readResponse interleaved notifications -------------------------

// TestReadResponse_InterleavedNotifications verifies that readResponse
// correctly skips interleaved notifications while waiting for the matching
// response. This confirms the core notification-forwarding loop works but
// also documents that params are dropped in the process.
func TestReadResponse_InterleavedNotifications(t *testing.T) {
	var forwardedMethods []string
	var forwardedParams []json.RawMessage
	var mu sync.Mutex
	inst := &Instance{
		key: InstanceKey{ServerID: "test-interleave"},
		onNotify: func(method string, params json.RawMessage) {
			mu.Lock()
			forwardedMethods = append(forwardedMethods, method)
			forwardedParams = append(forwardedParams, params)
			mu.Unlock()
		},
	}

	// Simulate a stdout stream: notification, notification, then response.
	lines := []string{
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":25}}`,
		`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	result, err := inst.readResponse(scanner, 1)
	if err != nil {
		t.Fatalf("readResponse: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(forwardedMethods) != 2 {
		t.Fatalf("expected 2 forwarded notifications, got %d", len(forwardedMethods))
	}
	if forwardedMethods[0] != "notifications/progress" {
		t.Errorf("first notification = %q, want notifications/progress", forwardedMethods[0])
	}
	if forwardedMethods[1] != "notifications/message" {
		t.Errorf("second notification = %q, want notifications/message", forwardedMethods[1])
	}
	if !strings.Contains(string(forwardedParams[0]), `"progress":25`) {
		t.Errorf("first notification params not preserved: %s", forwardedParams[0])
	}
	if !strings.Contains(string(forwardedParams[1]), `"level":"info"`) {
		t.Errorf("second notification params not preserved: %s", forwardedParams[1])
	}
}

// --- Stdio: response ID matching -------------------------------------------

// TestResponseIDMatches_Numeric verifies numeric ID matching.
func TestResponseIDMatches_Numeric(t *testing.T) {
	if !responseIDMatches(json.RawMessage(`5`), 5) {
		t.Error("numeric id 5 should match expectID 5")
	}
	if responseIDMatches(json.RawMessage(`5`), 6) {
		t.Error("numeric id 5 should not match expectID 6")
	}
}

// TestResponseIDMatches_StringEncoded verifies string-encoded IDs ("5")
// are tolerated (spec-loose servers).
func TestResponseIDMatches_StringEncoded(t *testing.T) {
	if !responseIDMatches(json.RawMessage(`"5"`), 5) {
		t.Error(`string id "5" should match expectID 5`)
	}
}

// --- Manager: handleDownstreamNotify routing -------------------------------

// TestHandleDownstreamNotify_ToolsListChanged verifies the ONLY notification
// method that triggers fan-out is notifications/tools/list_changed.
func TestHandleDownstreamNotify_ToolsListChanged(t *testing.T) {
	m := &Manager{
		health:        NewHealthTracker(),
		eventJournals: newJournalRegistry(),
	}
	key := InstanceKey{ServerID: "test-tools"}

	fanOut := make(chan struct{}, 1)
	unsub := m.SubscribeToolsChanged(func() {
		fanOut <- struct{}{}
	})
	defer unsub()

	m.handleDownstreamNotify(key, "notifications/tools/list_changed", json.RawMessage(`{"reason":"test"}`))

	// Fan-out is async (goroutine per subscriber); synchronize with the
	// callback instead of racing a shared counter against a fixed sleep.
	select {
	case <-fanOut:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tools-changed fan-out")
	}
	state := m.eventJournals.since(key, 0, 10, nil)
	if len(state.Events) != 1 || state.Events[0].Method != "notifications/tools/list_changed" {
		t.Fatalf("tools notification not journaled: %+v", state.Events)
	}
}

// TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout verifies
// non-tools notification methods are journaled but do not trigger
// tools/list_changed fan-out.
func TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout(t *testing.T) {
	m := &Manager{
		health:        NewHealthTracker(),
		eventJournals: newJournalRegistry(),
	}
	key := InstanceKey{ServerID: "test-other"}

	otherMethods := []string{
		"notifications/resources/list_changed",
		"notifications/prompts/list_changed",
		"notifications/resources/updated",
		"notifications/progress",
		"notifications/message",
		"notifications/cancelled",
		"notifications/initialized",
	}

	for _, method := range otherMethods {
		fanOut := make(chan struct{}, 1)
		unsub := m.SubscribeToolsChanged(func() { fanOut <- struct{}{} })
		m.handleDownstreamNotify(key, method, json.RawMessage(`{"source":"test"}`))
		select {
		case <-fanOut:
			unsub()
			t.Errorf("method %q should NOT trigger tools-changed fan-out", method)
		case <-time.After(20 * time.Millisecond):
			unsub()
		}
	}
	state := m.eventJournals.since(key, 0, 20, nil)
	if len(state.Events) != len(otherMethods) {
		t.Fatalf("journaled events = %d, want %d", len(state.Events), len(otherMethods))
	}
}

// --- HTTP instance: session ID handling ------------------------------------

// TestHTTPInstance_SessionIDPropagated verifies the Mcp-Session-Id header
// from the initialize response is captured and sent on subsequent requests.
//
// Spec ref: 2025-11-25/basic/transports — the server MAY assign a session
// ID; the client MUST include it on subsequent requests.
func TestHTTPInstance_SessionIDPropagated(t *testing.T) {
	var sessionIDs []string
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessionIDs = append(sessionIDs, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()

		w.Header().Set("Mcp-Session-Id", "sess-test-123")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "sess-srv"}, ts.URL, 0, nil, nil)
	if err := h.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Second call should include the session ID from initialize.
	_, _ = h.Call(context.Background(), "tools/call", json.RawMessage(`{}`))

	mu.Lock()
	defer mu.Unlock()
	if len(sessionIDs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(sessionIDs))
	}
	// First request (initialize) has no session ID.
	if sessionIDs[0] != "" {
		t.Errorf("first request should have empty session ID, got %q", sessionIDs[0])
	}
	// Second request carries the session ID.
	if sessionIDs[1] != "sess-test-123" {
		t.Errorf("second request should carry session ID, got %q", sessionIDs[1])
	}
}

// --- HTTP instance: notification (no ID) returns nil -----------------------

// TestHTTPInstance_NotificationAccepted verifies that requests with no ID
// (notifications) are accepted (202/200) and return (nil, nil).
func TestHTTPInstance_NotificationAccepted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "notif-srv"}, ts.URL, 0, nil, nil)
	// start() sends initialize (with ID) + initialized notification (no ID).
	// The test server returns 202 for everything, which fails initialize.
	// That's fine — we just need the instance object to test doRPC.
	_ = h.start(context.Background())
	// Directly test the notification path.
	result, err := h.doRPC(context.Background(), jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Errorf("notification should succeed, got error: %v", err)
	}
	if result != nil {
		t.Errorf("notification should return nil result, got %v", result)
	}
}

// --- HTTP instance: 401 auth required --------------------------------------

// TestHTTPInstance_AuthRequired verifies a 401 response returns ErrAuthRequired.
func TestHTTPInstance_AuthRequired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "auth-srv"}, ts.URL, 0, nil, nil)
	_, err := h.doRPC(context.Background(), jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
	})
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "authentication") && !isAuthError(err) {
		t.Errorf("expected auth-related error, got: %v", err)
	}
}

// --- Initialize handshake protocol version ---------------------------------

// TestHTTPInstance_InitializeProtocolVersion verifies the initialize request
// sends protocolVersion "2025-03-26" (the version the gateway currently
// speaks, even though the latest spec is 2025-11-25).
func TestHTTPInstance_InitializeProtocolVersion(t *testing.T) {
	var initBody jsonRPCRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonRPCRequest
		_ = json.Unmarshal(body, &req)
		// Capture only the initialize request (start() also sends
		// notifications/initialized, which would overwrite initBody).
		if req.Method == "initialize" {
			initBody = req
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}`))
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "pv-srv"}, ts.URL, 0, nil, nil)
	if err := h.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if initBody.Method != "initialize" {
		t.Errorf("captured method = %q, want initialize", initBody.Method)
	}

	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(initBody.Params, &params)
	if params.ProtocolVersion != "2025-03-26" {
		t.Errorf("protocolVersion = %q, want 2025-03-26", params.ProtocolVersion)
	}
}

// --- HTTP instance: initialized notification sent --------------------------

// TestHTTPInstance_InitializedNotificationSent verifies the gateway sends
// notifications/initialized after receiving the initialize response.
func TestHTTPInstance_InitializedNotificationSent(t *testing.T) {
	var methods []string
	var mu sync.Mutex
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonRPCRequest
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		methods = append(methods, req.Method)
		callCount++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}`))
		} else {
			// Notification — return 202.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer ts.Close()

	h := newHTTPInstance(InstanceKey{ServerID: "init-srv"}, ts.URL, 0, nil, nil)
	if err := h.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(methods))
	}
	if methods[0] != "initialize" {
		t.Errorf("first request = %q, want initialize", methods[0])
	}
	if methods[1] != "notifications/initialized" {
		t.Errorf("second request = %q, want notifications/initialized", methods[1])
	}
}
