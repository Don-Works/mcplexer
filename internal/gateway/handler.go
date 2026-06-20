package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/compact"
	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/telegram"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// ToolLister abstracts downstream tool discovery and invocation.
type ToolLister interface {
	ListAllTools(ctx context.Context) (map[string]json.RawMessage, error)
	ListToolsForServers(ctx context.Context, serverIDs []string) (map[string]json.RawMessage, error)
	Call(ctx context.Context, serverID, authScopeID, toolName string, args json.RawMessage) (json.RawMessage, error)
}

// CachingCaller extends ToolLister with cache-aware calling.
type CachingCaller interface {
	ToolLister
	CallWithMeta(ctx context.Context, serverID, authScopeID, toolName string, args json.RawMessage, cacheBust bool) (cache.CallResult, error)
	ToolCache() *cache.ToolCache
}

// Prefetcher pre-warms downstream servers so they are ready for tool calls.
type Prefetcher interface {
	EnsureRunning(ctx context.Context, serverID, authScopeID string)
}

// SessionReleaser tears down any per-session downstream instances owned by a
// session (browser-automation processes keyed per agent). The downstream
// Manager implements it; the caching wrapper forwards it. Optional — the
// disconnect path type-asserts and skips when unimplemented.
type SessionReleaser interface {
	ReleaseSession(sessionID string)
}

// handler contains the logic for each MCP method.
type handler struct {
	store          store.Store
	engine         *routing.Engine
	manager        ToolLister
	sessions       *sessionManager
	auditor        *audit.Logger
	approvals      *approval.Manager // nil = approval system disabled
	mesh           *mesh.Manager     // nil = mesh disabled
	bridge         *telegram.Manager // nil = chat bridge disabled
	settingsSvc    *config.SettingsService
	compactor      *compact.Compactor
	toolsListCache *cache.Cache[string, json.RawMessage]
	notifier       Notifier                  // set at runtime for sending notifications
	addonRegistry  *addon.Registry           // nil = no addons loaded
	addonExecutor  *addon.Executor           // nil = no addons loaded
	addonCreator   AddonCreator              // nil = creating addons disabled
	secretPrompts  *ephemeral.Manager        // nil = secret prompt feature disabled
	secretsManager *secrets.Manager          // nil = encrypted auth-scope storage disabled
	skillShare     *p2p.SkillShareService    // nil = skill share feature disabled
	skillRegistry  *skillregistry.Registry   // nil = skills registry disabled
	registryShare  *p2p.RegistryShareService // nil = registry hub sync disabled
	memorySvc      *memory.Service           // nil = memory subsystem disabled
	memoryShare    *p2p.MemoryShareService   // nil = memory share over libp2p disabled
	tasksSvc       *tasks.Service            // nil = tasks subsystem disabled
	workerAdmin    *workersadmin.Service     // nil = worker delegation disabled
	conciergeSvc   *concierge.Service        // nil = concierge surface disabled
	brainEditor    *brain.Editor             // nil = brain subsystem disabled
	adminGate      *AdminCWDGate             // CWD-based gate for admin tools; nil = open
	scopeRegistry  *ScopeRegistry
	errTracker     errorTracker
	semIndex       semanticIndex
	contextCostMu  sync.RWMutex
	contextCost    ContextCostStats

	// sanitizer is the shared, precompiled denylist applied to every
	// downstream tool result before it reaches the upstream agent.
	// M1 of the Guards plan: regex-based injection-marker scan +
	// <untrusted-content> envelope wrap. Safe to share across goroutines.
	sanitizer *sanitize.Denylist

	// secretTransferKey is the local age X25519 identity used to decrypt
	// inbound secret_offer ciphertexts (mesh__accept_secret). Wired via
	// setSecretTransferKey post-construction. nil = the receive half of
	// mesh__send_secret is disabled (sender side still works).
	secretTransferKey *age.X25519Identity

	// refinedDescs caches active refined descriptions (tool_name -> description).
	refinedDescs   map[string]string
	refinedDescsMu sync.RWMutex

	// bgCtx is a long-lived context for background goroutines (set from run()).
	bgCtx context.Context
	// backgroundRefreshOnceByKey ensures we only trigger one background
	// refresh per cache key (per server group) after returning cached
	// capabilities. Without per-key gating, the static-group call consumed
	// the once and the dynamic group never got a refresh — leaving
	// newly-seeded dynamic servers (empty capabilities_cache) permanently
	// invisible to mcpx__execute_code.
	backgroundRefreshOnceByKey sync.Map // map[string]*sync.Once

	// lastCreatedTask tracks the most-recent task id created by each
	// (sessionID, workspaceID) pair this process has seen. Backs the
	// `compose_into: "last"` ergonomic shortcut so an agent that just
	// created an epic can compose children into it without copy-pasting
	// the ULID. Cleared on process restart — intentionally session-life,
	// not durable.
	lastCreatedTaskMu sync.RWMutex
	lastCreatedTask   map[lastCreatedKey]string

	// sessionState holds the ephemeral per-MCP-session `session` object for
	// code mode: sessionID -> (key -> JSON value). Snapshotted after each
	// clean mcpx__execute_code run and rehydrated before the next, so an agent
	// can build an expensive dataset once and reuse it across calls in the
	// same session without re-fetching. In-memory only — cleared on
	// disconnect, lost on restart. Durable, cross-session/restart state is the
	// separate kv__ tool surface.
	sessionStateMu sync.RWMutex
	sessionState   map[string]map[string]json.RawMessage
}

