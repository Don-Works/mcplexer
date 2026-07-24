package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// TransportMode controls how the client root is determined.
type TransportMode int

const (
	// TransportStdio uses os.Getwd() as the trusted client root. This is
	// secure because the CWD is inherited from the parent process and cannot
	// be spoofed via the MCP protocol.
	TransportStdio TransportMode = iota

	// TransportSocket accepts client-reported MCP roots. Used for Unix socket
	// and HTTP connections where the server process CWD is unrelated to the
	// client's working directory.
	TransportSocket

	// TransportInternal is used by the worker-bound gateway.Server: there is
	// no real wire transport and no MCP initialize handshake — workers reach
	// the handler directly via Server.CallTool. The session never
	// initializes, clientRoot stays "", and admin-CWD gating denies admin
	// tools by default (a worker has no operator-controlled CWD).
	TransportInternal
)

// sessionManager manages the current MCP client session.
type sessionManager struct {
	store      store.Store
	engine     *routing.Engine
	transport  TransportMode
	sessionBus *session.Bus // optional; publishes connect/disconnect events
	session    *store.Session
	// mu guards the mutable workspace-resolution state below
	// (clientPath/wsChain/lastWSVer/adminTrusted). server.run spawns one
	// dispatch goroutine per incoming JSON-RPC line, all sharing a single
	// sessionManager, and the worker-bound gateway shares one Server across
	// concurrent worker runs — both drive concurrent workspaceAncestors()
	// calls whose version-check-and-refresh mutates these fields. Without the
	// lock a concurrent admin-gate reader could observe a transient
	// adminTrusted=false or a torn wsChain slice (security-relevant).
	mu         sync.Mutex
	clientPath string                      // trusted client CWD
	wsChain    []routing.WorkspaceAncestor // resolved workspace ancestors, most specific first
	lastWSVer  int64                       // last seen Engine.WorkspaceVersion
	// adminTrusted is set at session-bind time when any workspace
	// ancestor carries the "admin-trusted" tag. It's the workspace-tag
	// counterpart to the CWD-based admin gate — lets the operator
	// designate specific workspaces (e.g. the concierge's Telegram
	// workspace) as full-access without relocating them to ~/.mcplexer.
	adminTrusted bool
	// discoverRepoBrain is fired on session bind with the resolved client
	// root + the most-specific workspace id, so the brain (when enabled) can
	// discover a per-repo .mcplexer/ folder and register it with the
	// indexer/watcher dynamically (docs/brain.md Appendix C.2). Nil when the
	// brain is off — a complete no-op.
	discoverRepoBrain   func(ctx context.Context, clientRoot, workspaceID string)
	initializeStarted   bool
	initializeSucceeded bool
	protocolVersion     string
	clientCapabilities  ClientCapabilities
}

func newSessionManager(s store.Store, e *routing.Engine, t TransportMode, bus *session.Bus) *sessionManager {
	return &sessionManager{store: s, engine: e, transport: t, sessionBus: bus}
}

// SetRepoBrainDiscovery wires the per-repo .mcplexer/ discovery callback
// (M6 — federation). Nil-safe; leaving it unset keeps today's behaviour.
func (sm *sessionManager) SetRepoBrainDiscovery(fn func(ctx context.Context, clientRoot, workspaceID string)) {
	sm.discoverRepoBrain = fn
}

func (sm *sessionManager) beginInitialize() error {
	sm.mu.Lock()
	if sm.initializeStarted {
		sm.mu.Unlock()
		return errors.New("MCP session is already initialized")
	}
	// Sticky even if a later store operation fails: a second initialize on
	// the same transport must never switch into or out of worker authority.
	sm.initializeStarted = true
	sm.mu.Unlock()
	return nil
}

