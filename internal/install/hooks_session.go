package install

import (
	"fmt"
	"net/url"
	"strings"
)

// pretoolPathSuffix / sessionPathSuffix are the path tails the PreToolUse
// and session-lifecycle hooks post to. sessionEndpoint derives the latter
// from the configured pretool endpoint by swapping the suffix, so a custom
// daemon host:port (passed to NewHookInstaller by serve.go) is preserved
// for both hooks without the caller having to plumb a second URL.
const (
	pretoolPathSuffix = "/v1/hooks/pretool"
	sessionPathSuffix = "/v1/hooks/session"
)

// claudeSessionEvents are the Claude Code hook event keys that map to a
// session lifecycle: SessionStart fires once when a session begins;
// SessionEnd fires once when the session actually ends. The gateway's
// /v1/hooks/session handler bridges both into the memory contract (recall
// digest at start, capture nudge at end). Unlike PreToolUse these are NOT
// matched against a tool name, so each entry omits a "matcher" key.
//
// "Stop" is deliberately NOT registered: Claude Code fires Stop after EVERY
// assistant turn, not at session end, so registering it would inject the
// heavyweight "CAPTURE BEFORE ENDING" nudge on every turn. Only SessionStart
// + SessionEnd map to a true session boundary. (Defence in depth: the
// gateway handler also no-ops a Stop event even if one is delivered.)
var claudeSessionEvents = []string{"SessionStart", "SessionEnd"}

// sessionEndpoint returns the URL the session-lifecycle hooks post to. It
// is derived from h.endpoint (the PreToolUse URL) by swapping the path tail
// so a custom host:port is preserved. The common case (h.endpoint ends in
// the pretool suffix) is a direct suffix swap. Otherwise we parse the URL
// with net/url and replace ONLY the Path with the session suffix — this
// preserves scheme/host/port, never produces a double slash, and never
// silently keeps a stale query string. A parse failure (or a non-URL
// endpoint) falls back to the old scheme+host heuristic.
func (h *HookInstaller) sessionEndpoint() string {
	if strings.HasSuffix(h.endpoint, pretoolPathSuffix) {
		return h.endpoint[:len(h.endpoint)-len(pretoolPathSuffix)] + sessionPathSuffix
	}
	if u, err := url.Parse(h.endpoint); err == nil && u.Host != "" {
		// Replace the whole path with the session suffix; drop any query so a
		// non-standard endpoint can't leak a stale ?foo into the session URL.
		u.Path = sessionPathSuffix
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	// Last-resort fallback: strip any /v1/... tail then append the session
	// suffix, TrimRight'ing a trailing '/' so we never double-slash.
	base := h.endpoint
	if idx := strings.Index(base, "/v1/"); idx >= 0 {
		base = base[:idx]
	}
	return strings.TrimRight(base, "/") + sessionPathSuffix
}

// sessionHookCommand renders the curl invocation Claude Code execs for each
// session lifecycle event. Identical shape to hookCommand() (same stdin
// payload streaming, same graceful-degrade-on-daemon-down behaviour), only
// the endpoint differs — so a re-install upgrades both hooks consistently.
func (h *HookInstaller) sessionHookCommand() string {
	return fmt.Sprintf(
		`curl -s -X POST -H 'Content-Type: application/json' --data-binary @- %s`,
		h.sessionEndpoint(),
	)
}
