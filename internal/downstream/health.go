package downstream

import (
	"sync"
	"time"
)

// Retry model: two independent layers.
//
//  1. Instance restart (crash recovery) — restart_policy on the
//     downstream_servers row. When the subprocess exits, shouldRestart
//     decides whether to respawn it. The options are "never", "on-failure"
//     (exit code != 0), or "always". This is a simple bool check with no
//     backoff — the restart happens immediately in a fresh goroutine.
//     See Instance.shouldRestart / monitorProcess in instance.go.
//
//  2. HealthTracker auto-reload (operational stuck detection) — when a
//     running process stops responding (tools/call timeout, tools/list
//     error), the HealthTracker records consecutive failures. Once the
//     threshold (3 in 60s) is tripped, the manager evicts the wedge and
//     the next call lazy-starts a fresh process. Exponential backoff
//     (MinReloadBackoff = 60s, doubles, caps at MaxReloadBackoff = 5m)
//     prevents reload-loop when the upstream is genuinely down. A server
//     that has never served a success is never auto-reloaded (it's
//     mis-configured, not stuck).
//
// The two layers are independent: a crash-handled restart updates neither
// the health tracker nor the backoff. A stuck-detection reload does not
// set restart_policy.

// HealthTracker keeps a small per-server health snapshot used by the
// stuck-detector and the /api/v1/downstreams/{id}/health endpoint.
//
// The state is intentionally tiny — no rows persisted, no SQL writes on
// the hot path. The 24h auto-reload ring is the heaviest field and even
// that's a slice of timestamps bounded to ~5 entries in practice (we
// back off >= 60s between reloads, capped at 5m).
//
// Three classes of failure feed the counter:
//  1. tools/call timeouts (Manager.Call -> ErrCallTimeout)
//  2. tools/list per-server timeouts (ListToolsForServers, TimingTimeout)
//  3. tools/list per-server errors (ListToolsForServers, TimingError)
//
// 401 (ErrAuthRequired) is NOT a failure — Manager already evicts and
// retries with fresh credentials on that path.
type HealthTracker struct {
	mu       sync.Mutex
	byServer map[string]*serverHealth
}

// ServerHealth is the public read-only snapshot returned by Snapshot().
// Lower-case fields stay private; JSON tags on the API DTO live in the
// api handler so storage and wire concerns don't couple.
type ServerHealth struct {
	ServerID            string    `json:"server_id"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	LastFailureAt       time.Time `json:"last_failure_at,omitzero"`
	LastFailureReason   string    `json:"last_failure_reason,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	AutoReloads24h      int       `json:"auto_reloads_24h"`
	LastAutoReloadAt    time.Time `json:"last_auto_reload_at,omitzero"`
	NextReloadEligible  time.Time `json:"next_reload_eligible,omitzero"`
}

type serverHealth struct {
	lastSuccessAt       time.Time
	lastFailureAt       time.Time
	lastFailureReason   string
	consecutiveFailures int
	autoReloads         []time.Time // ring of reload times, pruned to last 24h
	lastReloadAt        time.Time
	// backoff is the current backoff window applied to the NEXT reload
	// attempt. Doubles on each consecutive auto-reload that didn't
	// stabilise the server (i.e. another failure arrived before the
	// window elapsed), caps at maxBackoff.
	backoff time.Duration
}

// Threshold + backoff knobs. Exposed as vars so tests can shrink them
// without sleeping for 60s+ per case. Production code never mutates.
var (
	// StuckThresholdCount + StuckThresholdWindow together define
	// "stuck": N consecutive failures observed within W. Below either
	// dimension, the counter resets on the next success.
	StuckThresholdCount  = 3
	StuckThresholdWindow = 60 * time.Second

	// MinReloadBackoff is the floor between auto-reload attempts for
	// the same server. Stops a reload-loop when the upstream is
	// genuinely down (every fresh instance will hit the same wedge).
	MinReloadBackoff = 60 * time.Second
	MaxReloadBackoff = 5 * time.Minute
)

// NewHealthTracker mints a fresh tracker. The Manager keeps one of
// these for the lifetime of the daemon.
func NewHealthTracker() *HealthTracker {
	return &HealthTracker{byServer: make(map[string]*serverHealth)}
}

// BackoffDelay returns the current backoff duration for the given server.
// Zero means no backoff is active (first reload not yet fired).
// The caller can compare this against MinReloadBackoff / MaxReloadBackoff
// to decide whether to log or surface the backoff state.
func (t *HealthTracker) BackoffDelay(serverID string) time.Duration {
	if serverID == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.byServer[serverID]
	if !ok {
		return 0
	}
	return h.backoff
}

// RecordSuccess marks the server as healthy at `now`, resetting the
// consecutive-failure counter and the failure reason. Backoff is NOT
// reset here — the backoff window persists until enough time has
// elapsed naturally, so an oscillating server doesn't reload faster
// than the cap would allow.
func (t *HealthTracker) RecordSuccess(serverID string, now time.Time) {
	if serverID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(serverID)
	h.lastSuccessAt = now
	h.consecutiveFailures = 0
	h.lastFailureReason = ""
}