func (sm *sessionManager) create(
	ctx context.Context,
	clientInfo ClientInfo,
	roots []Root,
	protocolVersion string,
	capabilities ClientCapabilities,
) error {
	modelHint := clientInfo.Version
	if clientInfo.Name != "" {
		modelHint = clientInfo.Name + "/" + clientInfo.Version
	}
	sess := &store.Session{
		ID:         uuid.NewString(),
		ClientType: clientInfo.Name,
		ModelHint:  modelHint,
	}

	// create() runs during the MCP initialize handshake, before any other
	// dispatch goroutine can observe this session, but we take the lock
	// anyway so the field writes are correctly synchronised with the
	// concurrent readers that follow and the race detector stays clean.
	clientPath := sm.detectClientRoot(roots)
	sm.mu.Lock()
	sm.clientPath = clientPath
	sm.wsChain = sm.resolveChainForPath(ctx, sm.clientPath)
	if sm.engine != nil {
		sm.lastWSVer = sm.engine.WorkspaceVersion()
	}
	if len(sm.wsChain) > 0 {
		sess.WorkspaceID = &sm.wsChain[0].ID
	}
	sm.session = sess
	sm.protocolVersion = protocolVersion
	sm.clientCapabilities = capabilities.clone()
	sm.mu.Unlock()

	// Record last_initialize_at + clientInfo for harness-sync /setup/status.
	// Best effort (harness key derived from clientInfo.Name); do not fail
	// the handshake on store hiccup.
	if key, ok := harnessKeyForClientInfo(clientInfo.Name); ok {
		_ = sm.store.RecordHarnessInitialize(ctx, key, clientInfo.Name)
	}

	// Per-repo .mcplexer/ discovery (docs/brain.md Appendix C.2): if the
	// brain is enabled, let it walk the client root for a repo-local brain
	// folder and register it. Done after the chain resolves so the
	// most-specific workspace id is available to bind the repo dir.
	if sm.discoverRepoBrain != nil && sm.clientPath != "" {
		wsID := ""
		if len(sm.wsChain) > 0 {
			wsID = sm.wsChain[0].ID
		}
		sm.discoverRepoBrain(ctx, sm.clientPath, wsID)
		// Discovery may materialise a more-specific project workspace from a
		// repo-local .mcplexer/ marker. Re-resolve before the session row is
		// persisted so the very first task__* call lands in that child
		// workspace instead of the parent that was visible before discovery.
		sm.mu.Lock()
		sm.wsChain = sm.resolveChainForPath(ctx, sm.clientPath)
		if sm.engine != nil {
			sm.lastWSVer = sm.engine.WorkspaceVersion()
		}
		if len(sm.wsChain) > 0 {
			sess.WorkspaceID = &sm.wsChain[0].ID
		} else {
			sess.WorkspaceID = nil
		}
		sm.mu.Unlock()
	}

	if err := sm.store.CreateSession(ctx, sess); err != nil {
		return err
	}
	sm.mu.Lock()
	sm.initializeSucceeded = true
	sm.mu.Unlock()
	if sm.sessionBus != nil {
		sm.sessionBus.Publish(session.Event{
			Type:    session.EventConnected,
			Session: *sess,
		})
	}
	return nil
}

func (sm *sessionManager) requireInitialized(method string) error {
	if sm.transport == TransportInternal || method == "initialize" || method == "ping" {
		return nil
	}
	sm.mu.Lock()
	ok := sm.initializeSucceeded
	sm.mu.Unlock()
	if !ok {
		return errors.New("MCP session must initialize successfully before using this method")
	}
	return nil
}

// resolveChainForPath finds all workspaces whose root path is an ancestor
// of clientRoot, ordered from most specific to least. Side effect: also
// recomputes sm.adminTrusted from the resolved ancestors' tags.
func (sm *sessionManager) resolveChainForPath(ctx context.Context, clientRoot string) []routing.WorkspaceAncestor {
	workspaces, err := sm.store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("failed to list workspaces for session binding", "error", err)
		return nil
	}

	// Collect all ancestor workspaces.
	var ancestors []store.Workspace
	for _, ws := range workspaces {
		if ws.RootPath != "" && isPathAncestor(ws.RootPath, clientRoot) {
			ancestors = append(ancestors, ws)
		}
	}

	// Sort by path length descending (most specific first).
	sort.Slice(ancestors, func(i, j int) bool {
		return len(ancestors[i].RootPath) > len(ancestors[j].RootPath)
	})

	// Brain hierarchy (docs/brain.md Appendix C.1): a workspace may name a
	// `parent` (client/org tier) that is NOT a path ancestor. Fuse the
	// parent chain in after the path ancestors so a session at acme-api
	// resolves [acme-api, acme, ...] and recall/list span acme ∪ global
	// too. Walk parent_id upward from every path ancestor, deduplicating by
	// id and guarding against cycles.
	ancestors = appendParentChain(ancestors, workspaces)

	chain := make([]routing.WorkspaceAncestor, len(ancestors))
	sm.adminTrusted = false
	for i, ws := range ancestors {
		chain[i] = routing.WorkspaceAncestor{ID: ws.ID, Name: ws.Name, RootPath: ws.RootPath}
		if workspaceHasTag(ws.Tags, adminTrustedTag) {
			sm.adminTrusted = true
		}
	}
	return chain
}

