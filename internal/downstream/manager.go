package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/auth"
	"github.com/don-works/mcplexer/internal/sandbox"
	"github.com/don-works/mcplexer/internal/store"
)

// downstream is the common interface for stdio and HTTP MCP instances.
type downstream interface {
	start(ctx context.Context) error
	stop()
	ListTools(ctx context.Context) (json.RawMessage, error)
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)
	getState() InstanceState
	waitRestartDone() <-chan struct{}
}

// Manager orchestrates downstream MCP server process lifecycles.
type Manager struct {
	store     store.Store
	auth      *auth.Injector
	mu        sync.Mutex
	instances map[InstanceKey]downstream

	// instanceStartedAt records when each tracked instance was inserted into
	// the map, guarded by mu alongside instances. Used only by the per-server
	// instance cap (enforceInstanceCap) to evict the oldest per-session
	// browser when a busy machine would otherwise spawn unbounded Chromium
	// processes. Entries are removed in lock-step with instances on
	// evict/release/shutdown.
	instanceStartedAt map[InstanceKey]time.Time

	startFlightMu sync.Mutex
	startFlight   map[InstanceKey]*startCall

	keyMu      sync.Mutex
	keyMutexes map[InstanceKey]*sync.Mutex

	// sandboxWrapper, when non-nil and Enabled(), routes every
	// downstream spawn through the host's sandbox driver
	// (sandbox-exec on darwin). Settable post-construction via
	// SetSandboxWrapper so serve.go can flip it based on settings
	// without restarting the daemon.
	sandboxMu      sync.RWMutex
	sandboxWrapper *sandbox.CommandWrapper

	// internalMu guards internals. Registered once at startup; lookups are
	// on every tool call.
	internalMu sync.RWMutex
	internals  map[string]InternalBackend

	// toolsChangedMu guards toolsChangedSubs. Subscribers register from
	// gateway sessions (stdio + one per socket connection) and from the
	// HTTP discover endpoint — each live session wants a
	// notifications/tools/list_changed when tools actually change.
	toolsChangedMu   sync.Mutex
	toolsChangedSubs []*func()

	// timingsMu guards the latest-per-server timing snapshot. Updated on
	// every ListToolsForServers iteration and read by the dashboard /
	// telemetry endpoint to surface slow downstreams without grepping
	// daemon logs.
	timingsMu     sync.RWMutex
	latestTimings map[string]ServerTiming

	// health tracks per-server consecutive-failure state for the stuck-
	// detector + auto-reload path. Populated by Manager.Call (timeout
	// errors) and Manager.ListToolsForServers (per-server timeouts and
	// errors). The /api/v1/downstreams/{id}/health endpoint reads off
	// this same tracker for observability.
	health *HealthTracker

	// onAutoReload, if set, is invoked once per auto-reload firing with
	// the server ID and the health snapshot that triggered it. Wired by
	// serve.go to emit an audit row + a mesh alert. Kept as a hook so
	// the manager doesn't need to import audit/mesh directly (which
	// would create dependency cycles via NewManager's signature).
	onAutoReloadMu sync.RWMutex
	onAutoReload   func(serverID string, snap ServerHealth)

	// autoReloadInFlight de-dupes concurrent auto-reload attempts for
	// the same server when many goroutines record failures at once.
	autoReloadMu       sync.Mutex
	autoReloadInFlight map[string]bool

	// eventJournals records bounded downstream notification history per
	// instance key so agents can poll deltas without streaming Code Mode
	// tool results.
	eventJournals *journalRegistry

	// onAuthInvalidated clears an OAuth token after the downstream has
	// definitively rejected it. serve.go wires this to FlowManager.RevokeToken
	// so token-change hooks still run (mesh auth sync, dashboard refresh).
	onAuthInvalidatedMu sync.RWMutex
	onAuthInvalidated   func(context.Context, string) error
}

// NewManager creates a new downstream process manager.
func NewManager(s store.Store, authInj *auth.Injector) *Manager {
	return &Manager{
		store:              s,
		auth:               authInj,
		instances:          make(map[InstanceKey]downstream),
		instanceStartedAt:  make(map[InstanceKey]time.Time),
		startFlight:        make(map[InstanceKey]*startCall),
		latestTimings:      make(map[string]ServerTiming),
		health:             NewHealthTracker(),
		autoReloadInFlight: make(map[string]bool),
		keyMutexes:         make(map[InstanceKey]*sync.Mutex),
		eventJournals:      newJournalRegistry(),
	}
}

// Health returns the per-server health tracker. Used by the
// /api/v1/downstreams/{id}/health endpoint to surface stuck-detector
// state without grepping daemon logs.
func (m *Manager) Health() *HealthTracker {
	return m.health
}

