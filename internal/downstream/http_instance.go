package downstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrAuthRequired indicates the downstream server returned 401 and needs OAuth.
var ErrAuthRequired = errors.New("downstream server requires authentication")

// AuthRequestFunc applies auth to a concrete outbound HTTP request.
type AuthRequestFunc func(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error

// HTTPInstance communicates with a remote MCP server over Streamable HTTP
// (JSON-RPC over HTTP POST). Each request is a separate HTTP POST.
type HTTPInstance struct {
	key    InstanceKey
	url    string
	client *http.Client

	mu          sync.Mutex
	state       InstanceState
	authHeaders http.Header
	applyAuth   AuthRequestFunc
	sessionID   string // Mcp-Session-Id from server
	// protocolVersion is selected during initialize and sent on every
	// subsequent Streamable HTTP request as MCP-Protocol-Version.
	protocolVersion string

	idleTimeout time.Duration
	idleTimer   *time.Timer
	reqID       atomic.Int64

	sessionURL string // may be updated by server via Location header

	// onNotify, when set, is called for every JSON-RPC notification
	// (message without "id") seen in an SSE response stream. This
	// allows the manager to react to server-pushed events such as
	// notifications/tools/list_changed.
	onNotify func(method string, params json.RawMessage)
}

func newHTTPInstance(
	key InstanceKey, url string, idleTimeout time.Duration, headers http.Header, applyAuth AuthRequestFunc,
) *HTTPInstance {
	return &HTTPInstance{
		key:         key,
		url:         url,
		state:       StateStopped,
		authHeaders: headers,
		applyAuth:   applyAuth,
		// No client-level Timeout — the per-call ctx deadline set in
		// Manager.Call (or PerServerListToolsTimeout for tools/list) is
		// authoritative. A client-level Timeout used to short-circuit
		// 60s into every request, which made the per-server
		// call_timeout_sec column useless for HTTP downstreams and
		// truncated long-running but legitimate operations (e.g.
		// Playwright cold-starts or LLM-backed tools).
		client:      &http.Client{},
		idleTimeout: idleTimeout,
	}
}

// SetAuthHeaders updates the authorization headers injected on every request.
func (h *HTTPInstance) SetAuthHeaders(headers http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authHeaders = headers
}

func (h *HTTPInstance) getState() InstanceState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *HTTPInstance) waitRestartDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (h *HTTPInstance) start(ctx context.Context) error {
	h.mu.Lock()
	if h.state != StateStopped {
		s := h.state
		h.mu.Unlock()
		return fmt.Errorf("cannot start http instance in state %s", s)
	}
	h.state = StateStarting
	// A fresh initialize request starts a fresh protocol session. Do not leak
	// prior negotiation or session-routing state into the handshake.
	h.protocolVersion = ""
	h.sessionID = ""
	h.sessionURL = ""
	h.mu.Unlock()

	// Perform MCP initialize handshake over HTTP (mutex released so doRPC can read authHeaders).
	initReq := newInitializeRequest()

	resp, err := h.doRPC(ctx, initReq)
	if err != nil {
		h.mu.Lock()
		h.state = StateStopped
		h.mu.Unlock()
		return fmt.Errorf("initialize: %w", err)
	}
	selected, err := validateInitializeResult(resp)
	if err != nil {
		h.mu.Lock()
		h.state = StateStopped
		h.mu.Unlock()
		return fmt.Errorf("initialize: %w", err)
	}
	h.mu.Lock()
	h.protocolVersion = selected
	h.mu.Unlock()

	// Send initialized notification (no ID = notification).
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	// Non-fatal: some servers don't handle initialized notifications.
	_, _ = h.doRPC(ctx, notif)

	h.mu.Lock()
	h.state = StateReady
	h.mu.Unlock()
	return nil
}

func (h *HTTPInstance) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	h.state = StateStopped
}

