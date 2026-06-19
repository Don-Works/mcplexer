package api

import (
	"context"
	"io/fs"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/assist"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/backup"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/hammerspoon"
	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/opencode"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/sandbox"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/telegram"
	"github.com/don-works/mcplexer/internal/web"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// Go's stdlib mime package doesn't know about `.webmanifest`. Without this
// registration, http.FileServerFS serves the PWA manifest with
// `Content-Type: text/plain`, which Chrome rejects when deciding whether
// the page is installable. application/manifest+json is the spec-defined
// type (W3C App Manifest §8).
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// RouterDeps holds the dependencies needed by the HTTP API router.
type RouterDeps struct {
	APIToken              string // required; token for HTTP API authentication
	Store                 store.Store
	ConfigSvc             *config.Service
	SettingsSvc           *config.SettingsService // optional; enables settings API
	Engine                *routing.Engine
	Manager               *downstream.Manager     // optional; enables tool discovery
	FlowManager           *oauth.FlowManager      // optional; enables OAuth flows
	Encryptor             *secrets.AgeEncryptor   // optional; enables secret encryption
	AuditBus              *audit.Bus              // optional; enables SSE audit stream
	ApprovalManager       *approval.Manager       // optional; enables approval system
	ApprovalBus           *approval.Bus           // optional; enables approval SSE stream
	Auditor               *audit.Logger           // optional; enables shell-guard pretool audit emission
	BackupSvc             *backup.Service         // optional; enables /api/v1/backups
	NotifyBus             *notify.Bus             // optional; enables user-notification SSE stream
	NotifyStore           notify.Store            // optional; enables persistent /api/v1/notifications endpoints (Signal tray)
	SessionBus            *session.Bus            // optional; enables session SSE stream
	ToolCache             *cache.ToolCache        // optional; enables cache stats/flush API
	InstallManager        *install.Manager        // optional; enables MCP install endpoints
	AddonRegistry         *addon.Registry         // optional; enables addon tools in discovery
	AddonCreator          *addon.Creator          // optional; enables /api/v1/addons authoring endpoints
	AddonPreview          *addon.PreviewExecutor  // optional; enables /api/v1/addons/preview-call
	OAuthWizard           *oauth.Wizard           // optional; enables /api/v1/addons/oauth-setup
	MeshManager           *mesh.Manager           // optional; enables /api/v1/mesh/send
	TelegramManager       *telegram.Manager       // optional; enables chat bridge API
	GoogleChatManager     *googlechat.Manager     // optional; enables Google Chat bridge API
	GoogleChatJWTVerifier *googlechat.JWTVerifier // optional; required by default (fail-closed); skip with GOOGLECHAT_DISABLE_JWT_VALIDATION=true
	HammerspoonManager    *hammerspoon.Manager    // optional; enables Hammerspoon installer + probe API
	P2PHost               *p2p.Host               // optional; enables /api/p2p/identity
	P2PPairing            *p2p.PairingService     // optional; enables /api/p2p/pair/*
	P2PPeerLookup         *p2p.SQLPeerLookup      // optional; enables /api/p2p/peers/{id}/status
	P2PReconnector        *p2p.Reconnector        // optional; surfaces reconnect telemetry on peer list / status
	SkillRunner           SkillRunner             // optional; enables /api/skills/{id}/run
	SkillsRoot            SkillsRoot              // optional; defaults to ~/.mcplexer/skills
	SecretPrompts         *ephemeral.Manager      // optional; enables /api/v1/secrets/prompts
	SecretPromptBus       *ephemeral.Bus          // optional; enables SSE for secret prompts
	SkillRegistry         *skillregistry.Registry // optional; enables /api/v1/skill-registry/*
	CatalogSvc            *config.CatalogService  // optional; enables /api/v1/catalog

	// WorkerTemplateRegistry — backs /api/v1/worker-templates and
	// /api/v1/workers/{id}/publish. Lives in the worker_templates table
	// (migration 057). Independent of SkillRegistry — either can be
	// wired without the other.
	WorkerTemplateRegistry *workertemplates.Registry

	// MemorySvc — backs /api/v1/memory/*. Wraps store.MemoryStore plus
	// an optional embed provider (NoopEmbedder fallback if unset). The
	// memory routes register only when this is non-nil. The handler
	// also uses deps.Store's MemoryStore methods directly for the
	// memory-offer surface.
	MemorySvc *memory.Service

	// TasksSvc — backs /api/v1/tasks/* (migration 061). Routes register
	// only when this is non-nil. The handler also reaches into deps.Store
	// for the vocabulary management endpoints.
	TasksSvc *tasks.Service

	// ConciergeSvc — backs /api/v1/chat-signals/* (migration 080). The
	// friction-extractor worker hits this REST surface to pull negative
	// signals + mark them promoted once they feed a refinement proposal.
	ConciergeSvc *concierge.Service

	// Guards (M1-D) — UI shim over the per-Guard packages. All optional;
	// individual fields surface in the response only when wired. The
	// guardsHandler exposes:
	//   GET    /api/v1/guards
	//   GET    /api/v1/guards/shell
	//   POST   /api/v1/guards/shell/clients/{id}/install_hooks
	//   POST   /api/v1/guards/shell/clients/{id}/uninstall_hooks
	//   GET    /api/v1/guards/sanitizer
	//   PUT    /api/v1/guards/sanitizer
	//   GET    /api/v1/guards/sanitizer/denylist
	//   GET    /api/v1/guards/schedule
	//   POST   /api/v1/guards/schedule
	//   POST   /api/v1/guards/schedule/{id}/run
	//   DELETE /api/v1/guards/schedule/{id}
	//   GET    /api/v1/guards/sandbox
	//   POST   /api/v1/guards/sandbox/clients/{id}/enable
	//   POST   /api/v1/guards/sandbox/clients/{id}/disable
	Scheduler      *scheduler.Scheduler   // optional; enables /api/v1/guards/schedule/{id}/run
	HookInstaller  *install.HookInstaller // optional; enables shell-guard install/uninstall
	Sanitizer      *sanitize.Denylist     // optional; populates sanitizer denylist surface
	SandboxInstall *sandbox.Installer     // optional; enables sandbox-guard enable/disable

	// Workers (M0.6) — same Service that backs the mcplexer__*_worker MCP
	// tools. Wired here so the PWA hits identical code paths over HTTP.
	// Optional: when nil the /api/v1/workers/* routes are not registered
	// and the UI surfaces an empty list.
	WorkerAdmin *workersadmin.Service

	// OpenCode (Layer 3) — managed `opencode serve` subprocess. Optional:
	// when nil the /api/v1/opencode/* routes are not registered and the
	// UI shows a "not installed" hint with an install link.
	OpenCode *opencode.Manager

	// Brain (M5) — backs the dashboard Brain tile (/api/v1/brain/*). All
	// optional: when the brain is disabled the status endpoint reports
	// enabled:false and the tile renders an opt-in hint. BrainGit is nil
	// when git is unavailable; the push endpoint then 503s.
	BrainGit     *brain.Git
	BrainConfig  brain.Config
	BrainEnabled bool

	// BrainEditor (M7) — backs the Notion-like Brain record editor
	// (/api/v1/brain/tree, /workspaces/{ws}/tasks|memory,
	// /record/{kind}/{id}). nil when the brain is disabled; the browser
	// routes then 503 and the SPA renders an opt-in hint.
	BrainEditor *brain.Editor

	// BrainIndexer (M7) — backs the browser's POST /reindex + /sync
	// (full reindex; git pull --rebase --autostash -> reindex). nil when
	// the brain is disabled; those endpoints then 503.
	BrainIndexer *brain.Indexer

	// BrainAssist (M8) — backs the lean AI augmentation endpoints
	// (/api/v1/assist/complete SSE ghost-text, /assist/memory-candidates).
	// Constructs a models.Adapter directly (never the worker runner). nil
	// when the brain is disabled OR no model surface is configured; the
	// endpoints then 503 (disabled) or 204 (no profile) so the GUI degrades
	// silently.
	BrainAssist *assist.Assistant

	// TrustedHosts allows browser requests whose Origin's hostname matches one
	// of these entries, in addition to the always-allowed loopback hosts. Use
	// this when serving the UI on a non-localhost interface (e.g. binding to
	// 0.0.0.0 on a LAN host and hitting http://my-host:13333). Entries should
	// be bare hostnames (no scheme/port); they are matched case-insensitively
	// against the Origin's hostname.
	TrustedHosts []string
}

// NewRouter creates an http.Handler with all API routes and SPA fallback.
func NewRouter(deps RouterDeps) http.Handler {
	mux := http.NewServeMux()

	ws := &workspaceHandler{svc: deps.ConfigSvc, store: deps.Store, engine: deps.Engine, manager: deps.Manager}
	mux.HandleFunc("GET /api/v1/workspaces", ws.list)
	mux.HandleFunc("POST /api/v1/workspaces", ws.create)
	mux.HandleFunc("GET /api/v1/workspaces/{id}", ws.get)
	mux.HandleFunc("PUT /api/v1/workspaces/{id}", ws.update)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", ws.delete)

	ds := &downstreamHandler{svc: deps.ConfigSvc, store: deps.Store, engine: deps.Engine, manager: deps.Manager, toolCache: deps.ToolCache}
	mux.HandleFunc("GET /api/v1/downstreams", ds.list)
	mux.HandleFunc("POST /api/v1/downstreams", ds.create)
	mux.HandleFunc("GET /api/v1/downstreams/{id}", ds.get)
	mux.HandleFunc("PUT /api/v1/downstreams/{id}", ds.update)
	mux.HandleFunc("DELETE /api/v1/downstreams/{id}", ds.delete)

	// Stuck-detector observability — surfaces per-server consecutive
	// failures + auto-reload count so the dashboard can warn "this
	// server has been flaky" before the user notices. Reads off the
	// in-memory health tracker; no SQL hit on the hot path.
	dsh := &downstreamHealthHandler{store: deps.Store, manager: deps.Manager}
	mux.HandleFunc("GET /api/v1/downstreams/{id}/health", dsh.get)

	rt := &routeHandler{svc: deps.ConfigSvc, store: deps.Store, engine: deps.Engine, manager: deps.Manager}
	mux.HandleFunc("GET /api/v1/routes", rt.list)
	mux.HandleFunc("POST /api/v1/routes/bulk", rt.bulkCreate)
	mux.HandleFunc("POST /api/v1/routes", rt.create)
	mux.HandleFunc("GET /api/v1/routes/{id}", rt.get)
	mux.HandleFunc("PUT /api/v1/routes/{id}", rt.update)
	mux.HandleFunc("DELETE /api/v1/routes/{id}", rt.delete)

	// Linked workspaces — cross-machine task replication links (migration
	// 088). In-process REST mirror of the mcplexer__link_workspace MCP
	// admin tools so the dashboard + integration harness can drive them.
	wl := &workspaceLinkHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/workspace-links", wl.list)
	mux.HandleFunc("POST /api/v1/workspace-links", wl.create)
	mux.HandleFunc("DELETE /api/v1/workspace-links", wl.delete)
	mux.HandleFunc("GET /api/v1/workspace-links/suggest", wl.suggest)

	auth := &authHandler{svc: deps.ConfigSvc, store: deps.Store}
	mux.HandleFunc("GET /api/v1/auth-scopes", auth.list)
	mux.HandleFunc("POST /api/v1/auth-scopes", auth.create)
	mux.HandleFunc("GET /api/v1/auth-scopes/{id}", auth.get)
	mux.HandleFunc("PUT /api/v1/auth-scopes/{id}", auth.update)
	mux.HandleFunc("DELETE /api/v1/auth-scopes/{id}", auth.delete)

	if deps.Encryptor != nil {
		sm := secrets.NewManager(deps.Store, deps.Encryptor)
		if deps.Auditor != nil {
			sm.SetAuditor(deps.Auditor)
		}
		sec := &secretsHandler{manager: sm, store: deps.Store}
		mux.HandleFunc("GET /api/v1/auth-scopes/{id}/secrets", sec.listKeys)
		mux.HandleFunc("PUT /api/v1/auth-scopes/{id}/secrets", sec.put)
		mux.HandleFunc("DELETE /api/v1/auth-scopes/{id}/secrets/{key}", sec.remove)
	}

	auditH := &auditHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/audit", auditH.query)

	if deps.AuditBus != nil {
		sse := &auditSSEHandler{bus: deps.AuditBus}
		mux.HandleFunc("GET /api/v1/audit/stream", sse.stream)
	}

	if deps.ApprovalManager != nil {
		ah := &approvalHandler{manager: deps.ApprovalManager, store: deps.Store}
		mux.HandleFunc("GET /api/v1/approvals", ah.list)
		mux.HandleFunc("GET /api/v1/approvals/{id}", ah.get)
		mux.HandleFunc("POST /api/v1/approvals/{id}/resolve", ah.resolve)

		// Approval rules CRUD — the trusted-allowlist that auto-decides
		// shell-guard approvals after a 5s grace period. Mutations
		// trigger ReloadPolicyRules on the manager so edits take effect
		// without a daemon restart.
		if deps.Store != nil {
			rules := &approvalRulesHandler{store: deps.Store, mgr: deps.ApprovalManager}
			mux.HandleFunc("GET /api/v1/approval-rules", rules.list)
			mux.HandleFunc("POST /api/v1/approval-rules", rules.create)
			mux.HandleFunc("PUT /api/v1/approval-rules/{id}", rules.update)
			mux.HandleFunc("DELETE /api/v1/approval-rules/{id}", rules.delete)
		}

		// M1-A — Claude Code PreToolUse hook bridge. Validates Bash
		// commands and routes them through the approval pipeline with
		// Surface="shell". Other tools pass through unconditionally.
		// deps.Auditor is optional: when nil the handler silently skips
		// audit emission so unit tests / minimal builds still work.
		hooks := &hooksHandler{
			approvalMgr: deps.ApprovalManager,
			auditor:     deps.Auditor,
			// Workspace resolver — lets the hook tag every audit +
			// approval row with the workspace whose RootPath the
			// agent's cwd lands inside, so the Audit page renders
			// "Project A" instead of "-". Nil-safe; deps.Store has
			// always carried ListWorkspaces.
			workspaces: deps.Store,
			// Memory recaller — backs the SessionStart digest in the
			// memory-contract session hook (hooks_session.go). Nil-safe:
			// when unset, the recall/capture nudges still fire, just
			// without the inline memory head-start.
			memories: deps.Store,
		}
		// Dangerous-mode accessor. Mirrors the approval.Manager wiring —
		// returning true from this lambda makes the pretool handler skip
		// every cheap-block (metachars, banned interpreters, eval flags)
		// and pass the request through, while still recording an audit
		// row tagged "dangerous-mode bypass" so the timeline reflects
		// what was waved through. Nil-safe — handler defaults to "off".
		if deps.SettingsSvc != nil {
			settingsSvc := deps.SettingsSvc
			hooks.dangerousMode = func() bool {
				return settingsSvc.Load(context.Background()).DangerousModeEnabled
			}
		}
		mux.HandleFunc("POST /v1/hooks/pretool", hooks.pretool)
		// Memory-contract session hook. SessionStart injects a recall
		// nudge + workspace memory digest; SessionEnd/Stop injects a
		// capture nudge. Makes recall/capture an enforced session-
		// lifecycle event instead of advisory-only (mirrors the task
		// lease enforcement). See hooks_session.go.
		mux.HandleFunc("POST /v1/hooks/session", hooks.session)
	}

	if deps.ApprovalBus != nil {
		asse := &approvalSSEHandler{bus: deps.ApprovalBus}
		mux.HandleFunc("GET /api/v1/approvals/stream", asse.stream)
	}

	if deps.NotifyBus != nil {
		nsse := &notifySSEHandler{bus: deps.NotifyBus}
		mux.HandleFunc("GET /api/v1/notifications/stream", nsse.stream)
	}

	if deps.NotifyStore != nil {
		nh := &notificationsHandler{store: deps.NotifyStore}
		mux.HandleFunc("GET /api/v1/notifications", nh.list)
		mux.HandleFunc("GET /api/v1/notifications/unread-count", nh.unreadCount)
		mux.HandleFunc("POST /api/v1/notifications/{id}/read", nh.markRead)
		mux.HandleFunc("POST /api/v1/notifications/read", nh.markReadBulk)
	}

	if deps.BackupSvc != nil {
		bh := &backupHandler{svc: deps.BackupSvc}
		mux.HandleFunc("GET /api/v1/backups", bh.list)
		mux.HandleFunc("POST /api/v1/backups", bh.create)
		mux.HandleFunc("GET /api/v1/backups/{id}", bh.get)
		mux.HandleFunc("GET /api/v1/backups/{id}/download", bh.download)
		mux.HandleFunc("POST /api/v1/backups/{id}/restore", bh.restore)
		mux.HandleFunc("DELETE /api/v1/backups/{id}", bh.delete)
	}

	// Brain tile (M5) — registered unconditionally so the dashboard always
	// has a status surface. When the brain is disabled the handler reports
	// enabled:false (the tile renders an opt-in hint); push 503s when git
	// is unavailable.
	{
		bh := &brainHandler{
			cfg:     deps.BrainConfig,
			git:     deps.BrainGit,
			store:   deps.Store,
			enabled: deps.BrainEnabled,
		}
		mux.HandleFunc("GET /api/v1/brain/status", bh.status)
		mux.HandleFunc("GET /api/v1/brain/errors", bh.errors)
		mux.HandleFunc("POST /api/v1/brain/push", bh.push)
		mux.HandleFunc("POST /api/v1/brain/sync", bh.sync)

		// Brain record browser/editor (M7) — Notion-like GUI over the same
		// canonical files VSCode + Claude use. Registered unconditionally
		// so the SPA always has a surface; each endpoint 503s when the
		// brain is disabled (h.ready guard). Static /tree + parameterised
		// /workspaces/{ws}/* + /record/{kind}/{id} share one handler.
		bbh := &brainBrowserHandler{
			editor:  deps.BrainEditor,
			indexer: deps.BrainIndexer,
			git:     deps.BrainGit,
			store:   deps.Store,
			enabled: deps.BrainEnabled,
		}
		mux.HandleFunc("GET /api/v1/brain/tree", bbh.tree)
		mux.HandleFunc("GET /api/v1/brain/clients", bbh.clients)
		mux.HandleFunc("GET /api/v1/brain/workspaces", bbh.workspaces)
		mux.HandleFunc("GET /api/v1/brain/scope", bbh.scope)
		mux.HandleFunc("GET /api/v1/brain/records", bbh.records)
		mux.HandleFunc("GET /api/v1/brain/search", bbh.search)
		mux.HandleFunc("GET /api/v1/brain/workspaces/{ws}/tasks", bbh.listTasks)
		mux.HandleFunc("GET /api/v1/brain/workspaces/{ws}/memory", bbh.listMemories)
		mux.HandleFunc("GET /api/v1/brain/record/{kind}/{id}", bbh.getRecord)
		mux.HandleFunc("POST /api/v1/brain/record/{kind}", bbh.saveRecord)
		mux.HandleFunc("PUT /api/v1/brain/record/{kind}/{id}", bbh.saveRecord)
		mux.HandleFunc("POST /api/v1/brain/record/{id}/suppress-candidate", bbh.suppressCandidate)
		mux.HandleFunc("POST /api/v1/brain/reindex", bbh.reindex)
		mux.HandleFunc("POST /api/v1/brain/browser-sync", bbh.browserSync)

		// Brain assist (M8) — lean AI augmentation: ghost-text SSE +
		// proactive memory candidates. Registered unconditionally; each
		// endpoint 503s when the brain is disabled (assist unwired) and
		// 204s when no model profile resolves (silent degrade).
		ah := &assistHandler{
			assistant: deps.BrainAssist,
			store:     deps.Store,
			enabled:   deps.BrainEnabled && deps.BrainAssist != nil,
		}
		mux.HandleFunc("POST /api/v1/assist/complete", ah.complete)
		mux.HandleFunc("POST /api/v1/assist/memory-candidates", ah.memoryCandidates)
		mux.HandleFunc("POST /api/v1/assist/guidance", ah.guidance)
	}

	if deps.SecretPrompts != nil {
		sp := &secretPromptsHandler{manager: deps.SecretPrompts, store: deps.Store}
		mux.HandleFunc("GET /api/v1/secrets/prompts/pending", sp.listPending)
		mux.HandleFunc("POST /api/v1/secrets/prompts/{id}/submit", sp.submit)
		mux.HandleFunc("POST /api/v1/secrets/prompts/{id}/cancel", sp.cancel)
	}

	if deps.SecretPromptBus != nil {
		spsse := &secretPromptsSSEHandler{bus: deps.SecretPromptBus}
		mux.HandleFunc("GET /api/v1/secrets/prompts/stream", spsse.stream)
	}

	if deps.SessionBus != nil {
		ssse := &sessionSSEHandler{bus: deps.SessionBus}
		mux.HandleFunc("GET /api/v1/sessions/stream", ssse.stream)
	}

	// Multiplexed always-on event stream — folds notifications, approvals,
	// sessions, secret-prompts, tasks, and low-volume worker run status onto
	// ONE connection so the browser's ~6-per-origin HTTP/1.1 budget isn't
	// exhausted by a separate EventSource per stream. The per-stream endpoints
	// above stay registered for backward-compat; the dashboard now subscribes
	// via this one. See events_sse_handler.go.
	{
		esse := &eventsSSEHandler{
			notifyBus:   deps.NotifyBus,
			approvalBus: deps.ApprovalBus,
			sessionBus:  deps.SessionBus,
			secretBus:   deps.SecretPromptBus,
		}
		if deps.TasksSvc != nil {
			esse.tasksBus = deps.TasksSvc.Bus()
		}
		if deps.WorkerAdmin != nil {
			esse.workerBus = deps.WorkerAdmin.RunBus()
		}
		mux.HandleFunc("GET /api/v1/events/stream", esse.stream)
	}

	ch := &cacheHandler{toolCache: deps.ToolCache, engine: deps.Engine}
	mux.HandleFunc("GET /api/v1/cache/stats", ch.stats)
	mux.HandleFunc("POST /api/v1/cache/flush", ch.flush)

	mux.HandleFunc("GET /api/v1/health", healthCheck)
	// Conventional probe alias with the same readiness contract.
	mux.HandleFunc("GET /healthz", healthCheck)
	mux.HandleFunc("POST /api/v1/system/reveal", systemReveal)
	mux.HandleFunc("POST /api/v1/system/launch-terminal", systemLaunchTerminal)

	dash := &dashboardHandler{
		sessionStore:    deps.Store,
		auditStore:      deps.Store,
		downstreamStore: deps.Store,
		approvalStore:   deps.Store,
		peerStore:       deps.Store,
		manager:         deps.Manager,
		toolCache:       deps.ToolCache,
		engine:          deps.Engine,
	}
	// Mesh time-series query lives on the sqlite-concrete DB, not the
	// store.Store interface. Type-assert to keep the dashboard wiring
	// optional — tests that pass a mock store get nil here, which the
	// handler tolerates by leaving mesh counts at zero.
	if mc, ok := deps.Store.(MeshCountStore); ok {
		dash.meshStore = mc
	}
	if deps.WorkerAdmin != nil {
		dash.delegationCounter = deps.WorkerAdmin
	}
	mux.HandleFunc("GET /api/v1/dashboard", dash.get)

	// Cross-workspace activity tiles — Linear-inbox-style rolling feeds
	// of tasks + memories. Each handler degrades to 503 when its
	// service isn't wired, so the SPA can render an empty-state tile
	// instead of crashing on a missing route.
	activity := &dashboardActivityHandler{
		tasksSvc:  deps.TasksSvc,
		memorySvc: deps.MemorySvc,
		wsStore:   deps.Store,
	}
	mux.HandleFunc("GET /api/v1/dashboard/activity/tasks", activity.handleTasksActivity)
	mux.HandleFunc("GET /api/v1/dashboard/activity/memories", activity.handleMemoriesActivity)

	dr := &dryRunHandler{
		engine:          deps.Engine,
		routeStore:      deps.Store,
		workspaceStore:  deps.Store,
		downstreamStore: deps.Store,
		authScopeStore:  deps.Store,
		flowManager:     deps.FlowManager,
	}
	mux.HandleFunc("POST /api/v1/dry-run", dr.run)

	if deps.InstallManager != nil {
		ih := &installHandler{manager: deps.InstallManager}
		mux.HandleFunc("GET /api/v1/mcp-install/status", ih.status)
		mux.HandleFunc("POST /api/v1/mcp-install/{clientId}/install", ih.install)
		mux.HandleFunc("POST /api/v1/mcp-install/{clientId}/uninstall", ih.uninstall)
		mux.HandleFunc("GET /api/v1/mcp-install/{clientId}/preview", ih.preview)
	}

	// Harness sync / bootstrap status (migration 104 + harnesssync pkg).
	if deps.Store != nil {
		sh := &harnessSetupHandler{
			store:         deps.Store,
			installMgr:    deps.InstallManager,
			skillRegistry: deps.SkillRegistry,
		}
		mux.HandleFunc("GET /api/v1/setup/status", sh.status)
		mux.HandleFunc("POST /api/v1/setup/install", sh.install)
		mux.HandleFunc("POST /api/v1/setup/recheck", sh.recheck)
	}

	// Guards (M1-D) — register unconditionally when a Store is wired so
	// the SPA always has the overview surface to render against (each
	// sub-Guard degrades to its zero value when its specific dep is
	// nil, matching the "best-effort status surface" UX).
	if deps.Store != nil {
		gh := &guardsHandler{
			store:          deps.Store,
			approvalMgr:    deps.ApprovalManager,
			scheduler:      deps.Scheduler,
			installMgr:     deps.InstallManager,
			hookInstaller:  deps.HookInstaller,
			sanitizer:      deps.Sanitizer,
			sandboxInstall: deps.SandboxInstall,
			settingsSvc:    deps.SettingsSvc,
			dsManager:      deps.Manager,
		}
		mux.HandleFunc("GET /api/v1/guards", gh.overview)
		mux.HandleFunc("GET /api/v1/guards/shell", gh.shellDetail)
		mux.HandleFunc("POST /api/v1/guards/shell/clients/{id}/install_hooks", gh.shellInstallHooks)
		mux.HandleFunc("POST /api/v1/guards/shell/clients/{id}/uninstall_hooks", gh.shellUninstallHooks)
		mux.HandleFunc("GET /api/v1/guards/sanitizer", gh.sanitizerDetail)
		mux.HandleFunc("PUT /api/v1/guards/sanitizer", gh.sanitizerUpdate)
		mux.HandleFunc("GET /api/v1/guards/sanitizer/denylist", gh.sanitizerDenylist)
		mux.HandleFunc("GET /api/v1/guards/schedule", gh.scheduleList)
		mux.HandleFunc("POST /api/v1/guards/schedule", gh.scheduleCreate)
		mux.HandleFunc("POST /api/v1/guards/schedule/{id}/run", gh.scheduleRun)
		mux.HandleFunc("DELETE /api/v1/guards/schedule/{id}", gh.scheduleDelete)
		mux.HandleFunc("GET /api/v1/guards/sandbox", gh.sandboxDetail)
		mux.HandleFunc("POST /api/v1/guards/sandbox/clients/{id}/enable", gh.sandboxEnable)
		mux.HandleFunc("POST /api/v1/guards/sandbox/clients/{id}/disable", gh.sandboxDisable)
		mux.HandleFunc("PUT /api/v1/guards/sandbox", gh.sandboxUpdate)
	}

	if deps.SettingsSvc != nil {
		sh := &settingsHandler{svc: deps.SettingsSvc, meshMgr: deps.MeshManager}
		mux.HandleFunc("GET /api/v1/settings", sh.get)
		mux.HandleFunc("PUT /api/v1/settings", sh.update)
	}

	// Workers (M0.6) — CRUD + lifecycle for in-process AI agents. Same
	// Service as the mcplexer__*_worker MCP tools. Optional: the routes
	// only register when the admin svc is wired (stdio-only smoke tests
	// don't need them).
	if deps.WorkerAdmin != nil {
		wh := &workersHandler{svc: deps.WorkerAdmin, settings: deps.SettingsSvc}
		mux.HandleFunc("GET /api/v1/delegations/model-capacity", wh.listDelegationModelCapacity)
		mux.HandleFunc("GET /api/v1/delegations", wh.listDelegations)
		mux.HandleFunc("POST /api/v1/delegations", wh.createDelegation)
		mux.HandleFunc("POST /api/v1/delegations/{id}/review", wh.reviewDelegation)
		mux.HandleFunc("GET /api/v1/workers", wh.list)
		mux.HandleFunc("POST /api/v1/workers", wh.create)
		// M2 — workspace-wide cost dashboard payload. Registered BEFORE
		// the {id} routes so the static path wins the longest-match
		// (Go's stdlib mux prefers specificity, but defensive ordering
		// avoids future surprises).
		mux.HandleFunc("GET /api/v1/workers/cost-aggregate", wh.costAggregate)
		mux.HandleFunc("GET /api/v1/workers/{id}", wh.get)
		mux.HandleFunc("PATCH /api/v1/workers/{id}", wh.update)
		mux.HandleFunc("DELETE /api/v1/workers/{id}", wh.remove)
		mux.HandleFunc("POST /api/v1/workers/{id}/pause", wh.pause)
		mux.HandleFunc("POST /api/v1/workers/{id}/resume", wh.resume)
		mux.HandleFunc("POST /api/v1/workers/{id}/run-now", wh.runNow)
		mux.HandleFunc("GET /api/v1/workers/{id}/runs", wh.listRuns)
		mux.HandleFunc("GET /api/v1/workers/{id}/runs/{run_id}", wh.getRun)
		mux.HandleFunc("POST /api/v1/workers/{id}/runs/{run_id}/cancel", wh.cancelRun)
		// M2 — live SSE for a single run. Polls every 1s and emits a
		// `status` event whenever the row changes; closes on terminal
		// status. Replaces the 5s poll on the detail page for in-flight
		// runs without invasive runner changes.
		mux.HandleFunc("GET /api/v1/workers/{id}/runs/{run_id}/events", wh.streamRun)
		// Cancel a stuck / orphaned run. Lives under /worker-runs/* so the
		// route is reachable by run-id alone (no parent worker required).
		mux.HandleFunc("POST /api/v1/worker-runs/{run_id}/cancel", wh.cancelRun)

		// M1 — propose-first approval surface.
		mux.HandleFunc("GET /api/v1/worker-approvals", wh.listApprovals)
		mux.HandleFunc("POST /api/v1/worker-approvals/{id}/approve", wh.approveApproval)
		mux.HandleFunc("POST /api/v1/worker-approvals/{id}/reject", wh.rejectApproval)

		// M4 — mesh-triggered workers: per-worker trigger CRUD +
		// per-peer trigger-grant convenience.
		wth := &workerTriggersHandler{svc: deps.WorkerAdmin}
		mux.HandleFunc("GET /api/v1/workers/{worker_id}/mesh-triggers", wth.listMeshTriggers)
		mux.HandleFunc("POST /api/v1/workers/{worker_id}/mesh-triggers", wth.createMeshTrigger)
		mux.HandleFunc("PATCH /api/v1/workers/{worker_id}/mesh-triggers/{trigger_id}", wth.updateMeshTrigger)
		mux.HandleFunc("DELETE /api/v1/workers/{worker_id}/mesh-triggers/{trigger_id}", wth.deleteMeshTrigger)
		mux.HandleFunc("POST /api/v1/peers/{peer_id}/trigger-grants", wth.grantTriggerToPeer)
		mux.HandleFunc("DELETE /api/v1/peers/{peer_id}/trigger-grants/{worker_name}", wth.revokeTriggerGrant)

		// Publishable worker templates live in the worker_templates table
		// (migration 057). Routes register only when BOTH WorkerAdmin
		// and WorkerTemplateRegistry are wired; either alone leaves the
		// surface dark.
		if deps.WorkerTemplateRegistry != nil {
			th := &workerTemplatesHandler{registry: deps.WorkerTemplateRegistry, svc: deps.WorkerAdmin}
			mux.HandleFunc("POST /api/v1/workers/{id}/publish", th.publish)
			mux.HandleFunc("GET /api/v1/worker-templates", th.list)
			mux.HandleFunc("POST /api/v1/worker-templates/install", th.install)
			mux.HandleFunc("GET /api/v1/worker-templates/{name}/{version}", th.get)
		}
	}

	// Model providers (Layer 2). Reusable provider profiles workers
	// reference by id. Routes register unconditionally — the surface is
	// safe to expose even when WorkerAdmin is nil (the workers UI links
	// to it as a stand-alone settings page).
	mph := &workersadmin.ModelProfileHandlers{Store: deps.Store}
	mux.HandleFunc("GET /api/v1/model-profiles", mph.List)
	mux.HandleFunc("POST /api/v1/model-profiles", mph.Create)
	mux.HandleFunc("GET /api/v1/model-profiles/{id}", mph.Get)
	mux.HandleFunc("PUT /api/v1/model-profiles/{id}", mph.Update)
	mux.HandleFunc("DELETE /api/v1/model-profiles/{id}", mph.Delete)

	// OpenCode (Layer 3) — managed subprocess and live model catalogue.
	if deps.OpenCode != nil {
		oc := &OpenCodeHandlers{Manager: deps.OpenCode}
		mux.HandleFunc("GET /api/v1/opencode/status", oc.Status)
		mux.HandleFunc("POST /api/v1/opencode/start", oc.Start)
		mux.HandleFunc("POST /api/v1/opencode/stop", oc.Stop)
		mux.HandleFunc("GET /api/v1/opencode/models", oc.Models)
	}

	rh := &refinementHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/descriptions", rh.list)
	mux.HandleFunc("GET /api/v1/descriptions/{id}", rh.get)
	mux.HandleFunc("POST /api/v1/descriptions/{id}/accept", rh.accept)
	mux.HandleFunc("POST /api/v1/descriptions/{id}/reject", rh.reject)
	mux.HandleFunc("POST /api/v1/descriptions", rh.submit)

	mh := &meshHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/mesh/status", mh.status)

	if deps.MeshManager != nil {
		msh := &meshSendHandler{mgr: deps.MeshManager}
		mux.HandleFunc("POST /api/v1/mesh/send", msh.send)
		mwh := &meshWaitHandler{mgr: deps.MeshManager}
		mux.HandleFunc("GET /api/v1/mesh/wait", mwh.wait)
	}

	// "Focus" — switch the user's local tmux to a mesh agent's pane.
	// Works for local agents (tmux switch-client) and peer-origin agents
	// (spawn a new local tmux window that SSHes into the peer).
	mfh := &meshFocusHandler{store: deps.Store}
	mux.HandleFunc("POST /api/v1/mesh/agents/{session_id}/focus", mfh.focus)

	// p2p identity is registered unconditionally so callers get a useful 501
	// in stub builds rather than a SPA HTML page. The handler guards on a
	// nil host internally.
	ph := &p2pHandler{host: deps.P2PHost, lookup: deps.P2PPeerLookup, reconnector: deps.P2PReconnector}
	mux.HandleFunc("GET /api/p2p/identity", ph.identity)
	mux.HandleFunc("GET /api/p2p/peers/{id}/status", ph.peerStatus)
	mux.HandleFunc("POST /api/p2p/connect", ph.connect)

	// p2p pairing routes — registered unconditionally for the same reason.
	pp := &p2pPairingHandler{svc: deps.P2PPairing, store: deps.Store, users: deps.Store, reconnector: deps.P2PReconnector}
	mux.HandleFunc("POST /api/p2p/pair/start", pp.pairStart)
	mux.HandleFunc("POST /api/p2p/pair/complete", pp.pairComplete)
	mux.HandleFunc("GET /api/p2p/peers", pp.listPeers)
	mux.HandleFunc("DELETE /api/p2p/peers/{id}", pp.revokePeer)
	mux.HandleFunc("PATCH /api/p2p/peers/{id}/ssh-target", pp.setSSHTarget)
	// JTAC65 — structured scope-denial probe. Returns 200 {allowed:true}
	// or 403 {error,denial:{code,scope,peer}}; callers use the typed
	// `denial.code` to distinguish no_scope / scope_revoked /
	// scope_out_of_band / cross_org_boundary.
	mux.HandleFunc("POST /api/p2p/peers/{id}/scopes/check", pp.checkScope)

	// M7.1 — per-human user identity endpoints.
	uh := &usersHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/users", uh.list)
	// Static sub-route registered before the parameterised /{id} variant
	// so the Go 1.22 mux picks the most-specific match first (mirrors the
	// tasks handler ordering for /tasks/stream vs /tasks/{id}).
	mux.HandleFunc("GET /api/v1/users/self", uh.self)
	mux.HandleFunc("GET /api/v1/users/{id}", uh.get)

	// Peer-scope registry — read-only catalog of scope strings the UI's
	// grant picker can offer. Wired statically; no dependencies needed.
	sh := &scopesHandler{}
	mux.HandleFunc("GET /api/v1/scopes", sh.handleList)

	// Agent rules (W1, skills-first epic) — marker-bounded mcplexer
	// block in ~/.claude/CLAUDE.md. Pure file I/O, no deps; registered
	// unconditionally so the dashboard tile always renders.
	RegisterAgentRulesRoutes(mux)

	// Tasks — operational per-workspace work items (migration 061).
	// Registers only when the tasks service is wired; otherwise the
	// dashboard sees 404s gracefully.
	if deps.TasksSvc != nil && deps.Store != nil {
		th := newTasksHandler(deps.TasksSvc, deps.Store)
		// Static sub-routes BEFORE parameterised /{id} variants so the
		// Go 1.22 mux picks the most-specific match first (mirrors the
		// memory handler ordering).
		mux.HandleFunc("GET /api/v1/tasks", th.handleList)
		mux.HandleFunc("GET /api/v1/tasks/stream", th.handleStream)
		mux.HandleFunc("GET /api/v1/tasks/count", th.handleCount)
		mux.HandleFunc("GET /api/v1/tasks/milestones", th.handleListMilestones)
		mux.HandleFunc("GET /api/v1/tasks/offers", th.handleListOffers)
		mux.HandleFunc("POST /api/v1/tasks/offers", th.handleCreateOffer)
		mux.HandleFunc("POST /api/v1/tasks/offers/{id}/accept", th.handleAcceptOffer)
		mux.HandleFunc("POST /api/v1/tasks/offers/{id}/decline", th.handleDeclineOffer)
		mux.HandleFunc("POST /api/v1/tasks", th.handleCreate)
		mux.HandleFunc("GET /api/v1/tasks/{id}", th.handleGet)
		mux.HandleFunc("POST /api/v1/tasks/{id}/update", th.handleUpdate)
		mux.HandleFunc("POST /api/v1/tasks/{id}/claim", th.handleClaim)
		mux.HandleFunc("POST /api/v1/tasks/{id}/heartbeat", th.handleHeartbeat)
		mux.HandleFunc("POST /api/v1/tasks/{id}/work_context", th.handleSetWorkContext)
		mux.HandleFunc("POST /api/v1/tasks/{id}/notes", th.handleAppendNote)
		mux.HandleFunc("GET /api/v1/tasks/{id}/notes", th.handleListNotes)
		mux.HandleFunc("DELETE /api/v1/tasks/{id}", th.handleDelete)
		mux.HandleFunc("GET /api/v1/task-status-vocabulary", th.handleListVocab)
		mux.HandleFunc("POST /api/v1/task-status-vocabulary", th.handleUpsertVocab)

		// C2.3 — attachment REST surface (multipart upload / streaming
		// download). Backs the dashboard's drag-drop attachment UI. The
		// MCP path inlines bodies up to 5 MiB; this REST path streams
		// up to taskAttachmentMaxBytes (25 MiB) so screenshots + generated
		// docs work without base64 overhead.
		ah := newTaskAttachmentHandler(deps.Store)
		mux.HandleFunc("POST /api/v1/tasks/{task_id}/attachments", ah.handleUpload)
		mux.HandleFunc("GET /api/v1/tasks/{task_id}/attachments", ah.handleListAttachments)
		mux.HandleFunc("GET /api/v1/attachments/{id}", ah.handleDownload)
		mux.HandleFunc("DELETE /api/v1/attachments/{id}", ah.handleDelete)
	}

	// Concierge chat-signal log (migration 080). Routes register only
	// when the concierge service is wired (always true in the daemon).
	if deps.ConciergeSvc != nil {
		csh := newChatSignalsHandler(deps.ConciergeSvc)
		mux.HandleFunc("GET /api/v1/chat-signals", csh.handleList)
		mux.HandleFunc("POST /api/v1/chat-signals/{id}/mark-promoted", csh.handleMarkPromoted)
	}

	// Concierge A/B leaderboard + lesson-pin (commit bc408ee shipped
	// the handler + tests but missed the router wiring — under test the
	// requests fell through to the SPA fallback and returned HTML).
	// Both routes need the memory service so the pin endpoint can
	// write through; gate the registration on both being wired.
	if deps.ConciergeSvc != nil && deps.MemorySvc != nil {
		ch := newConciergeHandler(deps.ConciergeSvc, deps.MemorySvc)
		mux.HandleFunc("GET /api/v1/concierge/ab/arms", ch.handleArms)
		mux.HandleFunc("POST /api/v1/concierge/lessons/pin", ch.handlePinLesson)
	}

	// Memory (cross-harness fact + note store). Registers only when the
	// memory service is wired. The handler also reaches into the store's
	// MemoryStore methods for the offer-management surface, so the Store
	// dependency must also be non-nil (always true in the daemon; tests
	// must pass it explicitly).
	if deps.MemorySvc != nil && deps.Store != nil {
		// deps.Auditor is *audit.Logger; passing a nil-typed pointer into
		// an interface arg would yield a non-nil-but-undereferenceable
		// interface, so explicitly normalise to a nil interface when the
		// concrete pointer is nil (the Go interface-nil gotcha).
		var memAuditor auditRecorder
		if deps.Auditor != nil {
			memAuditor = deps.Auditor
		}
		mem := newMemoryHandler(deps.MemorySvc, deps.Store, memAuditor)
		// Static sub-routes BEFORE the parameterised /{id} variants so
		// the Go 1.22 mux picks the most-specific match first.
		mux.HandleFunc("GET /api/v1/memory", mem.handleList)
		mux.HandleFunc("GET /api/v1/memory/count", mem.handleCount)
		// Brain-stats aggregate powers the memory landing header (shape
		// of the brain: totals, type mix, recency, sparkline, decay).
		// Lives in memory_stats_handler.go.
		mux.HandleFunc("GET /api/v1/memory/stats", mem.handleStats)
		// Graph view powers the /memory/graph dashboard visualisation.
		// Lives in memory_graph_handler.go to keep memory_handler.go
		// under 300 lines.
		gh := newMemoryGraphHandler(deps.MemorySvc)
		mux.HandleFunc("GET /api/v1/memory/graph", gh.handleGraph)
		mux.HandleFunc("POST /api/v1/memory", mem.handleCreate)
		mux.HandleFunc("POST /api/v1/memory/search", mem.handleSearch)
		mux.HandleFunc("POST /api/v1/memory/forget-by-source", mem.handleForgetBySource)
		mux.HandleFunc("GET /api/v1/memory/offers", mem.handleListOffers)
		mux.HandleFunc("POST /api/v1/memory/offers/{id}/accept", mem.handleAcceptOffer)
		mux.HandleFunc("POST /api/v1/memory/offers/{id}/decline", mem.handleDeclineOffer)
		// Entity-link surface (migration 076). Static /entities BEFORE the
		// parameterised /{id} variants so the mux picks specificity first.
		mux.HandleFunc("GET /api/v1/memory/entities", mem.handleListEntities)
		// Associative-recall surfaces (AR1, AR2, AR3).
		mux.HandleFunc("GET /api/v1/memory/entities/graph", mem.handleEntityGraph)
		mux.HandleFunc("GET /api/v1/memory/entities/{kind}/{id}/related", mem.handleRelatedEntities)
		mux.HandleFunc("GET /api/v1/memory/entities/{kind}/{id}/spreading", mem.handleSpreadingActivation)
		mux.HandleFunc("GET /api/v1/memory/{id}/entities", mem.handleListMemoryEntities)
		mux.HandleFunc("POST /api/v1/memory/{id}/entities", mem.handleLinkEntity)
		mux.HandleFunc("DELETE /api/v1/memory/{id}/entities", mem.handleUnlinkEntity)
		// AR4 + AR5 — learned-weight + suggestion surfaces.
		mux.HandleFunc("GET /api/v1/memory/{id}/co-recalled", mem.handleCoRecalled)
		mux.HandleFunc("GET /api/v1/memory/{id}/suggestions", mem.handleSuggestions)
		mux.HandleFunc("GET /api/v1/memory/{id}", mem.handleGet)
		mux.HandleFunc("POST /api/v1/memory/{id}/invalidate", mem.handleInvalidate)
		mux.HandleFunc("POST /api/v1/memory/{id}/pin", mem.handlePin)
		mux.HandleFunc("POST /api/v1/memory/{id}/unpin", mem.handleUnpin)
		mux.HandleFunc("DELETE /api/v1/memory/{id}", mem.handleDelete)

		// Memory consolidator surface — backs the /memory/consolidation page.
		// Routes register only when the worker admin service is wired (which
		// it always is in the daemon proper).
		if deps.WorkerAdmin != nil {
			ch := newConsolidateHandler(deps.WorkerAdmin, deps.Store)
			mux.HandleFunc("GET /api/v1/memory/consolidate/status", ch.handleStatus)
			mux.HandleFunc("POST /api/v1/memory/consolidate/enable", ch.handleEnable)
			mux.HandleFunc("POST /api/v1/memory/consolidate/disable", ch.handleDisable)
			mux.HandleFunc("POST /api/v1/memory/consolidate/run", ch.handleRunNow)
		}
	}

	if deps.SkillRegistry != nil {
		sr := &skillRegistryHandler{store: deps.Store, registry: deps.SkillRegistry}
		mux.HandleFunc("GET /api/v1/skill-registry", sr.list)
		mux.HandleFunc("GET /api/v1/skill-registry/search", sr.search)
		mux.HandleFunc("GET /api/v1/skill-registry/inventory", sr.inventory)
		mux.HandleFunc("POST /api/v1/skill-registry", sr.publish)
		mux.HandleFunc("GET /api/v1/skill-registry/{name}", sr.get)
		mux.HandleFunc("DELETE /api/v1/skill-registry/{name}", sr.delete)
		mux.HandleFunc("GET /api/v1/skill-registry/{name}/versions", sr.versions)
		mux.HandleFunc("GET /api/v1/skill-registry/{name}/bundle/files", sr.bundleFileIndex)
		mux.HandleFunc("GET /api/v1/skill-registry/{name}/bundle/file-content", sr.bundleFileContent)
		mux.HandleFunc("GET /api/v1/skill-registry/{name}/diff", sr.versionDiff)
		mux.HandleFunc("POST /api/v1/skill-registry/{name}/tags", sr.setTag)
		// W5 — local skills migration endpoints back the dashboard tile.
		sm := &skillMigrationHandler{registry: deps.SkillRegistry}
		mux.HandleFunc("GET /api/v1/skills/local-unpublished", sm.listLocalUnpublished)
		mux.HandleFunc("POST /api/v1/skills/import", sm.importLocalSkill)
		// W3 — refinement proposal inbox. SkillRefinementStore is part
		// of the universal store surface; the routes are gated behind
		// SkillRegistry presence because a deployment without a skill
		// registry has nothing to refine.
		srf := &skillRefinementHandler{store: deps.Store}
		mux.HandleFunc("GET /api/v1/skill-refinements", srf.list)
		mux.HandleFunc("GET /api/v1/skill-refinements/{id}", srf.get)
		mux.HandleFunc("POST /api/v1/skill-refinements/{id}/resolve", srf.resolve)
	}

	if deps.SkillRunner != nil {
		root := deps.SkillsRoot
		if root == nil {
			root = defaultSkillsRoot
		}
		sk := &skillHandler{runner: deps.SkillRunner, root: root}
		mux.HandleFunc("POST /api/skills/{id}/run", sk.run)
	}

	// W2 — skill telemetry endpoints (always-on; the store interface is
	// universal). Independent of SkillRegistry — runs can exist for
	// skills that were never published if the agent passed a name.
	srun := &skillRunsHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/skills/{name}/runs", srun.listForSkill)
	mux.HandleFunc("GET /api/v1/skill-runs/{id}", srun.get)

	// W6 — reputation stats (aggregator over W2 runs) + composition graph
	// (built from W4 produces/consumes manifest extras). Stats register
	// unconditionally; the graph route requires SkillRegistry (no skills
	// = nothing to graph). Static /skills/stats path comes BEFORE the
	// /skills/{name}/stats parameterised path so the Go 1.22 mux picks
	// the most-specific match for batch requests.
	sstats := &skillStatsHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/skills/stats", sstats.getBatch)
	mux.HandleFunc("GET /api/v1/skills/{name}/stats", sstats.getForSkill)
	if deps.SkillRegistry != nil {
		sgraph := &skillGraphHandler{store: deps.Store, registry: deps.SkillRegistry}
		mux.HandleFunc("GET /api/v1/skills/graph", sgraph.handleGraph)
	}

	// Telegram routes: always register read-only + token-storage endpoints so
	// the UI can populate before a token is set + server restarted. Handlers
	// that require a live client return a 503 when no client is attached
	// rather than falling through to the SPA HTML.
	if deps.Encryptor != nil {
		sm := secrets.NewManager(deps.Store, deps.Encryptor)
		if deps.Auditor != nil {
			sm.SetAuditor(deps.Auditor)
		}
		th := &telegramHandler{manager: deps.TelegramManager, store: deps.Store, secrets: sm}
		mux.HandleFunc("GET /api/v1/telegram/status", th.status)
		mux.HandleFunc("POST /api/v1/telegram/token", th.storeToken)
		mux.HandleFunc("POST /api/v1/telegram/pairings", th.createPairing)
		mux.HandleFunc("GET /api/v1/telegram/chats", th.listChats)
		mux.HandleFunc("DELETE /api/v1/telegram/chats/{id}", th.deleteChat)
		mux.HandleFunc("PATCH /api/v1/telegram/chats/{id}", th.updateChatPriority)
		mux.HandleFunc("POST /api/v1/telegram/test-message", th.testMessage)

		// Google Chat bridge — symmetrical surface. Read-only + token-store
		// endpoints always register so the dashboard can populate before a
		// service account JSON is configured + server restarted. The webhook
		// + test-send endpoints require a live client and return 503 if the
		// manager has no client attached.
		gh := &googleChatHandler{
			manager:     deps.GoogleChatManager,
			store:       deps.Store,
			secrets:     sm,
			jwtVerifier: deps.GoogleChatJWTVerifier,
		}
		mux.HandleFunc("GET /api/v1/googlechat/status", gh.status)
		mux.HandleFunc("POST /api/v1/googlechat/token", gh.storeToken)
		mux.HandleFunc("GET /api/v1/googlechat/spaces", gh.listSpaces)
		mux.HandleFunc("PATCH /api/v1/googlechat/spaces/{id}", gh.updateSpace)
		mux.HandleFunc("DELETE /api/v1/googlechat/spaces/{id}", gh.deleteSpace)
		mux.HandleFunc("POST /api/v1/googlechat/pairings", gh.createPairing)
		mux.HandleFunc("POST /api/v1/googlechat/test-message", gh.testMessage)
		mux.HandleFunc("POST /api/v1/googlechat/events", gh.events)

		// Hammerspoon installer + probe. Wired alongside the telegram /
		// googlechat handlers because all three need the same secrets
		// manager (auth-scope-backed credentials) and audit logger. The
		// HammerspoonManager dep is *optional* — the snippet + installer
		// endpoints work without a live bridge, and the probe degrades
		// to "bridge not configured" rather than 503'ing the route.
		hsH := &hammerspoonHandler{
			manager: deps.HammerspoonManager,
			store:   deps.Store,
			secrets: sm,
			auditor: deps.Auditor,
		}
		mux.HandleFunc("GET /api/v1/hammerspoon/snippet", hsH.snippet)
		mux.HandleFunc("POST /api/v1/hammerspoon/install", hsH.install)
		mux.HandleFunc("POST /api/v1/hammerspoon/probe", hsH.probe)
	}

	disc := &discoverHandler{manager: deps.Manager, store: deps.Store, addonReg: deps.AddonRegistry}
	mux.HandleFunc("POST /api/v1/downstreams/{id}/discover", disc.discover)

	// Server catalog — the full list of well-known MCP servers (embedded
	// defaults or fetched from MCPLEXER_CATALOG_URL). Always registered so
	// the SPA can populate the "available servers" grid even when the
	// service is nil (returns embedded defaults).
	if deps.CatalogSvc != nil {
		cat := &catalogHandler{svc: deps.CatalogSvc}
		mux.HandleFunc("GET /api/v1/catalog", cat.list)
	}

	// Flat tools catalogue — backs the workers editor's checkbox grid.
	// Reads CapabilitiesCache off each downstream; no live tools/list.
	tools := &toolsHandler{store: deps.Store}
	mux.HandleFunc("GET /api/v1/tools", tools.list)

	// Custom MCP addon authoring (M6.2 phase 1) + OpenAPI import (M6.3) + OAuth wizard / preview pane (M6.4).
	addonH := &addonHandler{
		creator:     deps.AddonCreator,
		previewExec: deps.AddonPreview,
		wizard:      deps.OAuthWizard,
		previewLim:  newPreviewRateLimiter(20, time.Minute),
	}
	mux.HandleFunc("POST /api/v1/addons/preview", addonH.preview)
	mux.HandleFunc("POST /api/v1/addons", addonH.create)
	mux.HandleFunc("POST /api/v1/addons/import-openapi", addonH.importOpenAPI)
	mux.HandleFunc("POST /api/v1/addons/preview-call", addonH.previewCall)
	mux.HandleFunc("POST /api/v1/addons/oauth-setup", addonH.oauthSetup)

	if deps.FlowManager != nil {
		dOAuth := &downstreamOAuthHandler{
			store:       deps.Store,
			flowManager: deps.FlowManager,
			manager:     deps.Manager,
		}
		mux.HandleFunc("POST /api/v1/downstreams/{id}/oauth-setup", dOAuth.setup)
		mux.HandleFunc("GET /api/v1/downstreams/{id}/oauth-status", dOAuth.status)

		dc := &downstreamConnectHandler{
			store:       deps.Store,
			flowManager: deps.FlowManager,
			encryptor:   deps.Encryptor,
		}
		mux.HandleFunc("POST /api/v1/downstreams/{id}/connect", dc.connect)
		mux.HandleFunc("GET /api/v1/downstreams/{id}/oauth-capabilities", dc.capabilities)
	}

	op := &oauthProviderHandler{svc: deps.ConfigSvc, store: deps.Store, encryptor: deps.Encryptor}
	mux.HandleFunc("GET /api/v1/oauth-providers", op.list)
	mux.HandleFunc("POST /api/v1/oauth-providers", op.create)
	mux.HandleFunc("GET /api/v1/oauth-providers/{id}", op.get)
	mux.HandleFunc("PUT /api/v1/oauth-providers/{id}", op.update)
	mux.HandleFunc("DELETE /api/v1/oauth-providers/{id}", op.delete)
	mux.HandleFunc("GET /api/v1/oauth-templates", op.listTemplates)

	oidc := &oidcDiscoverHandler{}
	mux.HandleFunc("POST /api/v1/oauth-providers/discover", oidc.discover)

	if deps.FlowManager != nil {
		of := &oauthFlowHandler{
			flow:      deps.FlowManager,
			store:     deps.Store,
			opStore:   deps.Store,
			encryptor: deps.Encryptor,
		}
		mux.HandleFunc("GET /api/v1/auth-scopes/{id}/oauth/authorize", of.authorize)
		mux.HandleFunc("GET /api/v1/oauth/callback", of.callback)
		mux.HandleFunc("GET /api/v1/auth-scopes/{id}/oauth/status", of.status)
		mux.HandleFunc("POST /api/v1/auth-scopes/{id}/oauth/revoke", of.revoke)
		mux.HandleFunc("POST /api/v1/auth-scopes/oauth-quick-setup", of.quickSetup)
	}

	// SPA fallback: serve embedded static files. The handler also sets the
	// session cookie when an HTML document is served so that subsequent API
	// fetches from the SPA carry authentication automatically.
	distFS, err := fs.Sub(web.StaticFiles, "dist")
	if err == nil {
		_, err = fs.Stat(distFS, "index.html")
	}
	if err == nil {
		spaHandler := spaFallback(distFS, http.FileServerFS(distFS), deps.APIToken)
		mux.Handle("/", spaHandler)
	}

	// Apply middleware chain around API + SPA handlers. Order is outermost
	// last: a request flows through requestID -> logging -> securityHeaders
	// -> browserOriginProtection -> apiTokenAuth -> requestBodyLimit ->
	// requireJSONContentType -> cors -> mux.
	var handler http.Handler = mux
	handler = corsMiddleware(deps.TrustedHosts)(handler)
	handler = requireJSONContentTypeMiddleware(handler)
	handler = requestBodyLimitMiddleware(handler)
	if deps.APIToken != "" {
		handler = apiTokenAuthMiddleware(deps.APIToken)(handler)
	}
	handler = browserOriginProtectionMiddleware(deps.TrustedHosts)(handler)
	handler = securityHeadersMiddleware(handler)
	handler = loggingMiddleware(handler)
	handler = requestIDMiddleware(handler)

	return handler
}

// spaFallback serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a real file (SPA client-side
// routing). When an HTML document is served, the session cookie is set so
// the SPA's subsequent fetches authenticate against the API automatically.
//
// Caching strategy:
//
//   - HTML responses (index.html + SPA fallbacks) get `Cache-Control:
//     no-store` — every page load must re-fetch the index so the
//     latest hashed asset references are picked up. Stale HTML is the
//     entire reason "the dashboard isn't loading" after an upgrade —
//     the browser hangs onto a previous index that points at JS
//     bundles that no longer exist.
//   - /assets/* responses use content-hashed filenames (vite emits
//     `index-<hash>.js`), so they're safe to cache long. We set
//     `public, max-age=31536000, immutable` to take them entirely out
//     of the revalidation path.
func spaFallback(staticFS fs.FS, fileServer http.Handler, apiToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path and check if the file exists in the embedded FS.
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(staticFS, p); err == nil {
			if isHTMLPath(p) {
				w.Header().Set("Cache-Control", "no-store, must-revalidate")
				if apiToken != "" {
					setSessionCookie(w, apiToken)
				}
			} else if p == "sw.js" {
				// The browser must always revalidate sw.js so a bumped
				// version number is picked up on the next navigation.
				// Without no-cache the OS/network cache can silently
				// serve a stale sw.js and keep the old CACHE_NAME alive
				// across daemon restarts.
				w.Header().Set("Cache-Control", "no-cache")
			} else if strings.HasPrefix(p, "assets/") {
				// Vite emits content-hashed filenames in /assets/.
				// Filename change = new URL, so long-term caching is
				// safe and cuts a meaningful chunk of refresh latency
				// for users coming back to the tab.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Missing /assets/* MUST NOT fall back to index.html. The browser
		// receives the SPA HTML on a `<script type="module">` tag, treats
		// it as a strict-MIME mismatch and either silently fails or — far
		// worse — leaves the module-graph promise unresolved, hanging the
		// whole page load. This is the production hazard after any
		// `make upgrade` that bumps Vite's content hash: every stale tab
		// references the old chunk, asks for it, gets HTML, hangs.
		// 404 fast so the browser surfaces a clean error and the user
		// either refreshes (picking up the new hash) or the SW kicks in.
		if strings.HasPrefix(p, "assets/") || p == "sw.js" || p == "manifest.webmanifest" ||
			strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") ||
			strings.HasSuffix(p, ".map") || strings.HasSuffix(p, ".png") ||
			strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".ico") {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		// Other unmatched paths are SPA client-side routes — serve index.html.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		if apiToken != "" {
			setSessionCookie(w, apiToken)
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

func isHTMLPath(p string) bool {
	if p == "" || p == "index.html" {
		return true
	}
	return strings.HasSuffix(p, ".html")
}
