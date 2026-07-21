package hammerspoon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// defaultBridgeTimeout caps any single Lua execution that didn't specify one.
const defaultBridgeTimeout = 5 * time.Second

// httpDriver speaks to Hammerspoon's hs.httpserver over loopback. It POSTs a
// JSON body to <url>/exec with a Bearer auth header carrying the shared
// password and expects a JSON envelope back.
//
// The driver is intentionally stateless — no connection pool tuning, no
// retries. Hammerspoon is local, so the only realistic transport failures are
// "app not running" (connection refused) and "wrong password" (401). Both are
// reported to the agent via the envelope so it can guide the user to a fix.
type httpDriver struct {
	url    string
	bearer string
	client *http.Client
}

// NewHTTPDriver constructs an HTTP-transport bridge. The url should be the
// base URL of the hs.httpserver — the driver appends /exec. The bearer is
// the shared password mcplexer generated when the integration was enabled.
func NewHTTPDriver(baseURL, bearer string) Bridge {
	return &httpDriver{
		url:    baseURL,
		bearer: bearer,
		client: &http.Client{
			// Per-request deadlines are imposed via context in Exec — leave
			// the client-level timeout open so context cancellation is the
			// single source of truth.
			Transport: &http.Transport{
				DisableKeepAlives:   false,
				MaxIdleConns:        4,
				MaxConnsPerHost:     2,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 2 * time.Second,
			},
		},
	}
}

// Exec runs the given Lua snippet against the configured bridge.
//
// The transport contract:
//   - 2xx + valid envelope → return the envelope.
//   - 401 → "bridge password mismatch" envelope (so the agent gets a clear
//     fix-it message rather than a raw HTTP code).
//   - 5xx → envelope with the response body folded into err.
//   - Connection refused / network error → "Hammerspoon not running".
//   - Context deadline → timeout envelope.
//
// All four are mapped into an Envelope{Ok:false, Err:...} rather than a Go
// error so MCPServer.Call can render one consistent CallToolResult shape.
func (d *httpDriver) Exec(ctx context.Context, lua string, timeout time.Duration) (Envelope, error) {
	if timeout <= 0 {
		timeout = defaultBridgeTimeout
	}

	target, err := url.JoinPath(d.url, "/exec")
	if err != nil {
		return Envelope{}, fmt.Errorf("hammerspoon: bad bridge url %q: %w", d.url, err)
	}

	body, err := json.Marshal(map[string]any{
		"lua":        lua,
		"timeout_ms": timeout.Milliseconds(),
	})
	if err != nil {
		return Envelope{}, fmt.Errorf("hammerspoon: marshal request: %w", err)
	}

	// Allow a small head-room over the Lua-side timeout so we get a clean
	// envelope back rather than racing the transport against the handler.
	reqCtx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Envelope{}, fmt.Errorf("hammerspoon: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if d.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+d.bearer)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return Envelope{Ok: false, Err: classifyTransportError(err)}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode below
	case http.StatusUnauthorized:
		return Envelope{Ok: false, Err: "Bridge password mismatch. Reset under Credentials → Hammerspoon."}, nil
	default:
		if resp.StatusCode >= 500 {
			return Envelope{Ok: false, Err: fmt.Sprintf("Hammerspoon bridge HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))}, nil
		}
		return Envelope{Ok: false, Err: fmt.Sprintf("Hammerspoon bridge HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))}, nil
	}

	var env Envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return Envelope{Ok: false, Err: "Hammerspoon bridge returned malformed JSON: " + err.Error()}, nil
	}
	return env, nil
}

// classifyTransportError maps low-level net errors to user-meaningful strings.
// Hammerspoon-not-running is the dominant case; surface it explicitly.
func classifyTransportError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Hammerspoon bridge timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "Hammerspoon bridge call cancelled"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Treat any dial-level failure as "app not running" — the only
		// realistic alternative on loopback is "port collision", which the
		// user notices via the install probe.
		return "Hammerspoon is not running, or the bridge is not installed. Launch Hammerspoon.app and require('hammerspoon-mcp')."
	}
	return "Hammerspoon bridge call failed: " + err.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