// SetAutoReloadHook installs a callback invoked once per auto-reload
// firing. serve.go uses this to emit an audit row + a mesh alert
// without coupling Manager to those packages directly. Pass nil to
// disable.
func (m *Manager) SetAutoReloadHook(fn func(serverID string, snap ServerHealth)) {
	m.onAutoReloadMu.Lock()
	m.onAutoReload = fn
	m.onAutoReloadMu.Unlock()
}

func (m *Manager) currentAutoReloadHook() func(string, ServerHealth) {
	m.onAutoReloadMu.RLock()
	defer m.onAutoReloadMu.RUnlock()
	return m.onAutoReload
}

// SetAuthInvalidationHook installs the callback used when a downstream keeps
// returning auth-required after the normal one-shot restart. Passing nil
// falls back to directly clearing auth_scopes.oauth_token_data.
func (m *Manager) SetAuthInvalidationHook(fn func(context.Context, string) error) {
	m.onAuthInvalidatedMu.Lock()
	m.onAuthInvalidated = fn
	m.onAuthInvalidatedMu.Unlock()
}

func (m *Manager) currentAuthInvalidationHook() func(context.Context, string) error {
	m.onAuthInvalidatedMu.RLock()
	defer m.onAuthInvalidatedMu.RUnlock()
	return m.onAuthInvalidated
}

// SetSandboxWrapper installs the per-spawn sandbox wrapper. Pass nil
// to disable; pass a wrapper whose Enabled() is false to keep the
// codepath wired but produce identity-transform exec.Cmds. Callers
// rebuild the wrapper after a settings change and call this again —
// in-flight Instances keep their original wrapper (cleanup deferred
// to their own stop()), and the next Start picks up the new one.
func (m *Manager) SetSandboxWrapper(w *sandbox.CommandWrapper) {
	m.sandboxMu.Lock()
	m.sandboxWrapper = w
	m.sandboxMu.Unlock()
}

// SandboxWrapper returns the currently installed wrapper, or nil. The
// dashboard hits this through guards_handler to render the active
// sandbox description.
func (m *Manager) SandboxWrapper() *sandbox.CommandWrapper {
	return m.currentSandboxWrapper()
}

func (m *Manager) currentSandboxWrapper() *sandbox.CommandWrapper {
	m.sandboxMu.RLock()
	defer m.sandboxMu.RUnlock()
	return m.sandboxWrapper
}

// SubscribeToolsChanged registers a callback to invoke whenever the tool
// surface changes (downstream emitted notifications/tools/list_changed, or
// someone called NotifyToolsChanged manually after discover / config edit).
// Returns an unsubscribe func — callers MUST invoke it on session close so
// closed sessions don't keep firing.
func (m *Manager) SubscribeToolsChanged(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	m.toolsChangedMu.Lock()
	handle := &fn
	m.toolsChangedSubs = append(m.toolsChangedSubs, handle)
	m.toolsChangedMu.Unlock()
	return func() {
		m.toolsChangedMu.Lock()
		defer m.toolsChangedMu.Unlock()
		for i, sub := range m.toolsChangedSubs {
			if sub == handle {
				m.toolsChangedSubs = append(m.toolsChangedSubs[:i], m.toolsChangedSubs[i+1:]...)
				return
			}
		}
	}
}

// NotifyToolsChanged fans out to every subscriber. Each callback runs in its
// own goroutine so one slow/blocked session can't stall the rest.
func (m *Manager) NotifyToolsChanged() {
	m.toolsChangedMu.Lock()
	subs := make([]*func(), len(m.toolsChangedSubs))
	copy(subs, m.toolsChangedSubs)
	m.toolsChangedMu.Unlock()
	for _, sub := range subs {
		fn := *sub
		go fn()
	}
}