// lastCreatedKey scopes the "last task this session created" pointer
// to the (session, workspace) pair so a concierge-style session
// routing tasks into multiple workspaces still gets the right "last"
// per workspace.
type lastCreatedKey struct {
	SessionID   string
	WorkspaceID string
}

// setNotifier sets the notifier for sending client notifications.
func (h *handler) setNotifier(n Notifier) {
	h.notifier = n
}

// setTasksSvc wires the tasks service post-construction so the
// daemon's setup path can register CRUD without changing the
// handler constructor signature.
func (h *handler) setTasksSvc(svc *tasks.Service) {
	h.tasksSvc = svc
}

func (h *handler) setWorkerAdmin(svc *workersadmin.Service) {
	h.workerAdmin = svc
}

// setConciergeSvc wires the concierge service post-construction. Same
// rationale as setTasksSvc — keeps the constructor signature bounded.
func (h *handler) setConciergeSvc(svc *concierge.Service) {
	h.conciergeSvc = svc
}

// setBrainEditor wires the brain Editor post-construction. Same rationale as
// setTasksSvc — keeps the constructor signature bounded. nil disables the
// brain__* tool surface.
func (h *handler) setBrainEditor(e *brain.Editor) {
	h.brainEditor = e
}

// setSecretTransferKey wires the local age X25519 identity used for
// inbound secret-offer decryption. Wire post-construction so the
// constructor signature stays bounded.
func (h *handler) setSecretTransferKey(id *age.X25519Identity) {
	h.secretTransferKey = id
}

func newHandler(
	s store.Store,
	e *routing.Engine,
	m ToolLister,
	a *audit.Logger,
	t TransportMode,
	approvals *approval.Manager,
	meshMgr *mesh.Manager,
	telegramMgr *telegram.Manager,
	settingsSvc *config.SettingsService,
	addonReg *addon.Registry,
	addonExec *addon.Executor,
	sessionBus *session.Bus,
	secretPromptMgr *ephemeral.Manager,
	secretsMgr *secrets.Manager,
	skillShare *p2p.SkillShareService,
	skillRegistry *skillregistry.Registry,
	registryShare *p2p.RegistryShareService,
	memorySvc *memory.Service,
	memoryShare *p2p.MemoryShareService,
	adminGate *AdminCWDGate,
) *handler {
	ttl := 15 * time.Second
	if settingsSvc != nil {
		settings := settingsSvc.Load(context.Background())
		if settings.ToolsCacheTTLSec > 0 {
			ttl = time.Duration(settings.ToolsCacheTTLSec) * time.Second
		}
	}
	sr := NewScopeRegistry()
	sr.Register("github", GitHubExtractor{})

	return &handler{
		store:          s,
		engine:         e,
		manager:        m,
		sessions:       newSessionManager(s, e, t, sessionBus),
		auditor:        a,
		approvals:      approvals,
		mesh:           meshMgr,
		bridge:         telegramMgr,
		settingsSvc:    settingsSvc,
		compactor:      compact.New(),
		toolsListCache: cache.New[string, json.RawMessage](10, ttl),
		addonRegistry:  addonReg,
		addonExecutor:  addonExec,
		secretPrompts:  secretPromptMgr,
		secretsManager: secretsMgr,
		skillShare:     skillShare,
		skillRegistry:  skillRegistry,
		registryShare:  registryShare,
		memorySvc:      memorySvc,
		memoryShare:    memoryShare,
		adminGate:      adminGate,
		scopeRegistry:  sr,
		sanitizer:      sanitize.DefaultDenylist(),
	}
}

func (h *handler) handleInitialize(
	ctx context.Context, params json.RawMessage,
) (json.RawMessage, *RPCError) {
	var p InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	if err := h.sessions.create(ctx, p.ClientInfo, p.Roots); err != nil {
		slog.Error("create session", "error", err)
	}

	// Auto-register the agent in the mesh so it's discoverable immediately.
	if h.mesh != nil {
		meta := h.sessionMeshMeta(ctx)
		_ = h.mesh.RegisterAgent(ctx, meta)
	}

	// Echo the client's protocolVersion when provided. This keeps strict
	// MCP clients (that validate the version in the initialize result against
	// the one they sent) happy. We don't have meaningful per-version behaviour
	// differences today, so accepting the client's proposal is safe.
	pv := p.ProtocolVersion
	if pv == "" {
		pv = "2025-03-26"
	}
	result := InitializeResult{
		ProtocolVersion: pv,
		Capabilities: ServerCapability{
			Tools: &ToolCapability{ListChanged: true},
		},
		ServerInfo: ServerInfo{Name: "mcplexer", Version: "0.4.0"},
	}
	result.Instructions = buildCodeModeInstructions(
		harnessProfileForClient(p.ClientInfo.Name),
		h.mesh != nil,
	)

	data, err := json.Marshal(result)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return data, nil
}

// InvalidateAndNotifyToolsChanged clears the tools/list cache and sends
// a tools/list_changed notification to the connected client. Called by
// the downstream manager when a server emits notifications/tools/list_changed.
func (h *handler) InvalidateAndNotifyToolsChanged() {
	h.toolsListCache.Flush()
	h.sendToolsListChanged()
}

func mapRouteError(err error) *RPCError {
	switch {
	case errors.Is(err, routing.ErrNoRoute):
		return &RPCError{Code: CodeRouteNotFound, Message: "no matching route"}
	case errors.Is(err, routing.ErrDenied):
		return &RPCError{Code: CodeRouteNotFound, Message: "route denied by policy"}
	default:
		return &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
}