// ListTools sends a tools/list request to the HTTP MCP server.
func (h *HTTPInstance) ListTools(ctx context.Context) (json.RawMessage, error) {
	h.mu.Lock()
	h.state = StateBusy
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.state = StateIdle
		h.resetIdleTimer()
		h.mu.Unlock()
	}()

	id := h.reqID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf(`%d`, id)),
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
	}

	data, err := h.doRPC(ctx, req)
	if err != nil && ctx.Err() != nil {
		h.sendCancelled(int(id), ctx.Err().Error())
	}
	return data, err
}

// Call sends a tools/call request to the HTTP MCP server.
// Context cancellation aborts the HTTP request via the context passed
// to doRPC. A best-effort notifications/cancelled notification is sent
// as a separate fire-and-forget POST so the server can clean up.
func (h *HTTPInstance) Call(
	ctx context.Context, method string, params json.RawMessage,
) (json.RawMessage, error) {
	h.mu.Lock()
	h.state = StateBusy
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.state = StateIdle
		h.resetIdleTimer()
		h.mu.Unlock()
	}()

	id := h.reqID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf(`%d`, id)),
		Method:  method,
		Params:  params,
	}

	data, err := h.doRPC(ctx, req)
	if err != nil && ctx.Err() != nil {
		// Context was cancelled — send best-effort cancellation
		// notification. The HTTP request is already aborted by context
		// cancellation, but the server may still be processing.
		h.sendCancelled(int(id), ctx.Err().Error())
	}
	return data, err
}

// sendCancelled sends a best-effort notifications/cancelled JSON-RPC
// notification to the HTTP downstream via a separate POST. If the
// request fails (network error, auth failure, etc.), the error is
// silently ignored — the caller already received a context error.
func (h *HTTPInstance) sendCancelled(requestID int, reason string) {
	params, err := json.Marshal(map[string]any{
		"requestId": requestID,
		"reason":    reason,
	})
	if err != nil {
		return
	}
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/cancelled",
		Params:  params,
	}
	body, err := json.Marshal(notif)
	if err != nil {
		return
	}

	url := h.url
	if h.sessionURL != "" {
		url = h.sessionURL
	}

	cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(
		cancelCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	h.mu.Lock()
	headers := h.authHeaders
	sid := h.sessionID
	h.mu.Unlock()
	for k, vals := range headers {
		for _, v := range vals {
			httpReq.Header.Set(k, v)
		}
	}
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// doRPC sends a JSON-RPC request via HTTP POST and returns the result.
func (h *HTTPInstance) doRPC(ctx context.Context, rpcReq jsonRPCRequest) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := h.url
	if h.sessionURL != "" {
		url = h.sessionURL
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	// Inject auth headers (e.g. Authorization: Bearer <token>).
	h.mu.Lock()
	headers := h.authHeaders
	applyAuth := h.applyAuth
	sid := h.sessionID
	protocolVersion := h.protocolVersion
	h.mu.Unlock()
	for k, vals := range headers {
		for _, v := range vals {
			httpReq.Header.Set(k, v)
		}
	}

	// Include session ID from previous initialize handshake.
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	if protocolVersion != "" {
		httpReq.Header.Set("MCP-Protocol-Version", protocolVersion)
	}

	if applyAuth != nil && h.key.AuthScopeID != "" {
		if err := applyAuth(ctx, h.key.AuthScopeID, httpReq, body); err != nil {
			return nil, fmt.Errorf("apply auth: %w", err)
		}
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture session ID from server (returned on initialize, echoed thereafter).
	if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
		h.mu.Lock()
		h.sessionID = v
		h.mu.Unlock()
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuthRequired
	}

	// Notifications return 202 with no body.
	if rpcReq.ID == nil {
		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
			return nil, nil
		}
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("notification failed (%d): %s", resp.StatusCode, respBody)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, respBody)
	}

	ct := resp.Header.Get("Content-Type")

	// Handle SSE responses (text/event-stream).
	if strings.HasPrefix(ct, "text/event-stream") {
		return h.readSSEResponse(resp.Body, rpcReq.ID)
	}

	// Standard JSON response.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if rpcResp.ID == nil {
		return nil, fmt.Errorf("rpc response missing id")
	}
	if !rawIDMatches(rpcResp.ID, rpcReq.ID) {
		return nil, fmt.Errorf(
			"%w: got response id %s, expected %s",
			ErrResponseDesync, string(rpcResp.ID), strings.TrimSpace(string(rpcReq.ID)))
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// readSSEResponse reads a text/event-stream response and extracts the
// JSON-RPC result. Per MCP Streamable HTTP spec, the server sends SSE
// events with "data:" lines containing JSON-RPC messages. Notifications
// (messages without "id") are forwarded via onNotify; the final response
// (message with "id") is returned as the result.
//
// SSE events are delimited by blank lines. A single event may span
// multiple "data:" lines (concatenated with newlines before parsing).
// Lines starting with ":" are comments and are ignored.
func (h *HTTPInstance) readSSEResponse(body io.Reader, expectID json.RawMessage) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	// GitHub's MCP API returns large tool lists that exceed the default 64KB buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // up to 4MB

	var dataBuf strings.Builder
	var haveData bool
	var finalResult json.RawMessage
	var finalErr error
	var haveResponse bool

	flushEvent := func() bool {
		if !haveData {
			return false
		}
		haveData = false
		raw := strings.TrimSpace(dataBuf.String())
		dataBuf.Reset()
		if raw == "" {
			return false
		}
		result, done, err := h.processSSEData(raw, expectID)
		if done {
			haveResponse = true
			finalResult = result
			finalErr = err
		}
		return done
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Blank line = end of current SSE event.
		if line == "" {
			if flushEvent() {
				break
			}
			continue
		}

		// SSE comment line — ignore.
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE field: "field:value" or "field: value".
		field, value := line, ""
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			field = line[:idx]
			value = line[idx+1:]
			value = strings.TrimPrefix(value, " ")
		}

		switch field {
		case "data":
			if haveData {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(value)
			haveData = true
		case "event", "id", "retry":
			// MCP Streamable HTTP does not use these; ignore.
		default:
			// Unknown SSE field — ignore per spec.
		}
	}

	// Flush any trailing event (no final blank line).
	if !haveResponse {
		flushEvent()
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read sse stream: %w", err)
	}
	if finalErr != nil {
		return nil, finalErr
	}
	if !haveResponse {
		return nil, fmt.Errorf("no result in sse stream")
	}
	return finalResult, nil
}