// Call dispatches a tool call to the appropriate downstream instance.
// It lazy-starts the process if not already running. Internal-transport
// servers are handled by their registered InternalBackend.
//
// On ErrAuthRequired (HTTP 401), the instance is evicted and restarted once
// so that fresh credentials are resolved from the DB/secrets store — this
// allows bearer tokens to be hot-reloaded without a full mcplexer restart.
func (m *Manager) Call(
	ctx context.Context,
	serverID, authScopeID, toolName string,
	args json.RawMessage,
) (json.RawMessage, error) {
	// Resolve `secret://<key>` references in args against this call's
	// auth scope before either dispatch branch. Substitution happens
	// AFTER the audit row has already been built upstream
	// (handler_tools.go records req.Arguments before invoking us), so
	// the audit log never sees plaintext — only the `secret://name`
	// placeholder the agent submitted. Cache keys upstream are also
	// computed pre-substitution, so plaintext never enters the cache
	// map either.
	args, err := substituteSecretRefs(ctx, args, m.secretLookupFor(authScopeID))
	if err != nil {
		return nil, fmt.Errorf("resolve secret refs: %w", err)
	}

	srv, err := m.store.GetDownstreamServer(ctx, serverID)
	if err == nil && srv.Transport == "internal" {
		backend := m.internalFor(serverID)
		if backend == nil {
			return nil, errNoInternalBackend
		}
		return backend.Call(ctx, toolName, args)
	}

	// Per-call dispatch deadline. Without this, a wedged HTTP/2 stream
	// or a stdio downstream that accepted the request but never wrote a
	// response would block the client indefinitely (today's incident
	// scenario: Linear MCP wedged mid-stream, agent calls hung forever).
	// Honors any tighter deadline already on ctx — context.WithTimeout
	// is a no-op when ctx's deadline is already earlier.
	timeout := callTimeoutFor(srv)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	key := m.instanceKeyFor(ctx, srv, serverID, authScopeID)

	inst, err := m.getOrStart(callCtx, key)
	if err != nil {
		if isAuthError(err) {
			m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
		}
		return nil, fmt.Errorf("get or start instance: %w", err)
	}

	params, err := json.Marshal(map[string]any{
		"name":      toolName,
		"arguments": json.RawMessage(args),
		"_meta": map[string]any{
			"progressToken": fmt.Sprintf("mcpx-%d", time.Now().UnixNano()),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal call params: %w", err)
	}

	result, callErr := inst.Call(callCtx, "tools/call", json.RawMessage(params))
	if callErr != nil && isAuthError(callErr) {
		// Bearer may have been rotated on the downstream side. Evict the stale
		// instance so the next getOrStart re-resolves credentials from the DB.
		slog.Info("downstream returned 401, evicting instance for token refresh",
			"server", serverID, "scope", authScopeID)
		m.evict(key)
		inst, err = m.getOrStart(callCtx, key)
		if err != nil {
			if isAuthError(err) {
				m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
			}
			return nil, fmt.Errorf("restart after 401: %w", err)
		}
		result, callErr = inst.Call(callCtx, "tools/call", json.RawMessage(params))
	}
	if callErr != nil && isAuthError(callErr) {
		m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
		m.recordCallFailure(serverID, "auth required")
		return result, callErr
	}
	if callErr != nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		// Per-call deadline path. Record on the health tracker — if
		// this is the Nth consecutive failure within the window, fire
		// the auto-reload off a goroutine (don't block the client).
		m.recordCallFailure(serverID, "call timeout")
		// Evict the timed-out instance. For a stdio downstream the
		// in-flight request is still parked in processLoop blocked on
		// scanner.Scan() with no deadline wired to it. If we leave the
		// instance in the map, its late response would be consumed by
		// the orphaned readResponse into a receiver-gone channel, and
		// every SUBSEQUENT caller on this instance would then read a
		// response belonging to a different request — a cross-call /
		// cross-scope data leak. A timed-out stream is desynced and
		// must not be reused, mirroring the 401 eviction path above.
		m.evict(key)
		// Map the raw timeout to a stable, human-readable sentinel
		// downstream of the caller so formatDownstreamError can hint
		// at "server may be wedged" rather than leaking transport noise.
		return nil, fmt.Errorf("call timed out after %s: %w", timeout, ErrCallTimeout)
	}
	if callErr != nil && errors.Is(callErr, ErrResponseDesync) {
		// The stdio stream drifted out of lock-step (a stale response
		// surfaced). Evict so the next call lazy-starts a fresh process
		// rather than reusing a stream that will keep answering callers
		// with the wrong request's result.
		m.recordCallFailure(serverID, "response desync")
		m.evict(key)
		return nil, callErr
	}
	if callErr != nil {
		// Non-401, non-timeout error — still counts as a failure for
		// stuck-detection purposes (transport error, downstream
		// returned an RPC error, etc). 401 path retries above and
		// reaches here only on the second attempt also failing.
		m.recordCallFailure(serverID, callErr.Error())
		return result, callErr
	}
	m.health.RecordSuccess(serverID, time.Now())
	return result, nil
}

// recordCallFailure marks a failure on the health tracker and, if the
// stuck-threshold tripped, kicks off the auto-reload goroutine. Idempotent
// across concurrent failures for the same server — autoReloadInFlight
// gates against parallel reloads.
func (m *Manager) recordCallFailure(serverID, reason string) {
	if m.health == nil || serverID == "" {
		return
	}
	should, snap := m.health.RecordFailure(serverID, reason, time.Now())
	if !should {
		return
	}
	m.scheduleAutoReload(serverID, snap)
}

func (m *Manager) invalidateOAuthTokenAfterAuthFailure(
	ctx context.Context, serverID, authScopeID string,
) {
	if authScopeID == "" || m.store == nil {
		return
	}
	invalidCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	scope, err := m.store.GetAuthScope(invalidCtx, authScopeID)
	if err != nil {
		slog.Warn("failed to inspect auth scope after downstream auth failure",
			"server", serverID, "scope", authScopeID, "error", err)
		return
	}
	if scope.Type != "oauth2" || len(scope.OAuthTokenData) == 0 {
		return
	}

	if hook := m.currentAuthInvalidationHook(); hook != nil {
		err = hook(invalidCtx, authScopeID)
	} else {
		err = m.store.UpdateAuthScopeTokenData(invalidCtx, authScopeID, nil)
	}
	if err != nil {
		slog.Warn("failed to invalidate OAuth token after downstream auth failure",
			"server", serverID, "scope", authScopeID, "error", err)
		return
	}
	slog.Warn("invalidated OAuth token after downstream auth failure",
		"server", serverID, "scope", authScopeID)
}

// scheduleAutoReload fires the reload path in a fresh goroutine so the
// caller (a failing client call) returns immediately. Only one
// in-flight reload per server — concurrent callers find the flag and
// return without duplicating work.
func (m *Manager) scheduleAutoReload(serverID string, snap ServerHealth) {
	m.autoReloadMu.Lock()
	if m.autoReloadInFlight[serverID] {
		m.autoReloadMu.Unlock()
		return
	}
	m.autoReloadInFlight[serverID] = true
	m.autoReloadMu.Unlock()

	go func() {
		defer func() {
			m.autoReloadMu.Lock()
			delete(m.autoReloadInFlight, serverID)
			m.autoReloadMu.Unlock()
		}()
		m.performAutoReload(serverID, snap)
	}()
}

// performAutoReload evicts every running instance of the server (any
// auth scope) so the next client call lazy-starts a fresh process /
// HTTP connection pool. Records the reload on the health tracker
// (which also resets the consecutive-failure counter and advances
// backoff) and fires the audit/mesh hook if installed.
func (m *Manager) performAutoReload(serverID string, snap ServerHealth) {
	slog.Warn("auto-reloading stuck downstream",
		"server_id", serverID,
		"consecutive_failures", snap.ConsecutiveFailures,
		"last_failure_reason", snap.LastFailureReason,
	)

	m.ReloadServerInstances(serverID)

	m.health.MarkReload(serverID, time.Now())

	// Tell the operator. Hook fires synchronously inside the reload
	// goroutine so the audit row + mesh alert are guaranteed to land
	// before another failure could trip a second reload.
	if hook := m.currentAutoReloadHook(); hook != nil {
		// The post-MarkReload snapshot carries the fresh 24h reload count +
		// lastReloadAt, but MarkReload just reset the consecutive-failure
		// counter — so re-attach the failing snapshot's count/reason that
		// actually triggered this reload. Without this the alert always
		// reads "after 0 consecutive failures", which is what tipped off the
		// agent that something was off.
		postSnap := m.health.Snapshot(serverID, time.Now())
		postSnap.ConsecutiveFailures = snap.ConsecutiveFailures
		postSnap.LastFailureReason = snap.LastFailureReason
		postSnap.LastFailureAt = snap.LastFailureAt
		hook(serverID, postSnap)
	}

	// Surface the catalog change to live sessions so they re-pull
	// tools/list if they cache descriptions client-side.
	m.NotifyToolsChanged()
}

// ReloadServerInstances evicts every running instance for a downstream server,
// across auth scopes and per-session keys. The next ListTools/Call lazy-starts
// a fresh process or HTTP MCP session from the latest server configuration.
func (m *Manager) ReloadServerInstances(serverID string) int {
	if serverID == "" {
		return 0
	}

	m.mu.Lock()
	keys := make([]InstanceKey, 0, 2)
	for key := range m.instances {
		if key.ServerID == serverID {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()

	for _, key := range keys {
		m.evict(key)
	}
	if len(keys) > 0 {
		slog.Info("evicted downstream instances for reload",
			"server_id", serverID,
			"count", len(keys),
		)
	}
	return len(keys)
}

// ErrCallTimeout is returned (wrapped) when Manager.Call's per-server
// deadline fires before the downstream produced a response. Callers can
// errors.Is against it to detect the auto-cancel path vs. an upstream
// rpc error.
var ErrCallTimeout = errors.New("downstream call deadline exceeded")

func (m *Manager) evict(key InstanceKey) {
	keyLock := m.lockForKey(key)
	keyLock.Lock()
	defer keyLock.Unlock()

	m.mu.Lock()
	inst, ok := m.instances[key]
	if ok {
		delete(m.instances, key)
		delete(m.instanceStartedAt, key)
	}
	m.mu.Unlock()
	if ok {
		inst.stop()
	}
}

// instanceKeyFor builds the lookup key for a dispatch. Browser-automation
// downstreams (ShouldIsolatePerSession) are keyed additionally by the
// per-agent isolation id carried on ctx, so each logical agent gets its own
// stateful browser process. Every other server keys by (ServerID,
// AuthScopeID) only — the shared single-instance lifecycle, unchanged.
//
// srv may be nil when the server row failed to load; we degrade to the
// shared key in that case (createInstance will surface the real error).
func (m *Manager) instanceKeyFor(
	ctx context.Context, srv *store.DownstreamServer, serverID, authScopeID string,
) InstanceKey {
	key := InstanceKey{ServerID: serverID, AuthScopeID: authScopeID}
	if srv != nil && ShouldIsolatePerSession(*srv) {
		key.SessionID = BrowserSessionIDFromContext(ctx)
	}
	return key
}

// ReleaseSession stops and removes every per-session instance owned by the
// given isolation id. Wired to the gateway's session-disconnect path so an
// agent's dedicated browser process dies promptly when it goes away, instead
// of lingering until the idle timer reaps it. No-op for the empty id (the
// shared lifecycle owns no per-session instances). Safe to call for sessions
// that never spawned a browser — the scan simply matches nothing.
func (m *Manager) ReleaseSession(sessionID string) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	var toStop []downstream
	var keys []InstanceKey
	for key, inst := range m.instances {
		if key.SessionID == sessionID {
			toStop = append(toStop, inst)
			keys = append(keys, key)
			delete(m.instances, key)
			delete(m.instanceStartedAt, key)
		}
	}
	m.mu.Unlock()

	for _, inst := range toStop {
		inst.stop()
	}
	if len(keys) > 0 {
		m.keyMu.Lock()
		for _, k := range keys {
			delete(m.keyMutexes, k)
		}
		m.keyMu.Unlock()
		m.eventJournals.dropForSession(sessionID)
		slog.Info("released per-session downstream instances",
			"session", sessionID, "count", len(toStop))
	}
}

// secretLookupFor returns a closure that resolves a single ref key against
// the given auth scope. When the scope is empty (server has no auth_scope
// configured) any reference produces ErrSecretRefNoScope — we'd rather
// fail loudly than silently leak the literal "secret://name" string into
// the downstream call.
func (m *Manager) secretLookupFor(authScopeID string) secretLookup {
	return func(ctx context.Context, key string) ([]byte, error) {
		if authScopeID == "" {
			return nil, ErrSecretRefNoScope
		}
		if m.auth == nil {
			return nil, fmt.Errorf("no auth injector configured")
		}
		return m.auth.GetSecret(ctx, authScopeID, key)
	}
}

// isAuthError returns true when the error indicates a downstream server
// rejected the request with an HTTP 401 (authorization required).
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrAuthRequired)
}