// appendParentChain extends the path-resolved ancestor list with each
// workspace's parent_id chain (docs/brain.md Appendix C.1). Parents are
// appended AFTER the path ancestors (lower precedence — a child's
// most-specific workspace still wins for publish/admin), deduplicated by id,
// and cycle-guarded. all is the full workspace set (already fetched by the
// caller) used to look parents up by id without another DB round-trip.
func appendParentChain(resolved []store.Workspace, all []store.Workspace) []store.Workspace {
	byID := make(map[string]store.Workspace, len(all))
	for _, w := range all {
		byID[w.ID] = w
	}
	seen := make(map[string]bool, len(resolved))
	for _, w := range resolved {
		seen[w.ID] = true
	}
	out := resolved
	// Walk up from each already-resolved workspace. A bounded loop guards
	// against a corrupted parent cycle (seen[] also breaks it, but the cap
	// is a belt-and-braces bound on pathological data).
	for _, start := range resolved {
		cur := start
		for depth := 0; depth < len(all)+1; depth++ {
			if cur.ParentID == "" {
				break
			}
			parent, ok := byID[cur.ParentID]
			if !ok {
				break // dangling parent — degrade to "no parent"
			}
			if !seen[parent.ID] {
				seen[parent.ID] = true
				out = append(out, parent)
			}
			cur = parent
		}
	}
	return out
}

// workspaceHasTag reports whether the workspace's tags JSON array
// contains the given tag (case-insensitive). Workspace.Tags is stored
// as raw JSON (e.g. ["concierge","telegram"]); we decode lazily.
// Malformed JSON falls through as "no match" so a corrupted tags
// column can't accidentally grant trust.
func workspaceHasTag(tagsJSON json.RawMessage, want string) bool {
	if len(tagsJSON) == 0 {
		return false
	}
	var tags []string
	if err := json.Unmarshal(tagsJSON, &tags); err != nil {
		return false
	}
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}

// adminTrustedTag is the workspace-tag value that grants admin access
// to a session without requiring its CWD to be ~/.mcplexer. Operators
// apply this tag to workspaces hosting trusted orchestrator workers
// (e.g. the Telegram concierge) so those workers can call
// mcplexer__create_worker, spawn_subagent, etc. without needing to be
// relocated under the data directory.
const adminTrustedTag = "admin-trusted"

// detectClientRoot determines the client's working directory based on the
// transport mode.
//
// stdio:  os.Getwd() — inherited from parent, tamper-proof via MCP.
// socket: client-reported MCP roots — logged for audit trail.
func (sm *sessionManager) detectClientRoot(roots []Root) string {
	if sm.transport == TransportStdio {
		cwd, err := os.Getwd()
		if err != nil {
			slog.Error("failed to detect working directory", "error", err)
			return ""
		}
		// Warn if client-reported roots disagree with the actual CWD.
		sm.validateClientRoots(cwd, roots)
		return cwd
	}

	// Socket/HTTP mode: use client-reported roots.
	var clientRoot string
	for _, root := range roots {
		if p := uriToPath(root.URI); p != "" {
			clientRoot = p
			break
		}
	}
	if clientRoot == "" {
		slog.Warn("no client root detected from MCP roots",
			"roots_count", len(roots), "transport", "socket")
	} else {
		slog.Info("session bound from client-reported root",
			"root", clientRoot, "transport", "socket")
	}
	return clientRoot
}

// validateClientRoots checks that client-reported MCP roots are consistent
// with the actual process CWD. Logs a warning on mismatch — this could
// indicate a spoofing attempt in stdio mode.
func (sm *sessionManager) validateClientRoots(cwd string, roots []Root) {
	if len(roots) == 0 {
		return
	}
	for _, root := range roots {
		p := uriToPath(root.URI)
		if p == "" {
			continue
		}
		if p == cwd || isPathAncestor(p, cwd) || isPathAncestor(cwd, p) {
			return // at least one root is consistent
		}
	}
	var reported []string
	for _, root := range roots {
		reported = append(reported, root.URI)
	}
	slog.Warn("client-reported roots do not match process CWD",
		"cwd", cwd, "reported_roots", reported)
}