// processSSEData parses a single SSE data payload (the concatenated
// "data:" lines for one event). It returns done=true only when a JSON-RPC
// response matching expectID has been seen.
func (h *HTTPInstance) processSSEData(
	raw string, expectID json.RawMessage,
) (json.RawMessage, bool, error) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *jsonRPCError   `json:"error,omitempty"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, false, nil
	}

	// Server notifications and unsupported server-to-client requests have a
	// method. Record them, but do not mistake result/error-shaped params for
	// this request's response.
	if msg.Method != "" {
		if h.onNotify != nil {
			slog.Debug("http downstream notification",
				"server", h.key.ServerID, "method", msg.Method)
			h.onNotify(msg.Method, msg.Params)
		}
		return nil, false, nil
	}

	// JSON-RPC response: has "id".
	if msg.ID != nil {
		if !rawIDMatches(msg.ID, expectID) {
			return nil, true, fmt.Errorf(
				"%w: got response id %s, expected %s",
				ErrResponseDesync, string(msg.ID), strings.TrimSpace(string(expectID)))
		}
		if msg.Error != nil {
			return nil, true, fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		if msg.Result != nil {
			return msg.Result, true, nil
		}
		return nil, true, nil
	}

	return nil, false, nil
}

func rawIDMatches(got, want json.RawMessage) bool {
	gotID := normalizeJSONRPCID(got)
	wantID := normalizeJSONRPCID(want)
	return gotID != "" && gotID == wantID
}

func normalizeJSONRPCID(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func (h *HTTPInstance) resetIdleTimer() {
	if h.idleTimeout <= 0 {
		return
	}
	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	h.idleTimer = time.AfterFunc(h.idleTimeout, func() {
		h.stop()
	})
}
