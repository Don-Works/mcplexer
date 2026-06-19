package downstream

import (
	"context"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// browserSessionIDKey carries the per-agent isolation id used to give
// browser-automation downstreams their own process per logical agent. The
// gateway sets it from the MCP session id (interactive/socket clients); the
// worker dispatcher sets it to "worker:<WorkerID>" (in-process workers share
// one uninitialized gateway session, so they need an explicit id). Any other
// path leaves it empty, which keeps the shared single-instance lifecycle.
type browserSessionIDKey struct{}

// WithBrowserSessionID stamps ctx with the isolation id for browser-class
// downstream instances. Callers must NOT overwrite a non-empty value already
// present — the worker dispatcher sets a worker-scoped id before the gateway
// would fall back to the (empty) worker-session id. Empty id is a no-op.
func WithBrowserSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, browserSessionIDKey{}, id)
}

// BrowserSessionIDFromContext returns the per-agent isolation id, or "" when
// the call is not scoped to a particular agent (the shared default).
func BrowserSessionIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(browserSessionIDKey{}).(string)
	return v
}

// browserIsolationHints are the substrings that mark a downstream as a
// browser-automation server whose instance must be isolated per agent. A
// browser process holds a single live page + cookies + navigation state, so
// two agents sharing one process collide (one navigates the other's tab).
// Mirrors the breadth of the cache layer's isBrowserAutomationServer so the
// "don't cache" and "don't share the process" decisions stay in lock-step.
var browserIsolationHints = []string{
	"browser",    // agent_browser, browser-use, any *browser* server id/ns
	"playwright", // @playwright/mcp, playwright-mcp
	"puppeteer",  // puppeteer MCP servers
	"chrome",     // chrome-devtools / cdp bridges
}

// ShouldIsolatePerSession reports whether a downstream server's instances
// must be keyed per logical agent (one process each) rather than shared. It
// matches browser-automation servers by id, tool namespace, and the
// command+args text — any of which carrying a browser hint is enough. This
// is intentionally heuristic and transport-agnostic: the keying decision is
// about process statefulness, not about how the process is reached.
func ShouldIsolatePerSession(s store.DownstreamServer) bool {
	hay := strings.ToLower(strings.Join([]string{
		s.ID,
		s.ToolNamespace,
		s.Command,
		string(s.Args),
	}, " "))
	for _, hint := range browserIsolationHints {
		if strings.Contains(hay, hint) {
			return true
		}
	}
	return false
}