func (m *Manager) createInstance(
	ctx context.Context, key InstanceKey,
) (downstream, error) {
	server, err := m.store.GetDownstreamServer(ctx, key.ServerID)
	if err != nil {
		return nil, fmt.Errorf("get server %s: %w", key.ServerID, err)
	}

	if server.Disabled {
		return nil, fmt.Errorf("downstream server %q is disabled", server.Name)
	}

	timeout := time.Duration(server.IdleTimeoutSec) * time.Second

	if server.Transport == "http" && server.URL != nil {
		var headers http.Header
		var applyAuth AuthRequestFunc
		if m.auth != nil && key.AuthScopeID != "" {
			applyAuth = m.auth.ApplyToRequest
		}
		hInst := newHTTPInstance(key, *server.URL, timeout, headers, applyAuth)
		hInst.onNotify = func(method string, params json.RawMessage) {
			m.handleDownstreamNotify(key, method, params)
		}
		return hInst, nil
	}

	// Default: stdio transport
	var cmdArgs []string
	if len(server.Args) > 0 {
		if err := json.Unmarshal(server.Args, &cmdArgs); err != nil {
			return nil, fmt.Errorf("unmarshal args: %w", err)
		}
	}

	var authEnv map[string]string
	if m.auth != nil {
		var err error
		authEnv, err = m.auth.EnvForDownstream(ctx, key.AuthScopeID)
		if err != nil {
			slog.Warn("failed to resolve auth env",
				"scope", key.AuthScopeID, "error", err)
		}
	}
	env := MergeEnv(os.Environ(), nil, authEnv)

	restartPolicy := server.RestartPolicy
	if restartPolicy == "" {
		restartPolicy = "on-failure" // migration default
	}

	inst := newInstance(key, server.Command, cmdArgs, env, timeout, m.currentSandboxWrapper(), restartPolicy)
	inst.onNotify = func(method string, params json.RawMessage) {
		m.handleDownstreamNotify(key, method, params)
	}
	return inst, nil
}

