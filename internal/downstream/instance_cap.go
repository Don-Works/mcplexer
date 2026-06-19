package downstream

import (
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultBrowserMaxInstances bounds how many concurrent per-session browser
// processes a single browser-automation downstream may hold when its config
// row leaves max_instances unset (0). Browser-class servers
// (ShouldIsolatePerSession) key an instance per logical agent, so without a
// cap a busy machine with N live sessions + workers spawns N headless
// Chromium processes per browser server — the "Chrome going crazy" leak. The
// cap keeps isolation (each agent still gets its own browser up to the bound)
// while restoring the structural ceiling that max_instances was meant to give.
//
// Sized at 4: enough for a handful of genuinely-concurrent agents driving
// browsers at once, small enough that idle/abandoned sessions can't pile up
// dozens of Chromium processes. Var (not const) so tests can shadow it.
var DefaultBrowserMaxInstances = 4

// maxInstancesForServer resolves the per-server concurrent-instance ceiling.
// A positive max_instances on the row wins. For browser-class servers that
// leave it unset we fall back to DefaultBrowserMaxInstances so the per-session
// keying can't grow without bound. Non-browser servers are unbounded (0) —
// they share a single instance anyway, so the map never grows per session.
func maxInstancesForServer(srv *store.DownstreamServer) int {
	if srv == nil {
		return 0
	}
	if srv.MaxInstances > 0 {
		return srv.MaxInstances
	}
	if ShouldIsolatePerSession(*srv) {
		return DefaultBrowserMaxInstances
	}
	return 0
}

// enforceInstanceCap evicts the oldest per-session instances of newKey's
// server until starting one more would stay within max. Called with m.mu
// NOT held; it takes the locks it needs. max<=0 means "no cap" (no-op).
//
// Victim selection: only OTHER per-session instances of the same server are
// candidates (never the key we're about to start, never the shared empty-
// session instance, never a different server). Among candidates the oldest by
// start time is evicted first — that's the session least likely to still be
// actively driving its browser. This bounds the process count without
// breaking an in-flight agent's reuse of its own browser.
func (m *Manager) enforceInstanceCap(newKey InstanceKey, max int) {
	if max <= 0 {
		return
	}
	for {
		victim, count, ok := m.pickCapVictim(newKey)
		// count is the number of live instances for this server (including
		// any per-session ones). Room for one more keeps us at or below max.
		if count < max || !ok {
			return
		}
		slog.Info("browser instance cap reached, evicting oldest session instance",
			"server", newKey.ServerID,
			"victim_session", victim.SessionID,
			"live_instances", count,
			"max", max,
		)
		m.evict(victim)
	}
}

// pickCapVictim returns the oldest evictable per-session instance for
// newKey's server and the current live instance count for that server. ok is
// false when there is no evictable victim (e.g. the only instances are the
// shared one or the key we're about to start).
func (m *Manager) pickCapVictim(newKey InstanceKey) (victim InstanceKey, count int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var oldest time.Time
	for key := range m.instances {
		if key.ServerID != newKey.ServerID {
			continue
		}
		count++
		// Never evict the instance we're starting, the shared (empty-
		// session) instance, or anything for a different server.
		if key == newKey || key.SessionID == "" {
			continue
		}
		started := m.instanceStartedAt[key]
		if !ok || started.Before(oldest) {
			oldest = started
			victim = key
			ok = true
		}
	}
	return victim, count, ok
}