// RecordFailure increments the consecutive-failure counter and returns
// (shouldReload, snapshot) — when shouldReload is true, the caller MUST
// invoke the reload path and then call MarkReload to update the ring.
//
// shouldReload is true iff ALL three are met:
//  1. consecutiveFailures >= StuckThresholdCount
//  2. the failure window is within StuckThresholdWindow
//     (oldest failure timestamp in the streak >= now - window)
//  3. now >= lastReloadAt + backoff (or backoff is zero == first reload)
//
// The window check defends against slow-drip flakiness — if three
// failures arrive over an hour we don't auto-reload; we wait for either
// a streak (3 in 60s) or for the operator to notice the flake count.
func (t *HealthTracker) RecordFailure(serverID, reason string, now time.Time) (bool, ServerHealth) {
	if serverID == "" {
		return false, ServerHealth{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(serverID)
	// Window check: if the last failure was outside the window, this
	// streak is "fresh" — reset the counter to 1 rather than continuing.
	if !h.lastFailureAt.IsZero() && now.Sub(h.lastFailureAt) > StuckThresholdWindow {
		h.consecutiveFailures = 0
	}
	h.consecutiveFailures++
	h.lastFailureAt = now
	h.lastFailureReason = reason

	should := h.consecutiveFailures >= StuckThresholdCount
	if should && !h.lastReloadAt.IsZero() {
		if now.Sub(h.lastReloadAt) < h.backoff {
			should = false
		}
	}
	// Auto-reload is a RECOVERY mechanism — it evicts a wedged-but-alive
	// instance so the next call lazy-starts a fresh one. A server that has
	// never served a single successful response this daemon lifetime isn't
	// "stuck", it's mis-configured: disabled, auth-required, a bad command,
	// or a downstream returning 404 / no initialize response. Reloading
	// evicts nothing, can never succeed, and the high-priority
	// "auto-recovered" mesh alert it fires is both false and the dominant
	// flood source (the same dozen broken servers re-alerting every refresh).
	// Suppress until the server has proven it can work at least once.
	if should && h.lastSuccessAt.IsZero() {
		should = false
	}
	return should, t.snapshotLocked(serverID, h, now)
}

// MarkReload records that an auto-reload just fired for the server.
// Bumps the 24h ring, advances the backoff (doubling, capped at
// MaxReloadBackoff), and resets the consecutive-failure counter so the
// next failure starts fresh.
func (t *HealthTracker) MarkReload(serverID string, now time.Time) {
	if serverID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(serverID)
	h.lastReloadAt = now
	h.consecutiveFailures = 0
	// Prune ring to last 24h.
	cutoff := now.Add(-24 * time.Hour)
	pruned := h.autoReloads[:0]
	for _, ts := range h.autoReloads {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	h.autoReloads = append(pruned, now)
	// Advance backoff. First reload uses MinReloadBackoff; subsequent
	// doublings cap at MaxReloadBackoff.
	if h.backoff == 0 {
		h.backoff = MinReloadBackoff
	} else {
		h.backoff *= 2
		if h.backoff > MaxReloadBackoff {
			h.backoff = MaxReloadBackoff
		}
	}
}

// Snapshot returns the current ServerHealth for a single server. Safe
// to call from any goroutine; reads under the same mutex as writes.
func (t *HealthTracker) Snapshot(serverID string, now time.Time) ServerHealth {
	if serverID == "" {
		return ServerHealth{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.byServer[serverID]
	if !ok {
		return ServerHealth{ServerID: serverID}
	}
	return t.snapshotLocked(serverID, h, now)
}

// SnapshotAll returns ServerHealth for every server seen so far.
func (t *HealthTracker) SnapshotAll(now time.Time) []ServerHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ServerHealth, 0, len(t.byServer))
	for id, h := range t.byServer {
		out = append(out, t.snapshotLocked(id, h, now))
	}
	return out
}

// entry returns the existing serverHealth or lazily creates one.
// Caller holds t.mu.
func (t *HealthTracker) entry(serverID string) *serverHealth {
	h, ok := t.byServer[serverID]
	if !ok {
		h = &serverHealth{}
		t.byServer[serverID] = h
	}
	return h
}

// snapshotLocked builds the public snapshot. Counts auto-reloads in the
// last 24h on the fly (cheap — ring is bounded).
// Caller holds t.mu.
func (t *HealthTracker) snapshotLocked(serverID string, h *serverHealth, now time.Time) ServerHealth {
	cutoff := now.Add(-24 * time.Hour)
	count24h := 0
	for _, ts := range h.autoReloads {
		if ts.After(cutoff) {
			count24h++
		}
	}
	next := time.Time{}
	if !h.lastReloadAt.IsZero() && h.backoff > 0 {
		next = h.lastReloadAt.Add(h.backoff)
	}
	return ServerHealth{
		ServerID:            serverID,
		LastSuccessAt:       h.lastSuccessAt,
		LastFailureAt:       h.lastFailureAt,
		LastFailureReason:   h.lastFailureReason,
		ConsecutiveFailures: h.consecutiveFailures,
		AutoReloads24h:      count24h,
		LastAutoReloadAt:    h.lastReloadAt,
		NextReloadEligible:  next,
	}
}