// ListTools sends a tools/list request to a specific downstream instance.
// Internal-transport servers have no process; return an empty tool list.
//
// On ErrAuthRequired (HTTP 401), the instance is evicted and restarted once
// so that fresh credentials are resolved from the DB/secrets store.
func (m *Manager) ListTools(
	ctx context.Context, serverID, authScopeID string,
) (json.RawMessage, error) {
	srv, err := m.store.GetDownstreamServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server %s: %w", serverID, err)
	}
	if srv.Transport == "internal" {
		if backend := m.internalFor(serverID); backend != nil {
			return backend.ListTools(ctx)
		}
		return json.RawMessage(`{"tools":[]}`), nil
	}

	key := InstanceKey{ServerID: serverID, AuthScopeID: authScopeID}

	inst, err := m.getOrStart(ctx, key)
	if err != nil {
		if isAuthError(err) {
			m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
		}
		return nil, fmt.Errorf("get or start instance: %w", err)
	}

	result, listErr := inst.ListTools(ctx)
	if listErr != nil && isAuthError(listErr) {
		slog.Info("downstream returned 401 on tools/list, evicting instance for token refresh",
			"server", serverID, "scope", authScopeID)
		m.evict(key)
		inst, err = m.getOrStart(ctx, key)
		if err != nil {
			if isAuthError(err) {
				m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
			}
			return nil, fmt.Errorf("restart after 401: %w", err)
		}
		result, listErr = inst.ListTools(ctx)
	}
	if listErr != nil && isAuthError(listErr) {
		m.invalidateOAuthTokenAfterAuthFailure(ctx, serverID, authScopeID)
	}
	return result, listErr
}

