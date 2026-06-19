package hammerspoon

import (
	"context"
	"encoding/json"
	"time"
)

// Bridge is the transport between mcplexer and the Hammerspoon Lua runtime.
// Implementations execute a Lua snippet and return a normalized envelope.
//
// Two production drivers ship with mcplexer:
//   - httpDriver: POSTs to hs.httpserver running inside Hammerspoon. Fast,
//     keeps the Lua process warm. Default.
//   - cliDriver:  Shells out to the `hs -c` CLI. Zero-config, slow (~50× the
//     HTTP path). Selected explicitly via HAMMERSPOON_DRIVER=cli.
//
// The bridge transports just the JSON envelope so the two drivers are
// interchangeable from MCPServer's point of view.
type Bridge interface {
	// Exec runs the given Lua snippet against the user's Hammerspoon runtime
	// and returns the {ok, result, err} envelope. The returned error is
	// non-nil only for transport-level failures (network, exec, parse) — a
	// successful round-trip with ok=false is reported via the envelope.
	Exec(ctx context.Context, lua string, timeout time.Duration) (Envelope, error)
}

// Envelope is the JSON shape returned by both drivers. It mirrors what the
// embedded init.lua emits: {ok, result, err}.
type Envelope struct {
	Ok     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"err,omitempty"`
}

// nullBridge is the placeholder used when the integration is disabled or no
// driver is configured. Every Exec call returns a clean envelope so the
// MCPServer can surface a uniform "downstream not enabled" error to agents
// instead of crashing the gateway.
type nullBridge struct{}

// Exec on nullBridge always reports the downstream is not enabled.
func (nullBridge) Exec(_ context.Context, _ string, _ time.Duration) (Envelope, error) {
	return Envelope{
		Ok:  false,
		Err: "Hammerspoon downstream not enabled. Enable it in the mcplexer dashboard.",
	}, nil
}

// isNullBridge reports whether b is the placeholder bridge.
func isNullBridge(b Bridge) bool {
	_, ok := b.(nullBridge)
	return ok
}