// isPathAncestor returns true if ancestor is a path ancestor of (or equal to) path.
// It checks path boundaries to prevent "/users/m" matching "/users/example".
func isPathAncestor(ancestor, path string) bool {
	ancestor = strings.TrimSuffix(ancestor, "/")
	path = strings.TrimSuffix(path, "/")

	if ancestor == path {
		return true
	}
	if ancestor == "" { // Was "/"
		return true
	}
	return strings.HasPrefix(path, ancestor+"/")
}

// uriToPath extracts a filesystem path from a file:// URI.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri // best-effort: treat as raw path
	}
	if u.Scheme == "file" {
		return u.Path
	}
	return uri
}

func (sm *sessionManager) disconnect(ctx context.Context) error {
	sm.mu.Lock()
	sess := sm.session
	sm.mu.Unlock()
	if sess == nil {
		return nil
	}
	if err := sm.store.DisconnectSession(ctx, sess.ID); err != nil {
		return err
	}
	if sm.sessionBus != nil {
		s := *sess
		now := time.Now().UTC()
		s.DisconnectedAt = &now
		sm.sessionBus.Publish(session.Event{
			Type:    session.EventDisconnected,
			Session: s,
		})
	}
	return nil
}

func (sm *sessionManager) sessionID() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil {
		return ""
	}
	return sm.session.ID
}

func (sm *sessionManager) workspaceID() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.wsChain) == 0 {
		return ""
	}
	return sm.wsChain[0].ID
}

func (sm *sessionManager) workspaceName() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.wsChain) == 0 {
		return ""
	}
	return sm.wsChain[0].Name
}

func (sm *sessionManager) workspaceAncestors(ctx context.Context) []routing.WorkspaceAncestor {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.engine != nil {
		if v := sm.engine.WorkspaceVersion(); v != sm.lastWSVer {
			sm.wsChain = sm.resolveChainForPath(ctx, sm.clientPath)
			sm.lastWSVer = v
			// direct sm.session read OK here: we hold sm.mu (sessionID would deadlock)
			sid := ""
			if sm.session != nil {
				sid = sm.session.ID
			}
			slog.Info("session workspace chain refreshed",
				"session", sid,
				"workspaces", len(sm.wsChain))
		}
	}
	// Return a copy: callers must not observe a slice that a concurrent
	// refresh could replace, and a defensive copy keeps the underlying array
	// immutable from the caller's perspective.
	out := make([]routing.WorkspaceAncestor, len(sm.wsChain))
	copy(out, sm.wsChain)
	return out
}

func (sm *sessionManager) clientRoot() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.clientPath
}

// workspaceRoots returns the non-empty RootPath of every resolved
// workspace ancestor for this session. Used by the admin gate so the
// dev-mode source-repo exemption can lift over the daemon socket, where
// clientRoot() is empty because Claude Code doesn't advertise the
// source repo as an MCP root. A registered workspace whose root is a
// mcplexer source tree is an equally strong "owns the path" signal.
func (sm *sessionManager) workspaceRoots() []string {
	if sm == nil {
		return nil
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.wsChain) == 0 {
		return nil
	}
	roots := make([]string, 0, len(sm.wsChain))
	for _, ws := range sm.wsChain {
		if ws.RootPath != "" {
			roots = append(roots, ws.RootPath)
		}
	}
	return roots
}

// isAdminTrusted reports whether this session's workspace chain
// carries an "admin-trusted" tag. The flag is set at session-bind
// time; refresh by re-resolving when the workspace version changes
// (which workspaceAncestors already does).
func (sm *sessionManager) isAdminTrusted() bool {
	if sm == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.adminTrusted
}

func (sm *sessionManager) clientType() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil {
		return ""
	}
	return sm.session.ClientType
}

func (sm *sessionManager) modelHint() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil {
		return ""
	}
	return sm.session.ModelHint
}

// clientNegotiation returns an immutable snapshot of the protocol revision and
// open client capability object negotiated during initialize. The capability
// map and each raw value are copied so future capability-aware handlers can
// inspect or decode them without racing with or mutating session state.
func (sm *sessionManager) clientNegotiation() (string, ClientCapabilities) {
	if sm == nil {
		return "", nil
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.protocolVersion, sm.clientCapabilities.clone()
}