// ListAllTools queries all downstream servers for their tools in parallel.
// Returns a map of serverID -> raw tools/list result JSON.
func (m *Manager) ListAllTools(ctx context.Context) (map[string]json.RawMessage, error) {
	servers, err := m.store.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downstream servers: %w", err)
	}
	// Skip servers that must not be auto-started. Disabled servers have no
	// catalog to contribute; stdio servers spawn local child processes even
	// for tools/list. Explicit ListTools calls still work.
	ids := make([]string, 0, len(servers))
	for _, srv := range servers {
		if srv.Disabled || IsAutoStartUnsafeServer(srv) {
			continue
		}
		ids = append(ids, srv.ID)
	}
	return m.ListToolsForServers(ctx, ids)
}

// PerServerListToolsTimeout bounds each downstream's contribution to a
// parallel tools/list aggregation. Without this, one slow or hung server
// (e.g. a remote MCP server stuck in interactive OAuth) blocks the entire
// group past the MCP client's tools/list timeout (~30s for opencode), so
// the client times out and reports the whole gateway as failed.
//
// Sized at 15s: comfortably covers cold-starts of npx-fetched stdio
// servers (Playwright's `npx -y @playwright/mcp@latest` cold-start
// regularly takes 8-12s on a quiet box — package resolve + chromium
// binary check + MCP initialize). Stays well under the 30s upstream
// client ceiling so a single slow downstream can't sink the catalog.
//
// Exposed as `var` (not `const`) so tests can shadow it with a small
// value to keep test runs fast. Production code never mutates it.
var PerServerListToolsTimeout = 15 * time.Second

// DefaultCallTimeout bounds a single tools/call dispatch when the
// downstream_servers row's per-server call_timeout_sec is zero (the
// migration default).
//
// Sized at 120s: comfortably above the ~30s upstream MCP client ceiling
// — covers slow downstream operations like LLM-backed tools, large
// Playwright actions, GitHub repo crawls — but short enough that a
// wedged HTTP/2 stream (today's incident: Linear MCP got stuck
// mid-response) recovers within one agent turn instead of indefinitely.
//
// Var (not const) so tests can shadow it cheaply.
var DefaultCallTimeout = 120 * time.Second

// callTimeoutFor resolves the per-call deadline for a downstream server
// row, falling back to DefaultCallTimeout when the column is unset.
func callTimeoutFor(server *store.DownstreamServer) time.Duration {
	if server == nil || server.CallTimeoutSec <= 0 {
		return DefaultCallTimeout
	}
	return time.Duration(server.CallTimeoutSec) * time.Second
}

