package downstream

import (
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// IsInteractiveAuthServer reports whether probing a downstream can trigger an
// interactive browser auth flow.
func IsInteractiveAuthServer(s store.DownstreamServer) bool {
	if !strings.EqualFold(s.Transport, "stdio") {
		return false
	}
	return strings.Contains(downstreamProcessText(s), "mcp-remote")
}

// IsBrowserAutomationServer reports whether a downstream is likely to spawn a
// browser automation stack when probed. These servers are valuable, but they
// must start only when explicitly called or reloaded.
func IsBrowserAutomationServer(s store.DownstreamServer) bool {
	if !strings.EqualFold(s.Transport, "stdio") {
		return false
	}
	proc := downstreamProcessText(s)
	return strings.Contains(proc, "@playwright/mcp") ||
		strings.Contains(proc, "playwright-mcp")
}

// IsOnDemandOnlyServer reports whether automatic catalog discovery must avoid
// starting this downstream. Explicit calls and explicit single-server reloads
// can still start it.
func IsOnDemandOnlyServer(s store.DownstreamServer) bool {
	return IsInteractiveAuthServer(s) || IsBrowserAutomationServer(s)
}

// IsAutoStartUnsafeServer reports whether automatic live catalog discovery
// must avoid this downstream. All stdio servers are unsafe for automatic
// discovery because even a tools/list probe starts a local child process.
func IsAutoStartUnsafeServer(s store.DownstreamServer) bool {
	return strings.EqualFold(s.Transport, "stdio")
}

func downstreamProcessText(s store.DownstreamServer) string {
	return strings.ToLower(strings.TrimSpace(s.Command) + " " + string(s.Args))
}