// slowListToolsThreshold flags any single-server tools/list that takes
// longer than this. It's not a failure — anything under PerServer-
// ListToolsTimeout still returns successfully — but it's the signal we
// use to spot servers that are heading towards timeout territory.
const slowListToolsThreshold = 3 * time.Second

// ListToolsForServers queries specific downstream servers for their tools in parallel.
//
// Each server gets its own bounded context (PerServerListToolsTimeout) so a
// slow or hung downstream cannot stall the aggregation. Servers that exceed
// the timeout or fail are logged and omitted from the result; the call
// always returns whatever cataloged successfully within the window.
func (m *Manager) ListToolsForServers(ctx context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	// Resolve auth scopes for each server from route rules so that
	// HTTP downstreams get proper Authorization headers during discovery.
	scopeByServer := m.resolveAuthScopes(ctx, serverIDs)

	// Resolve friendly names so log lines are grep-able by human name,
	// not just the UUID. Single batch query keeps this cheap on the
	// hot path. Missing rows fall back to the ID.
	nameByServer := m.resolveServerNames(ctx, serverIDs)

	// Track per-server timings for telemetry consumers (dashboard,
	// /api/v1/downstreams/timings). One write per server per refresh,
	// guarded by the timings mutex; no impact on the goroutine
	// hot-path beyond a single map lookup + lock acquire.
	recordTiming := func(id string, status TimingStatus, elapsed time.Duration) {
		m.recordListToolsTiming(id, status, elapsed, nameByServer[id])
	}

	var mu sync.Mutex
	result := make(map[string]json.RawMessage, len(serverIDs))

	var wg sync.WaitGroup
	for _, id := range serverIDs {
		authScope := scopeByServer[id]
		wg.Add(1)
		go func() {
			defer wg.Done()

			cctx, cancel := context.WithTimeout(ctx, PerServerListToolsTimeout)
			defer cancel()

			started := time.Now()
			tools, err := m.ListTools(cctx, id, authScope)
			elapsed := time.Since(started)
			name := nameByServer[id]
			if name == "" {
				name = id
			}

			if err != nil {
				if errors.Is(cctx.Err(), context.DeadlineExceeded) {
					recordTiming(id, TimingTimeout, elapsed)
					m.recordCallFailure(id, "list tools timeout")
					slog.Warn("list tools per-server timeout exceeded; skipping",
						"server", name,
						"server_id", id,
						"timeout", PerServerListToolsTimeout,
						"elapsed_ms", elapsed.Milliseconds(),
					)
				} else {
					recordTiming(id, TimingError, elapsed)
					m.recordCallFailure(id, err.Error())
					slog.Warn("failed to list tools",
						"server", name,
						"server_id", id,
						"error", err,
						"elapsed_ms", elapsed.Milliseconds(),
					)
				}
				return
			}
			// Successful tools/list also resets the stuck-counter —
			// many wedges hit BOTH the discovery path and the dispatch
			// path, and the cache refresh is the first signal to come
			// back when the upstream recovers.
			m.health.RecordSuccess(id, time.Now())

			// Slowness audit: anything taking >3s while still under the
			// timeout is worth flagging. These are the candidates for
			// per-server config overrides (longer timeout, or eviction
			// from the catalog if they're chronically slow).
			if elapsed > slowListToolsThreshold {
				recordTiming(id, TimingSlow, elapsed)
				slog.Warn("list tools slow",
					"server", name,
					"server_id", id,
					"elapsed_ms", elapsed.Milliseconds(),
					"threshold_ms", slowListToolsThreshold.Milliseconds(),
				)
			} else {
				recordTiming(id, TimingOK, elapsed)
				slog.Debug("list tools ok",
					"server", name,
					"server_id", id,
					"elapsed_ms", elapsed.Milliseconds(),
				)
			}

			mu.Lock()
			result[id] = tools
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Honor parent cancellation: if the caller's context is gone, surface that.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// resolveServerNames returns a map of server ID → friendly name. Missing
// rows are omitted; callers should fall back to the ID itself when the
// map has no entry. Errors are swallowed: the names are only used for
// log readability, never for correctness.
func (m *Manager) resolveServerNames(ctx context.Context, serverIDs []string) map[string]string {
	out := make(map[string]string, len(serverIDs))
	servers, err := m.store.GetDownstreamServersByIDs(ctx, serverIDs)
	if err != nil {
		return out
	}
	for i := range servers {
		if servers[i].Name != "" {
			out[servers[i].ID] = servers[i].Name
		}
	}
	return out
}

// resolveAuthScopes finds an auth scope for each server by scanning route rules.
func (m *Manager) resolveAuthScopes(ctx context.Context, serverIDs []string) map[string]string {
	result := make(map[string]string, len(serverIDs))
	rules, err := m.store.ListRouteRules(ctx, "")
	if err != nil {
		return result
	}
	need := make(map[string]bool, len(serverIDs))
	for _, id := range serverIDs {
		need[id] = true
	}
	for _, rule := range rules {
		if !need[rule.DownstreamServerID] || rule.AuthScopeID == "" {
			continue
		}
		if _, ok := result[rule.DownstreamServerID]; !ok {
			result[rule.DownstreamServerID] = rule.AuthScopeID
		}
	}
	return result
}

// EnsureRunning pre-warms a downstream server instance by lazy-starting it.
// This is a best-effort operation — errors are logged but not returned.
func (m *Manager) EnsureRunning(ctx context.Context, serverID, authScopeID string) {
	key := InstanceKey{ServerID: serverID, AuthScopeID: authScopeID}
	if _, err := m.getOrStart(ctx, key); err != nil {
		slog.Debug("prefetch: failed to pre-warm downstream",
			"server", serverID, "error", err)
	}
}

// EventsSince returns downstream notifications recorded after sinceSeq for one
// instance stream.
func (m *Manager) EventsSince(
	key InstanceKey, sinceSeq int64, limit int, methods []string,
) EventStreamState {
	if m == nil || m.eventJournals == nil {
		return EventStreamState{ServerID: key.ServerID, AuthScopeID: key.AuthScopeID, SinceSeq: sinceSeq}
	}
	return m.eventJournals.since(key, sinceSeq, limit, normalizeMethodFilter(methods))
}

// WaitForEvents blocks until new downstream notifications arrive after
// sinceSeq or the timeout elapses. timedOut is true when no matching events
// were found before the timeout.
func (m *Manager) WaitForEvents(
	ctx context.Context, key InstanceKey, sinceSeq int64, timeout time.Duration,
	limit int, methods []string,
) (EventStreamState, bool) {
	if m == nil || m.eventJournals == nil {
		return EventStreamState{ServerID: key.ServerID, AuthScopeID: key.AuthScopeID, SinceSeq: sinceSeq}, true
	}
	return m.eventJournals.wait(ctx, key, sinceSeq, timeout, limit, normalizeMethodFilter(methods))
}

// EventsBatch returns since-deltas for multiple instance streams in one call.
func (m *Manager) EventsBatch(
	requests []EventBatchRequest, limit int, methods []string,
) []EventStreamState {
	if m == nil || m.eventJournals == nil {
		return nil
	}
	return m.eventJournals.batch(requests, limit, normalizeMethodFilter(methods))
}

// handleDownstreamNotify is called when a downstream instance receives a
// notification (e.g. notifications/tools/list_changed, notifications/progress).
// It routes known notification types to the appropriate subsystem and logs
// others for observability.
func (m *Manager) handleDownstreamNotify(key InstanceKey, method string, params json.RawMessage) {
	if m.eventJournals != nil {
		m.eventJournals.append(key, method, params)
	}
	switch method {
	case "notifications/tools/list_changed":
		m.NotifyToolsChanged()
	case "notifications/progress":
		slog.Debug("downstream progress notification",
			"server", key.ServerID, "scope", key.AuthScopeID, "method", method, "params_len", len(params))
	case "notifications/cancelled":
		slog.Debug("downstream cancellation notification",
			"server", key.ServerID, "scope", key.AuthScopeID, "method", method, "params_len", len(params))
	case "notifications/resources/list_changed",
		"notifications/resources/updated",
		"notifications/prompts/list_changed":
		slog.Debug("unsupported downstream catalog notification",
			"server", key.ServerID, "scope", key.AuthScopeID, "method", method, "params_len", len(params))
	default:
		slog.Debug("unhandled downstream notification",
			"server", key.ServerID, "scope", key.AuthScopeID, "method", method, "params_len", len(params))
	}
}

// ListInstances returns info about all tracked (non-stopped) instances.
func (m *Manager) ListInstances() []InstanceInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]InstanceInfo, 0, len(m.instances))
	for key, inst := range m.instances {
		s := inst.getState()
		if s == StateStopped {
			continue
		}
		out = append(out, InstanceInfo{Key: key, State: s})
	}
	return out
}

// Shutdown gracefully stops all running instances.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	instances := make([]downstream, 0, len(m.instances))
	for _, inst := range m.instances {
		instances = append(instances, inst)
	}
	m.mu.Unlock()

	for _, inst := range instances {
		inst.stop()
	}

	m.mu.Lock()
	m.instances = make(map[InstanceKey]downstream)
	m.instanceStartedAt = make(map[InstanceKey]time.Time)
	m.mu.Unlock()
	return nil
}
