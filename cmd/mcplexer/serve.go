package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/api"
	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/auth"
	"github.com/don-works/mcplexer/internal/backup"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/control"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/hammerspoon"
	"github.com/don-works/mcplexer/internal/index"
	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/lmstudio"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/memory/harnessimport"
	"github.com/don-works/mcplexer/internal/memsync"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/opencode"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/readiness"
	"github.com/don-works/mcplexer/internal/replication"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/sandbox"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/scheduler/triggers"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/telegram"
	"github.com/don-works/mcplexer/internal/usage"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
	"github.com/don-works/mcplexer/internal/workers/schedulebridge"
	"github.com/don-works/mcplexer/internal/workertemplates"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // h2c keeps loopback SSE streams multiplexed on current supported Go.
	"golang.org/x/sync/errgroup"
)

// mcplexerVersion is surfaced via /api/v1/health and recorded in backup
// manifests. It is derived from build-time VCS metadata rather than bumped by
// hand.
var mcplexerVersion = resolveMCPlexerVersion()

func cmdServe(args []string) error {
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := applyFlags(cfg, args); err != nil {
		return err
	}

	// Stdio cold-start fix: when the launchd daemon is reachable on its
	// Unix socket, transparently forward stdin/stdout through it instead
	// of building a fresh in-process downstream MCP stack. Saves the
	// 3-7s "list tools slow" cold-start the user otherwise pays per MCP
	// session. This MUST happen before opening the SQLite database —
	// migrations + the WAL lock both add latency we can't afford on the
	// proxy path, and the proxy itself reads nothing local.
	//
	// findDaemonSocket caps the probe at 250ms; missing-daemon installs
	// (headless boxes, `dry-run`, fresh setup, tests) fall straight
	// through to the in-process stdio path below.
	if cfg.Mode == "stdio" {
		if proxied, perr := tryStdioProxy(ctx); proxied {
			return perr
		}
	}

	// Ensure data directory exists before opening the database.
	if dir := filepath.Dir(cfg.DBDSN); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}

	// Log writer routes through lumberjack rotation when LogPath is set;
	// then wrap the JSON handler with audit.ContextHandler so every record
	// produced via the *Context-flavoured log methods auto-stamps the
	// correlation_id from ctx. Deep call sites stay clean — they just
	// need to use ctx-aware slog methods and the join key appears for free.
	logWriter := buildSlogWriter(cfg.LogPath, loadLogRotationConfig(cfg.ConfigFile))
	base := slog.NewJSONHandler(logWriter, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})
	logger := slog.New(audit.NewContextHandler(base))
	slog.SetDefault(logger)

	// macOS Accessibility: long-lived daemon mode only. Short-lived
	// stdio sessions (one per MCP client connection) MUST NOT prompt —
	// it would spam the user on every fresh claude/cursor session. The
	// real implementation is in accessibility_darwin.go (cgo against
	// ApplicationServices); accessibility_other.go is a no-op on every
	// other platform and on no-cgo darwin cross-compiles.
	if cfg.Mode == "http" {
		requestAccessibility(logger)
	}

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		if isDBLockedError(err) {
			return fmt.Errorf("open database: %w\n  Database is locked. Another mcplexer process may be using it.\n  Try: mcplexer daemon stop && mcplexer serve", err)
		}
		return fmt.Errorf("open database (%s): %w; check that the path is writable and disk space is available", cfg.DBDSN, err)
	}
	defer func() { _ = db.Close() }()

	if err := config.SeedDefaultWorkspaces(ctx, db); err != nil {
		return err
	}
	if err := config.SeedDefaultOAuthProviders(ctx, db); err != nil {
		return err
	}
	if err := config.SeedDefaultDownstreamServers(ctx, db); err != nil {
		return err
	}
	if err := config.SeedDefaultAuthScopes(ctx, db); err != nil {
		return err
	}
	if err := config.SeedDefaultRouteRules(ctx, db); err != nil {
		return err
	}

	// Mark all sessions as disconnected on startup. Any session still
	// showing as connected is stale from a prior process (crash, SIGKILL,
	// or unclean shutdown). Their MCP clients must reconnect.
	if n, err := db.DisconnectAllSessions(ctx); err != nil {
		slog.Warn("failed to disconnect stale sessions", "error", err)
	} else if n > 0 {
		slog.Info("disconnected stale sessions from prior run", "count", n)
	}

	// Purge stale LOCAL mesh agents so dead in-process sessions don't
	// linger as duplicates. Peer-origin agents are NOT purged — they're
	// repopulated by libp2p gossip, which can lag minutes after a
	// restart. Wiping them on restart used to mean the agent panel
	// showed an empty peer list until gossip caught up, even though
	// peer messages still arrived. (User-reported regression on the
	// 23:17 upgrade.)
	if _, err := db.DeleteMeshAgentsByOrigin(ctx, store.MeshAgentOriginLocal); err != nil {
		slog.Warn("failed to purge stale local mesh agents", "error", err)
	}

	// Interrupt worker_runs left in status="running" or "dispatched"
	// by the prior process. A run that finalises cleanly stamps its
	// terminal state before the process exits, so any survivor is by
	// definition an orphan — the in-process runner that owned it is
	// gone. Status 'interrupted' is distinct from 'failure' so
	// dashboards separate daemon-restart casualties from genuine bugs.
	// Delegation workers are excluded — ResumeOrphanedDelegations
	// handles those separately.
	// startedBefore is now to leave post-boot rows untouched.
	bootTime := time.Now().UTC()
	if n, err := db.ReapOrphanedRunningRuns(ctx, bootTime, bootTime); err != nil {
		slog.Warn("failed to interrupt orphaned worker_runs", "error", err)
	} else if n > 0 {
		slog.Info("interrupted orphaned worker_runs from prior process", "count", n)
	}

	// Load YAML config into store if file exists. The retention block
	// is re-read inside buildServerDeps when the scheduler is wired —
	// keeping it in one place avoids passing extra state through the
	// helper boundaries.
	if cfg.ConfigFile != "" {
		if _, err := os.Stat(cfg.ConfigFile); err == nil {
			fileCfg, err := config.LoadFile(cfg.ConfigFile)
			if err != nil {
				return err
			}
			if err := config.Apply(ctx, db, fileCfg); err != nil {
				return err
			}
			logger.Info("loaded config", "file", cfg.ConfigFile)
		}
	}

	cfgSvc := config.NewService(db)
	settingsSvc := config.NewSettingsService(db)

	dataDir := filepath.Dir(cfg.DBDSN)
	api.SetSystemInfo(api.SystemInfo{
		Mode:          cfg.Mode,
		Version:       mcplexerVersion,
		HTTPAddr:      cfg.HTTPAddr,
		PublicURL:     cfg.PublicURL,
		SocketPath:    cfg.SocketPath,
		DataDir:       dataDir,
		ConfigFile:    cfg.ConfigFile,
		LogPath:       filepath.Join(dataDir, "mcplexer.log"),
		AddonsDir:     filepath.Join(dataDir, "addons"),
		P2PEnabled:    cfg.P2PEnabled,
		ServerProfile: cfg.ServerProfile,
		TrustedHosts:  cfg.TrustedHosts,
		Capabilities:  serverCapabilities(cfg.ServerProfile),
	})

	switch cfg.Mode {
	case "stdio":
		// Daemon-proxy fast-path was attempted (and either committed or
		// declined) at the top of cmdServe before the DB was opened.
		// Reaching here means we fell back to in-process stdio — log it
		// so the user can tell from `mcplexer.log` which path served.
		logger.Info("starting in stdio mode (in-process — no daemon socket reachable)")
		return runStdio(ctx, cfg, db, settingsSvc)
	case "http":
		return runServer(ctx, cfg, db, cfgSvc, settingsSvc, cfg.SocketPath != "")
	default:
		return fmt.Errorf("unknown mode: %q", cfg.Mode)
	}
}

// applyFlags parses --mode=X flags from the args list.
func applyFlags(cfg *Config, args []string) error {
	for _, arg := range args {
		if len(arg) > 7 && arg[:7] == "--mode=" {
			cfg.Mode = arg[7:]
		}
		if len(arg) > 7 && arg[:7] == "--addr=" {
			cfg.HTTPAddr = arg[7:]
		}
		if len(arg) > 9 && arg[:9] == "--socket=" {
			cfg.SocketPath = arg[9:]
		}
		if arg == "--p2p" {
			cfg.P2PEnabled = true
		}
		if len(arg) > 15 && arg[:15] == "--p2p-identity=" {
			cfg.P2PIdentityPath = arg[15:]
		}
		if len(arg) > 10 && arg[:10] == "--profile=" {
			profile, err := normalizeServerProfile(arg[10:])
			if err != nil {
				return err
			}
			cfg.ServerProfile = profile
		}
		if len(arg) > 17 && arg[:17] == "--server-profile=" {
			profile, err := normalizeServerProfile(arg[17:])
			if err != nil {
				return err
			}
			cfg.ServerProfile = profile
		}
	}
	return nil
}

// startP2P boots the embedded libp2p host if cfg.P2PEnabled is true. Returns
// (nil, nil) when disabled — callers must treat a nil host as "feature off".
//
// In binaries built without `-tags p2p` this is a stub: when P2PEnabled is
// true it returns ErrP2PNotBuiltIn so the daemon can log a helpful error and
// keep running. When P2PEnabled is false the call is identical in both
// modes (a no-op).
//
// The on-disk identity key is encrypted with age (via enc) and written to
// {IdentityPath}.age. Pass nil enc to fall back to cleartext at IdentityPath.
// serverDeps is the bundle of singletons the HTTP API and Unix-socket
// gateway both need. Built once by buildServerDeps and consumed by
// runServer in either http-only or http+socket mode.
type serverDeps struct {
	authInj           *auth.Injector
	flow              *oauth.FlowManager
	enc               *secrets.AgeEncryptor
	secretsMgr        *secrets.Manager
	apiToken          string
	engine            *routing.Engine
	manager           *downstream.Manager
	cachingLister     gateway.CachingCaller
	toolCache         *cache.ToolCache
	approvalBus       *approval.Bus
	approvalMgr       *approval.Manager
	notifyBus         *notify.Bus
	notifyStore       notify.Store
	notifyPushStore   notify.PushStore
	auditBus          *audit.Bus
	auditor           *audit.Logger
	sessionBus        *session.Bus
	addonReg          *addon.Registry
	addonExec         *addon.Executor
	addonCreator      *addon.Creator
	installMgr        *install.Manager
	meshMgr           *mesh.Manager
	telegramMgr       *telegram.Manager
	googleChatMgr     *googlechat.Manager
	hammerspoonMgr    *hammerspoon.Manager
	googleChatJWT     *googlechat.JWTVerifier
	p2pHost           *p2p.Host
	p2pPairing        *p2p.PairingService
	p2pLookup         *p2p.SQLPeerLookup
	p2pReconnector    *p2p.Reconnector
	p2pLiveness       *p2p.LivenessMonitor
	collaboration     *collaboration.Manager
	skillShare        *p2p.SkillShareService
	registryShare     *p2p.RegistryShareService
	skillRegistry     *skillregistry.Registry
	workerTplRegistry *workertemplates.Registry
	memorySvc         *memory.Service
	memoryShare       *p2p.MemoryShareService
	attachmentShare   *p2p.AttachmentShareService
	taskSync          *p2p.TaskSyncService
	taskSyncScheduler *taskSyncScheduler
	// replicator silently fans local memory writes + skill installs to
	// Tier-1 same-user paired peers. nil = auto-replication disabled
	// (slim build or wiring failure); the manual share path still works.
	replicator      *replication.Coordinator
	tasksSvc        *tasks.Service
	conciergeSvc    *concierge.Service
	secretPromptMgr *ephemeral.Manager
	secretPromptBus *ephemeral.Bus
	backupSvc       *backup.Service
	usageSvc        *usage.Service

	// v0.13.0 — mesh__send_secret transfer keypair. nil = receive half of
	// peer-to-peer secret transfer is disabled (sender side still works
	// for outgoing-only flows, but receive will fail with "transfer key
	// not configured" until this is wired).
	secretTransferKey *age.X25519Identity

	// selfUser is the local users.is_self=1 row populated at boot by
	// config.BootstrapSelfUser. Used by the consent resolver to compare
	// self.user_id against a peer's user_id when classifying tier.
	selfUser *store.User

	// consentResolver classifies the trust tier + auto-pair status of a
	// peer at share-time. Wired into the skill / memory / task share
	// audit adapters so every cross-boundary audit row carries the
	// consent envelope demanded by epic 01KSK91Q4W8TNED9MAF0CTRVKC.
	// Falls back to consent.NopResolver{} (always cross_org) when the
	// store hasn't yet bootstrapped the local user row.
	consentResolver consent.Resolver

	// Guards (M0-M5)
	scheduler       *scheduler.Scheduler
	hookInstaller   *install.HookInstaller
	sanitizer       *sanitize.Denylist
	sandboxInstall  *sandbox.Installer
	fileWatcher     *triggers.FileWatcher
	brainWatcher    *brain.Watcher
	brainSerial     *brain.Serializer
	brainIndexer    *brain.Indexer
	brainEditor     *brain.Editor
	brainCfg        brain.Config
	brainGit        *brain.Git
	brainAutoCommit *brain.AutoCommitter
	codeIndex       *index.Service
	// brainRepoDiscovery is the per-repo .mcplexer/ discovery callback (M6 —
	// federation). Nil when the brain is off. Threaded into every gateway
	// session so a session bound to a repo with a .mcplexer/ folder registers
	// it with the indexer/watcher dynamically.
	brainRepoDiscovery func(ctx context.Context, clientRoot, workspaceID string)

	// Brain secrets (M3) — SOPS+age recipients + key file path, set when
	// the brain is enabled. Threaded into the brain_migrate_secrets admin
	// tool. Empty when brain disabled.
	brainAgeRecipients []string
	brainAgeKeyFile    string

	// Workers (M0)
	workerRunner     *runner.Runner
	workerAdmin      *workersadmin.Service
	workerRunBus     *runner.RunBus
	workerDispatcher *toolDispatcher // kept so the gateway-backed builtin caller can be wired post-construction
	workerGateway    *gateway.Server // dedicated gateway.Server for worker-bound CallTool; session stays uninitialized

	// Layer 3 — built-in OpenCode subprocess. Nil when binary is not on
	// PATH; the /opencode/* routes only register when present.
	opencode *opencode.Manager

	// cleanups runs in reverse order (LIFO) when runServer exits.
	cleanups []func()
}

func (d *serverDeps) defer_(fn func()) {
	d.cleanups = append(d.cleanups, fn)
}

func (d *serverDeps) shutdown() {
	for i := len(d.cleanups) - 1; i >= 0; i-- {
		d.cleanups[i]()
	}
}

// buildServerDeps assembles every singleton needed by the http and socket
// servers. The returned cleanup must be invoked (LIFO) by the caller before
// process exit.
func buildServerDeps(ctx context.Context, cfg *Config, db *sqlite.DB, settingsSvc *config.SettingsService) (*serverDeps, error) {
	d := &serverDeps{}
	d.backupSvc = backup.New(filepath.Dir(cfg.DBDSN), cfg.DBDSN, mcplexerVersion)

	authInj, fm, enc, secretsMgr, err := buildAuthInjector(cfg, db)
	if err != nil {
		return nil, err
	}
	d.authInj, d.flow, d.enc, d.secretsMgr = authInj, fm, enc, secretsMgr
	d.usageSvc = buildUsageService(db, secretsMgr)

	// v0.13.0 — load the dedicated age X25519 transfer keypair used for
	// peer→peer secret transfer (`mesh__send_secret`). Best-effort: a
	// failure here only disables the receive half of secret transfer;
	// it must not block the rest of the daemon.
	if enc != nil {
		xferPath := filepath.Join(filepath.Dir(cfg.DBDSN), "secret-transfer.age.key")
		if k, err := secrets.LoadOrCreateTransferKey(xferPath, enc); err == nil {
			d.secretTransferKey = k
		} else {
			slog.Warn("failed to load secret-transfer key (mesh__send_secret receive disabled)",
				"path", xferPath, "error", err)
		}
	}

	apiToken, err := auth.LoadOrCreateAPIToken(cfg.APITokenPath)
	if err != nil {
		return nil, fmt.Errorf("load api token: %w", err)
	}
	slog.Info("api token loaded", "path", cfg.APITokenPath)
	d.apiToken = apiToken

	resolveP2PEnabled(ctx, cfg, settingsSvc)
	d.p2pHost = mustStartP2P(ctx, cfg, enc)
	if d.p2pHost != nil {
		host := d.p2pHost
		d.defer_(func() { _ = host.Close() })
	}
	d.p2pPairing = buildPairingService(d.p2pHost, db)
	selfUser, suErr := config.BootstrapSelfUser(ctx, db, db)
	if suErr != nil {
		slog.Warn("bootstrap self user failed", "error", suErr)
	}
	d.selfUser = selfUser
	inviteSvc := p2p.NewCollaborationInviteService(d.p2pHost, db)
	d.collaboration = collaboration.NewManager(db, inviteSvc)
	if selfUser != nil {
		if _, ownerErr := d.collaboration.EnsureLocalOwner(ctx, selfUser); ownerErr != nil {
			slog.Warn("bootstrap collaboration owner failed", "error", ownerErr)
		} else if d.p2pHost != nil {
			if _, shareErr := d.collaboration.EnsureWorkspaceShares(ctx); shareErr != nil {
				slog.Warn("bootstrap collaboration workspace shares failed", "error", shareErr)
			}
		}
	}
	d.consentResolver = newConsentResolver(db, selfUser)
	attachSelfIdentity(d.p2pPairing, db, selfUser)
	if d.p2pPairing != nil {
		d.p2pPairing.SetDisplayNameProvider(makeDisplayNameProvider(ctx, settingsSvc))
	}
	d.p2pLookup = p2p.NewSQLPeerLookup(db.Raw(), slog.Default())
	p2pDiscovery := p2p.NewDiscoveryService(d.p2pHost, d.p2pLookup, slog.Default())
	d.defer_(func() { _ = p2pDiscovery.Close() })
	d.p2pReconnector = p2p.NewReconnector(d.p2pHost, d.p2pLookup, 0, slog.Default())
	d.p2pLiveness = p2p.NewLivenessMonitor(d.p2pHost, d.p2pLookup, db, slog.Default())
	if d.p2pLiveness != nil {
		d.p2pReconnector.SetLivenessOracle(d.p2pLiveness)
		// Reverse hook: successful pings stamp reconnect_state="connected"
		// so the UI badge never gets stuck at "Searching" while last_seen
		// is being refreshed by liveness.
		d.p2pLiveness.SetReconnectMarker(d.p2pReconnector)
		d.p2pLiveness.Start(ctx)
		d.defer_(d.p2pLiveness.Close)
	}
	d.p2pReconnector.Start(ctx)
	d.defer_(d.p2pReconnector.Close)

	d.engine = routing.NewEngine(db)
	d.manager = downstream.NewManager(db, d.authInj)
	if d.flow != nil {
		d.manager.SetAuthInvalidationHook(d.flow.RevokeToken)
	}
	mgr := d.manager
	d.defer_(func() { _ = mgr.Shutdown(ctx) })

	// Built-in OpenCode subprocess (Layer 3). Constructed unconditionally
	// so the /opencode/status endpoint can report "not installed" without
	// the daemon caring whether the binary exists. Auto-start is gated by
	// MCPLEXER_OPENCODE_AUTOSTART=1 — opt-in because spawning a server
	// process at every daemon launch is a footgun for users who don't use
	// OpenCode.
	d.opencode = opencode.NewManager(opencode.Options{
		// MCPLEXER_OPENCODE_PATH lets users point at a binary outside
		// launchd's stripped-down PATH (the macOS daemon doesn't see
		// ~/.opencode/bin or ~/bin without explicit configuration).
		BinaryPath: os.Getenv("MCPLEXER_OPENCODE_PATH"),
		AutoStart:  os.Getenv("MCPLEXER_OPENCODE_AUTOSTART") == "1",
	})
	ocm := d.opencode
	d.defer_(func() { _ = ocm.Stop() })
	if os.Getenv("MCPLEXER_OPENCODE_AUTOSTART") == "1" {
		go func() {
			if err := ocm.Start(ctx); err != nil && !errors.Is(err, opencode.ErrNotInstalled) {
				slog.Warn("opencode autostart failed", "error", err)
			}
		}()
	}

	d.toolCache = buildToolCache(ctx, db)
	d.cachingLister = cache.NewCachingToolLister(d.manager, d.toolCache)

	d.approvalBus = approval.NewBus()
	d.approvalMgr = approval.NewManager(db, d.approvalBus)
	d.approvalMgr.ExpireStale(ctx)
	d.defer_(d.approvalMgr.Shutdown)

	// Dangerous-mode: snapshot the settings flag on every approval so
	// the user can flip it from the dashboard and the next gated call
	// blasts through without a daemon restart. The accessor swallows
	// settingsSvc errors (the Load path itself logs + returns defaults
	// already) — false is the safe default if anything goes sideways.
	dangerousModeSvc := settingsSvc
	d.approvalMgr.SetDangerousModeProvider(func() bool {
		return dangerousModeSvc.Load(ctx).DangerousModeEnabled
	})

	d.notifyBus = notify.NewBus()
	// Persist every published notify event so the Signal tray can
	// backfill on open and survive page reloads. The bus stays
	// fire-and-forget for SSE subscribers; the store is async-best-
	// effort (a slow DB shouldn't back up publishers).
	d.notifyStore = newNotifyStore(db)
	d.notifyBus.SetStore(d.notifyStore)
	if ps, ok := d.notifyStore.(notify.PushStore); ok {
		d.notifyPushStore = ps
		d.notifyBus.SetDispatcher(newWebPushDispatcher(ps, webPushSubscriber(cfg.WebPushSubject, cfg.PublicURL)))
	}

	// Bridge approvals into the Signal tray. Without this, pending
	// approvals (shell-guard Bash hits, MCP tool gates) only fire
	// approval.Bus events that the tray doesn't subscribe to, so they
	// were silently invisible. Now every pending/resolved approval also
	// emits a notify.Event with kind="approval_pending|approved|denied".
	d.approvalMgr.SetNotifyPublisher(&approvalNotifyAdapter{bus: d.notifyBus})

	// AFK / trusted-allowlist policy resolver (M5). Loads approval_rules
	// from the DB and installs a PolicyTrustedAllow resolver, so rules
	// the user configures via the Shell Guard UI take effect without a
	// daemon restart (CRUD handler calls d.approvalMgr.ReloadPolicyRules
	// after each write). Empty ruleset = safe default; every approval
	// still falls through to human prompt.
	if rules, err := db.ListApprovalRules(ctx, ""); err == nil {
		resolver := &approval.PolicyResolver{
			Policy: approval.PolicyTrustedAllow,
			Rules:  rules,
		}
		resolver.SetHitRecorder(db)
		d.approvalMgr.SetPolicyResolver(resolver, 0) // 0 → 5s default grace
	} else {
		slog.Warn("approval rules: initial load failed", "error", err)
	}
	// Bounded retention: keep at most this many rows. Oldest-read
	// evicted first by the store, then oldest period. 500 is the
	// MVP default — power users get a config knob later.
	go pruneNotificationsLoop(ctx, db)

	// Audit saved-search evaluator: every minute, count matches for each
	// enabled saved search over its window and fire a Signal-tray
	// notification when the threshold is crossed (debounced per search).
	go auditSavedSearchLoop(ctx, db, d.notifyBus)

	d.installMgr, err = install.New()
	if err != nil {
		slog.Warn("mcp install manager unavailable", "error", err)
	}

	// Guards (M0-M5). All wired here so the API surface and the UI
	// have populated backends. The HookInstaller posts to the local
	// loopback hook bridge; if cfg.HTTPAddr is empty (test fixtures)
	// the installer surfaces a clear error rather than silently
	// dropping the install.
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		slog.Warn("guards: resolve home dir", "error", homeErr)
	} else {
		// HTTPAddr is canonical ":3333"; if a host prefix is set, strip it
		// so the hook always targets loopback.
		port := cfg.HTTPAddr
		if colon := strings.LastIndex(port, ":"); colon >= 0 {
			port = port[colon:]
		}
		hookEndpoint := "http://127.0.0.1" + port + "/v1/hooks/pretool"
		d.hookInstaller, err = install.NewHookInstaller(home, db, hookEndpoint)
		if err != nil {
			slog.Warn("guards: hook installer init", "error", err)
		}
		d.sandboxInstall, err = sandbox.NewInstaller(home, db)
		if err != nil {
			slog.Warn("guards: sandbox installer init", "error", err)
		}
	}
	d.sanitizer = sanitize.DefaultDenylist()

	// Sandbox wrapper for downstream MCP server spawns. When the
	// SandboxDownstreams setting is on (Guards → Sandbox dashboard
	// toggle) and the host has a working driver, every downstream
	// MCP server we spawn is wrapped in sandbox-exec so credential
	// paths (~/.ssh, ~/.aws, etc.) are inaccessible. Off by default
	// because some servers legitimately need network/FS access; the
	// user opts in once they've reviewed their server list. Read at
	// boot here; flipping the setting at runtime calls
	// downstream.Manager.SetSandboxWrapper via the PUT handler so
	// existing instances keep their prior config (cleanup on stop)
	// and the next spawn picks up the new wrapper.
	if settings := settingsSvc.Load(ctx); settings.SandboxDownstreams {
		wrapper := sandbox.NewCommandWrapper(sandbox.Config{
			Network: sandbox.NetworkHost, // host network preserved by default
		})
		d.manager.SetSandboxWrapper(wrapper)
		slog.Info("guards: sandbox wrapper installed", "driver", wrapper.Describe())
	}

	// FileWatcher (M4) — drives kind=file_watch scheduled jobs via
	// fsnotify. Previously the package was fully built + unit-tested
	// but never instantiated, so jobs with kind=file_watch sat in the
	// DB forever without firing. Construct it before the scheduler so
	// the scheduler can pass itself as the runner.

	// Auditor must exist BEFORE the scheduler is constructed — previously
	// scheduler.New captured a nil d.auditor (auditor was wired further
	// down at the end of the guards block), which silently disabled all
	// scheduled-job audit emission for the daemon's lifetime. Initialise
	// the audit/session buses + logger here so every guard wired below
	// gets a live auditor.
	d.auditBus = audit.NewBus()
	d.sessionBus = session.NewBus()
	d.auditor = audit.NewLogger(db, db, d.auditBus)

	// Wire the auditor into the secrets manager AFTER both exist.
	// Closes the forensics gap: every Get/Put/Delete/List on a secret
	// scope now emits a secret.* audit row carrying (scope_id, key) —
	// plaintext values are never written to audit. Nil-safe: when
	// secretsMgr is missing (no age key configured), this is a no-op.
	if d.secretsMgr != nil {
		d.secretsMgr.SetAuditor(d.auditor)
	}

	d.scheduler = scheduler.New(db, d.approvalMgr, d.auditor, scheduler.RealClock{})
	// Wire the audit/worker_runs retention executor + policy + seed
	// the built-in nightly job BEFORE Start() so the first Reload()
	// inside Start picks the row up. Failures here are non-fatal —
	// retention being off is better than the daemon refusing to boot.
	pruneExec := newStorePruneExecutor(db)
	d.scheduler.SetPruneExecutor(pruneExec)
	prunePolicy := retentionPolicyFromConfig(loadRetentionConfig(cfg.ConfigFile))
	d.scheduler.SetPrunePolicy(&prunePolicy)
	if err := ensureAuditPruneJob(ctx, db); err != nil {
		slog.Warn("retention: seed audit_prune job", "error", err)
	}

	// brw browser-profile auto-discovery (DEFAULT OFF). When enabled, wire the
	// in-process reconcile executor + seed the interval-fallback and policy
	// file_watch jobs BEFORE Start so the scheduler's initial Reload arms the
	// interval job (the FileWatcher, constructed below after Start, picks up
	// the watch job on its own initial Reload). Both jobs carry the
	// scheduler.BrwReconcileCommand sentinel so dispatch routes them to the
	// executor instead of execing. Failures here are non-fatal — a degraded
	// auto-discovery is better than the daemon refusing to boot.
	if cfg.BrwAutodiscover {
		brwExec := newBrwReconcileExecutor(brwReconcileDeps{
			store:      db,
			svc:        config.NewService(db),
			engine:     d.engine,
			manager:    d.manager,
			workspaces: cfg.BrwWorkspaces,
			brwctlPath: cfg.BrwctlPath,
			policyPath: cfg.BrwPolicyPath,
			prune:      cfg.BrwPrune,
		})
		d.scheduler.SetBrwReconcileExecutor(brwExec)
		if err := ensureBrwReconcileJobs(ctx, db, cfg.BrwInterval, cfg.BrwPolicyPath); err != nil {
			slog.Warn("brw autodiscover: seed reconcile jobs", "error", err)
		}
		slog.Info("brw autodiscover enabled",
			"interval", cfg.BrwInterval,
			"policy", cfg.BrwPolicyPath,
			"workspaces", cfg.BrwWorkspaces,
			"prune", cfg.BrwPrune,
			"brwctl", cfg.BrwctlPath,
		)
	}

	if startErr := d.scheduler.Start(ctx); startErr != nil {
		slog.Warn("guards: scheduler start", "error", startErr)
	} else {
		sched := d.scheduler
		d.defer_(func() { _ = sched.Stop(5 * time.Second) })

		// Start FileWatcher now that the scheduler exists. 0 debounce
		// → use the package default (500ms). Failure here is non-fatal:
		// time-based jobs still work; only file_watch jobs lose their
		// firing path. Logged so the admin can see.
		fw, fwErr := triggers.NewFileWatcher(db, sched, 0)
		if fwErr != nil {
			slog.Warn("filewatch: construct", "error", fwErr)
		} else if startErr := fw.Start(ctx); startErr != nil {
			slog.Warn("filewatch: start", "error", startErr)
		} else {
			d.fileWatcher = fw
			d.defer_(func() { _ = fw.Stop() })
		}
	}

	// MCPlexer Brain (M1) — MD<->SQLite sync engine. Construction can run
	// here (db exists); the dual-write hook into d.tasksSvc is wired AFTER
	// that service is built (~L754). Flag is the env flag OR'd with
	// settings.brain_enabled. Every error is non-fatal (logged + degrade),
	// matching the FileWatcher precedent above. Flag-off = today's
	// behaviour, byte-for-byte — no file write, no watcher.
	brainCfg := brain.LoadConfig(os.Getenv)
	if raw, sErr := db.GetSettings(ctx); sErr == nil {
		brainCfg = brainCfg.MergeSettings(raw)
	}
	d.brainCfg = brainCfg
	if brainCfg.Enabled {
		ix := brain.NewIndexer(brainCfg, db, slog.Default())
		d.brainIndexer = ix
		// M4: a workspace.md edit re-resolves sessions/routes by bumping the
		// route cache's wsVersion (SPEC §9). Wired pre-reindex so the
		// startup sweep already invalidates if a workspace.md is present.
		if d.engine != nil {
			ix.SetWorkspaceInvalidate(func(string) { d.engine.InvalidateAllRoutes() })
		}
		// M4: load per-workspace config/routes.yaml (source="brain"),
		// upsert + prune, before the first reindex so routes are live.
		if abErr := config.ApplyBrain(ctx, db, brainCfg.Dir); abErr != nil {
			slog.Warn("brain: apply config", "error", abErr)
		}
		if rErr := ix.ReindexAll(ctx); rErr != nil {
			slog.Warn("brain: initial reindex", "error", rErr)
		}
		ser := brain.NewSerializer(brainCfg, db, slog.Default())
		ser.ShareSelfWrites(ix)
		d.brainSerial = ser

		// M7: the Notion-like dashboard record editor writes through this
		// same Serializer (hash-CAS + atomic + autocommit), so a GUI save is
		// byte-identical to an agent/VSCode edit. Backs /api/v1/brain/*
		// browser endpoints (threaded into NewRouter below).
		d.brainEditor = brain.NewEditor(db, ser)

		// M4: export the skill registry to global/skills/<name>/v<N>/SKILL.md
		// (skills are natively Markdown+frontmatter). One-way, best-effort,
		// guarded so a human-edited SKILL.md in the repo is never clobbered.
		if seErr := ser.ExportSkills(ctx, db); seErr != nil {
			slog.Warn("brain: export skills", "error", seErr)
		}

		// Git backplane (M2): AUTO local commit on idle, MANUAL push.
		// git.Init is idempotent; an absent git binary degrades to a no-op
		// (Available() false) — never fatal. The AutoCommitter coalesces
		// touched-path signals from the watcher + serializer into one
		// commit per quiet window.
		g := brain.NewGit(brainCfg.Dir, slog.Default())
		if g.Available() {
			if iErr := g.Init(ctx); iErr != nil {
				slog.Warn("brain: git init", "error", iErr)
			}
			d.brainGit = g
			ac := brain.NewAutoCommitter(g, 0, 0, slog.Default())
			d.brainAutoCommit = ac
			ser.SetCommitNotify(ac.Notify)
			d.defer_(func() { ac.Close() })
		}

		if w, wErr := brain.NewWatcher(ix, 0); wErr != nil {
			slog.Warn("brain: watcher construct", "error", wErr)
		} else {
			if d.brainAutoCommit != nil {
				w.SetCommitNotify(d.brainAutoCommit.Notify)
			}
			if startErr := w.Start(ctx); startErr != nil {
				slog.Warn("brain: watcher start", "error", startErr)
				_ = w.Close()
			} else {
				d.brainWatcher = w
				d.defer_(func() { _ = w.Close() })
				// M6 (federation): when a repo dir is registered, extend the
				// fsnotify watch set so subsequent edits to the repo-local
				// .mcplexer/ are observed.
				ix.SetRegisterDirNotify(func(dir string) {
					if err := w.AddDir(dir); err != nil {
						slog.Warn("brain: watch repo dir", "dir", dir, "error", err)
					}
				})
			}
		}

		// M6 (federation, docs/brain.md Appendix C.2): per-session discovery
		// of a repo-local .mcplexer/ folder. On session bind the gateway calls
		// this with the client root + most-specific workspace; we walk the
		// ancestors for a .mcplexer/ dir and register it with the indexer
		// (source=repo, canonical for that workspace). Best-effort: a missing
		// dir is the common case (most sessions have no repo brain).
		// Exclude the locked-down gateway data dir (filepath.Dir(DBDSN) ==
		// ~/.mcplexer) and the central brain dir from repo-brain discovery so
		// a session CWD under $HOME never matches HOME/.mcplexer and drags the
		// protected tree (db, secrets, p2p, backups, memory-exports) into the
		// watcher/indexer.
		//
		// CRITICAL: the data dir IS ~/.mcplexer, whose basename already equals
		// RepoBrainDirName (".mcplexer"). DiscoverRepoBrain forms candidates as
		// <ancestor>/.mcplexer, so the candidate that matches the data dir is the
		// data dir PATH ITSELF (when the ancestor is $HOME) — NOT dataDir/.mcplexer.
		// Excluding filepath.Join(dataDir, ".mcplexer") (the old value, a
		// nonexistent path) therefore excluded nothing, and any $HOME-rooted
		// session registered ~/.mcplexer as a repo brain — the watcher then
		// indexed memory-exports/*.md as phantom tasks. The data dir must be
		// excluded by its own path; the nested form is kept as belt-and-braces
		// for a real repo brain placed inside it.
		dataDir := filepath.Dir(cfg.DBDSN)
		brainExcludeRoots := []string{
			dataDir,
			filepath.Join(dataDir, brain.RepoBrainDirName),
			filepath.Join(brainCfg.Dir, brain.RepoBrainDirName),
		}
		d.brainRepoDiscovery = func(dctx context.Context, clientRoot, workspaceID string) {
			dir, ok := brain.DiscoverRepoBrain(clientRoot, brainExcludeRoots...)
			if !ok {
				return
			}
			project, pErr := brain.EnsureRepoWorkspace(dctx, db, dir, workspaceID)
			if pErr != nil {
				slog.Warn("brain: materialise repo workspace", "dir", dir, "error", pErr)
				return
			}
			if (project.Created || project.Updated) && d.engine != nil {
				d.engine.InvalidateAllRoutes()
			}
			registerWorkspaceID := project.WorkspaceID
			if registerWorkspaceID == "" {
				registerWorkspaceID = workspaceID
			}
			if registerWorkspaceID == "" {
				slog.Warn("brain: repo dir has no workspace", "dir", dir)
				return
			}
			if rErr := ix.RegisterDir(dctx, dir, registerWorkspaceID, store.IndexSourceRepo); rErr != nil {
				slog.Warn("brain: register repo dir", "dir", dir, "error", rErr)
			}
		}

		// Secrets (M3): SOPS+age value-only-encrypted scope store. The age
		// private identity stays machine-local under the data dir
		// (SOPS_AGE_KEY_FILE); only public recipients (Max's machines only —
		// Appendix B #5) ever land in the repo. Wire the SOPS source as the
		// secrets.Manager's dual-read primary BEFORE the age-DB blob; on a
		// miss/err it falls back, so this is a no-op until secrets are
		// migrated. Every error is non-fatal (logged + degrade).
		ageKeyFile := os.Getenv(brain.EnvAgeKeyFile)
		if ageKeyFile == "" {
			ageKeyFile = brain.DefaultAgeKeyFile(filepath.Dir(cfg.DBDSN))
		}
		if recipients, kErr := brain.EnsureAgeKeyFile(ageKeyFile); kErr != nil {
			slog.Warn("brain: age key file", "path", ageKeyFile, "error", kErr)
		} else {
			d.brainAgeRecipients = recipients
			d.brainAgeKeyFile = ageKeyFile
			if d.secretsMgr != nil {
				d.secretsMgr.SetBrainSource(brain.NewSOPSSource(brainCfg.Dir, ageKeyFile))
			}
		}
	}

	d.addonReg, d.addonExec, d.addonCreator = loadAddonsWithCreator(ctx, cfg, db, d.authInj)

	settings := settingsSvc.Load(ctx)
	if settings.MeshEnabled {
		d.meshMgr = mesh.NewManager(db)
		d.meshMgr.SetNotifyBus(d.notifyBus)
		d.meshMgr.SetDisplayNameProvider(makeDisplayNameProvider(ctx, settingsSvc))
		d.meshMgr.SetPeerRenamer(db)
		d.meshMgr.SetPeerLister(db)
		// v0.13.0 — mesh__send_secret plumbing.
		d.meshMgr.SetPeerIdentityUpdater(db)
		d.meshMgr.SetSecretOfferStager(db)
		// mesh__push_skill plumbing — stage inbound skill offers.
		d.meshMgr.SetSkillOfferStager(db)
		if d.secretTransferKey != nil {
			recipient := d.secretTransferKey.Recipient().String()
			d.meshMgr.SetTransferRecipientProvider(func() string { return recipient })
		}
		d.meshMgr.SetAuthSync(db, d.enc, d.secretTransferKey)
		d.meshMgr.SetAuthSyncRefreshHook(func() {
			if d.engine != nil {
				d.engine.InvalidateAllRoutes()
			}
			if d.manager != nil {
				d.manager.NotifyToolsChanged()
			}
		})
		wireAuthSyncHooks(d.meshMgr, d.secretsMgr, d.flow)
		reaper := mesh.NewReaper(ctx, db)
		d.defer_(reaper.Stop)
	}
	meshTransport := wireMeshP2P(ctx, d.p2pHost, d.p2pLookup, d.meshMgr)
	if meshTransport != nil {
		d.defer_(func() { _ = meshTransport.Close() })
	}
	// Offline-delivery queue: park targeted to_peer sends that fail at
	// the libp2p layer (peer offline / unreachable) and replay them on
	// reconnect. Cross-machine only — same-machine routing stays via the
	// local sqlite bus and never queues here.
	if d.meshMgr != nil && meshTransport != nil {
		_ = wireMeshOutboundQueue(ctx, d.meshMgr, db, meshTransport,
			d.p2pReconnector, d.p2pLiveness, d.notifyBus)
	}

	d.telegramMgr = buildTelegramManager(ctx, db, d.enc, d.meshMgr, d.notifyBus, d.auditor)
	if d.telegramMgr != nil {
		d.manager.RegisterInternal("telegram", telegram.NewMCPServer(d.telegramMgr))
	}

	d.hammerspoonMgr = buildHammerspoonManager(ctx, db, d.enc, d.auditor)
	if d.hammerspoonMgr != nil {
		d.manager.RegisterInternal("hammerspoon", hammerspoon.NewMCPServer(d.hammerspoonMgr))
	}

	// LM Studio kickoff tools. Always registered so the tools advertise;
	// calls return a clear opt-in error until MCPLEXER_ALLOW_LMSTUDIO=1
	// is set on the daemon.
	d.manager.RegisterInternal("lmstudio", lmstudio.NewMCPServer(lmstudio.NewManagerFromEnv()))

	d.googleChatMgr = buildGoogleChatManager(ctx, db, d.enc, d.meshMgr, d.notifyBus, d.auditor)
	d.googleChatJWT = googlechat.NewJWTVerifier(os.Getenv("GOOGLECHAT_BOT_PROJECT_NUMBER"))

	// Register the self-CRUD InternalBackend. Admin visibility is gated
	// by AdminCWDGate at the gateway, so these tools only appear when the
	// agent's CWD is at or under the data directory.
	internalBackend := control.NewInternalBackend(db, d.backupSvc)
	internalBackend.SetEncryptor(d.enc)
	internalBackend.SetUsageService(d.usageSvc)
	// Route/workspace/server mutations via MCP admin tools must invalidate
	// the routing rules cache immediately (the REST handlers already do);
	// otherwise new routes only take effect after the 30s cache TTL.
	internalBackend.SetRouteInvalidator(d.engine.InvalidateAllRoutes)
	if d.brainGit != nil {
		internalBackend.SetBrainGit(d.brainGit)
	}
	if brainCfg.Enabled && len(d.brainAgeRecipients) > 0 {
		internalBackend.SetBrainMigrateSecrets(brainCfg.Dir, d.brainAgeRecipients, d.brainAgeKeyFile)
	}
	// M5 — migration tooling (brain_init / brain_import / brain_verify /
	// brain_disable). Wired only when the brain is enabled (the serializer +
	// indexer it shares with the live engine only exist then). The opt-in
	// flow is: enable the flag → restart → brain_init → brain_import (parity-
	// verified) → confirm. brain_disable flips settings.brain_enabled back.
	if brainCfg.Enabled && d.brainSerial != nil && d.brainIndexer != nil {
		internalBackend.SetBrainMigration(brainCfg, d.brainSerial, d.brainIndexer, db)
	}
	d.manager.RegisterInternal("mcplexer", internalBackend)

	if d.meshMgr != nil {
		d.meshMgr.SetAuditor(d.auditor)
		// Wire consent resolver so peer-addressed mesh__send audit
		// rows carry tier + accepted_by per epic
		// 01KSK91Q4W8TNED9MAF0CTRVKC. SelfAgentID for now mirrors
		// SelfUserID until per-session agent ids thread through.
		if bridge := newMeshConsentBridge(d.consentResolver); bridge != nil {
			uid := ""
			if d.selfUser != nil {
				uid = d.selfUser.UserID
			}
			d.meshMgr.SetConsentResolver(bridge, uid, uid)
		}
	}

	d.secretPromptMgr, d.secretPromptBus = buildSecretPromptManager(ctx, db, d.notifyBus, d.auditor, cfg.DBDSN)
	if d.secretPromptMgr != nil {
		mgr := d.secretPromptMgr
		d.defer_(mgr.Stop)
	}

	// Wire the stuck-detector's auto-reload hook so every automatic
	// reload of a wedged downstream emits an audit row + (when mesh is
	// up) a priority=high alert. Operators see exactly which downstream
	// the daemon decided was unhealthy and how many times it's been
	// auto-recovered today.
	wireAutoReloadHook(d.manager, db, d.auditor, d.meshMgr)

	d.skillShare = buildSkillShareService(d.p2pHost, db, d.auditor, defaultDataPath("skills"), d.consentResolver, d.selfUser)
	// skillShare backs mesh__offer_skill / mesh__request_skill over
	// /mcplexer/skill/1.0.0 gated by mesh.skill_request (distinct from
	// the registry surface below which uses mesh.registry_request).

	// Skills registry — agent-facing search/publish surface for SKILL.md
	// docs. Per-machine, NOT auto-shared across mesh peers (explicit
	// registry entry pull can use RegistryShareService over
	// /mcplexer/skill-registry/1.0.0 when a peer grants
	// mesh.registry_request). Seeds the embedded docs.
	d.skillRegistry = skillregistry.New(db)
	internalBackend.SetSkillRegistry(d.skillRegistry)
	internalBackend.SetGitSource(skillregistry.NewGitSource(defaultDataPath("skill-registry-git")))
	if err := skillregistry.Seed(ctx, d.skillRegistry); err != nil {
		slog.Warn("skill registry seed failed", "error", err)
	}
	d.registryShare = buildRegistryShareService(
		d.p2pHost, db, d.skillRegistry, d.auditor, d.consentResolver, d.selfUser,
	)

	// Worker templates — their own table since migration 057. Independent
	// of the skill registry (skills are markdown, templates are JSON +
	// parameter/secret schema). Inbuilt seeds (e.g. memory-consolidator)
	// are published global on first boot so every workspace can install
	// from them.
	d.workerTplRegistry = workertemplates.New(db)
	internalBackend.SetWorkerTemplateRegistry(d.workerTplRegistry)
	if err := workertemplates.Seed(ctx, d.workerTplRegistry); err != nil {
		slog.Warn("worker template seed failed", "error", err)
	}

	// Memory subsystem (migration 058). FTS5-only floor by default; if the
	// user has set MCPLEXER_OPENAI_API_KEY we wire an OpenAI embedder so
	// vector recall lights up. CLAUDE.md write-through digest goes to
	// ~/.mcplexer/memory-exports/<scope>.md — adding the @import to
	// CLAUDE.md is on the user.
	// Embedder selection: env override → persisted settings → auto-detect a
	// local OpenAI-compatible endpoint (default). The chosen model MUST emit
	// 1536-dim vectors (memories_vec is FLOAT[1536]) — auto-detect verifies
	// this before wiring; an env/settings mismatch surfaces as a clear error
	// at recall time, not silent corruption. See resolveMemoryEmbedder.
	memEmbedder := resolveMemoryEmbedder(ctx, settingsSvc.Load(ctx))
	if d.skillRegistry != nil {
		d.skillRegistry.SetEmbedder(memEmbedder)
	}
	memDigest := memory.NewFileDigester(defaultDataPath("memory-exports"))
	d.memorySvc = memory.NewService(db, memEmbedder, memDigest)
	d.defer_(func() { _ = d.memorySvc.Close() })
	// Re-embed-on-edit (wave B2): the brain Editor/Indexer rewrite memory
	// rows via store.UpdateMemory directly, which DROPS the stale vector on a
	// content change. Wire the memory Service's async re-embed so an edited
	// memory's vector recall recovers instead of staying FTS-only forever.
	// No-ops when no embedder is configured (maybeEmbedAsync checks HasModel).
	if d.brainEditor != nil {
		d.brainEditor.SetReEmbedHook(d.memorySvc.ReEmbedAfterUpdate)
	}
	if d.brainIndexer != nil {
		d.brainIndexer.SetReEmbedHook(d.memorySvc.ReEmbedAfterUpdate)
	}
	// Optional cross-encoder rerank (the single biggest precision lever)
	// when MCPLEXER_RERANK_BASE_URL is set. Speaks the common Jina/Cohere/
	// OpenAI-compatible rerank shape. Disabled (noop) otherwise.
	if rbase := os.Getenv("MCPLEXER_RERANK_BASE_URL"); rbase != "" {
		if rr, err := memory.NewHTTPReranker(rbase,
			os.Getenv("MCPLEXER_RERANK_MODEL"), os.Getenv("MCPLEXER_RERANK_API_KEY")); err == nil {
			d.memorySvc.SetReranker(rr)
			slog.Info("memory: cross-encoder rerank enabled", "base_url", rbase)
		} else {
			slog.Warn("memory: reranker construction failed", "error", err)
		}
	}
	// First-class semantic recall for the EXISTING corpus: when a vector
	// provider is wired (env / settings / auto-detect), embed any memories
	// that have no vector yet so old notes become searchable by meaning
	// without the user re-saving them. Runs once in the background; no-ops
	// when there's no embedder or nothing pending. Resumable across restarts.
	if d.memorySvc.StartBackfillAsync(ctx) {
		slog.Info("memory: embeddings backfill started for existing corpus")
	}
	// Bridge memory.Service.Notify → notify.Bus so every memory CUD op,
	// pin/unpin, link/unlink, and cross-peer offer transition fans out
	// over /api/v1/notifications/stream to the dashboard's Signal tray
	// + the /memory landing page's live-activity widget. Pre-fix this
	// hook was nil and the /memory page read "nothing learned yet" no
	// matter how many memories the gateway ingested.
	if d.notifyBus != nil {
		d.memorySvc.Notify = (&memoryNotifyAdapter{bus: d.notifyBus}).publish
	}

	// Harness memory auto-import: on startup, ingest any harness-native
	// memory files (Claude Code, MiMoCode, etc.) into the mcplexer memory
	// store. This is the bridge that makes mcplexer the single source of
	// truth — once imported, the harness is redirected to use mcplexer
	// memory going forward. Runs in a background goroutine so it doesn't
	// block daemon startup. Idempotent: re-running on restart is a no-op
	// for unchanged files.
	if os.Getenv("MCPLEXER_AUTO_IMPORT_MEMORY") != "0" {
		go func() {
			home, err := os.UserHomeDir()
			if err != nil {
				slog.Warn("memory: auto-import skipped — cannot resolve home dir", "error", err)
				return
			}
			results, err := harnessimport.ImportAll(context.Background(), db, home)
			if err != nil {
				slog.Warn("memory: auto-import error", "error", err)
				return
			}
			for _, r := range results {
				slog.Info("memory: auto-imported harness memory",
					"harness", r.Harness,
					"imported", r.Imported,
					"skipped", r.Skipped,
					"errors", len(r.Errors))
			}
		}()
	}

	// Harness memory sync scanner: periodically checks harness-native
	// memory directories for new/modified files and imports them. This is
	// the safety net — catches harnesses that still write to native memory
	// despite the "use mcplexer memory" directive. Set
	// MCPLEXER_SYNC_ENABLED=0 to disable.
	if scanner := memsync.NewScannerFromEnv(db); scanner != nil {
		scanner.Start()
		d.defer_(func() { scanner.Stop() })
		slog.Info("memory: sync scanner started")
	}

	// Tasks (migration 061) — operational per-workspace work items.
	// Construction is trivial (just wraps the store); leaving it unwired
	// would silently degrade `task__*` to "not enabled" replies. The bus
	// fans mutation events out to the dashboard's SSE subscribers so the
	// tasks pages don't have to poll.
	d.tasksSvc = tasks.New(db)
	d.tasksSvc.SetBus(tasks.NewBus())
	d.tasksSvc.SetWorkspaceLookup(db)
	bridgeHumanTaskNotifications(ctx, d.tasksSvc.Bus(), d.notifyBus, db)
	go humanTaskDueNotificationLoop(ctx, d.tasksSvc, db, d.notifyBus)

	// Code index (migration 127) — local codebase indexer backing the
	// index__* tools. Always wired; construction just wraps the store, and
	// leaving it nil would advertise the tools but reply "unavailable".
	d.codeIndex = index.NewService(db, nil)
	codeEmbedder, codeEmbedModel := resolveCodeIndexEmbedder(ctx, settingsSvc.Load(ctx))
	d.codeIndex.ConfigureEmbeddings(ctx, codeEmbedder, codeEmbedModel)

	// Concierge — chat turn signal classifier + log (epic
	// 01KSGKFZMVFZRWVDSZMK8W9JN1). Always wired alongside the tasks
	// service; the store has no fail-mode and the surface is read-only
	// when no concierge worker is running.
	d.conciergeSvc = concierge.NewService(db, nil)
	// Phase 2 — emit kind=task_event mesh messages on every mutation.
	// Wired only when mesh is enabled; without the manager the emitter
	// would have no sender, and the nil-safe Emit* methods would drop
	// silently anyway. Keep the wiring explicit so the lifecycle is
	// readable from the daemon's top-level.
	var taskEmitter *tasks.Emitter
	if d.meshMgr != nil {
		taskEmitter = tasks.NewEmitter(d.meshMgr)
		d.tasksSvc.SetEmitter(taskEmitter)
	}
	if d.p2pHost != nil {
		d.tasksSvc.SetLocalPeerID(d.p2pHost.PeerID())
	}

	// Cross-peer memory sharing over /mcplexer/memory/1.0.0. Built only
	// when p2p is active; the stub-mode constructor is a no-op so the
	// memorySvc + memoryShare can both safely be nil downstream.
	d.memoryShare = buildMemoryShareService(d.p2pHost, db, d.memorySvc, d.auditor, d.consentResolver, d.selfUser, settingsSvc)
	// Cross-peer task-attachment fetch over /mcplexer/attachment/1.0.0.
	// Pull-only — requesting peer asks by attachment id, the offering
	// peer answers with the full payload base64-encoded inline. Scope
	// gate: mesh.attachment_request. Tier 1 auto-paired peers inherit
	// the scope by default; Tier 2/3 require an explicit grant. The
	// stub-mode constructor is a no-op so attachmentShare can safely
	// be nil downstream.
	d.attachmentShare = buildAttachmentShareService(d.p2pHost, db, d.auditor, d.consentResolver, d.selfUser)
	// Cross-peer task sharing over /mcplexer/task/1.0.0 (Phase 3).
	// The tasks service implements p2p.TaskShareReceiver so the wire handler
	// applies proof-bound collaboration authorization, throttling, and
	// staleness checks before accepting anything.
	d.tasksSvc.SetTaskShare(buildTaskShareService(d.p2pHost, db, d.tasksSvc, d.auditor, d.consentResolver, d.selfUser))
	// Cross-peer task-state sync over /mcplexer/task-sync/1.0.0 provides
	// read-only catch-up for accepted workspace memberships. The inbound
	// handler registers in both build modes (stub no-ops); the outbound
	// scheduler only runs with a live p2p host. The workspace home resolves
	// the live proof-bound device and exact read grants on every stream;
	// legacy linked-workspace scopes are routing hints only.
	d.taskSync = buildTaskSyncService(d.p2pHost, db, d.tasksSvc, d.auditor, d.consentResolver, d.selfUser)
	d.defer_(d.taskSync.Stop)
	if d.p2pHost != nil {
		if sched := newTaskSyncScheduler(d.taskSync, db, db, slog.Default()); sched != nil {
			d.taskSyncScheduler = sched
			sched.SetOutbox(d.tasksSvc)
			sched.Start(ctx)
			if d.p2pReconnector != nil {
				// Immediate catch-up when a peer transitions back online so a
				// partition heals without waiting for the next 60s tick.
				d.p2pReconnector.AddOnlineObserver(func(peerID string) {
					_ = sched.SyncPeerNow(peerID)
				})
			}
			slog.Info("task-sync scheduler ready", "protocol", p2p.TaskSyncProtocol)
		}
	}

	// Brain dual-write (M1): every task mutation also serializes the
	// task's canonical .md file. Wired here (not in the early brain block)
	// because d.tasksSvc is nil at that point. Nil serializer (flag off)
	// leaves the hook unset — the service never writes files.
	if d.brainSerial != nil {
		d.tasksSvc.SetBrainHook(d.brainSerial)
		// M4: every memory mutation also serializes the memory's canonical
		// .md file. Nil serializer (flag off) leaves the hook unset.
		if d.memorySvc != nil {
			d.memorySvc.SetBrainHook(d.brainSerial)
		}
	}

	// Tier-1 silent replication coordinator. Wires memory writes +
	// skill installs onto a per-peer queue that drains over the
	// existing memory_share / skill_share libp2p protocols on a
	// 5-second batch interval. Same-user pairs (consent Tier 1) are
	// the target tier; same-org and cross-org peers stay manual.
	d.replicator = buildReplicationCoordinator(
		d.p2pHost, d.memoryShare, d.skillShare, d.memorySvc, db,
		d.consentResolver, defaultDataPath("skills"),
	)
	if d.replicator != nil {
		// Chain the memory notify hook so the dashboard's notify bus
		// continues to receive every event AND the replicator queues
		// each write. The replicator runs first (queueing is fast);
		// the SSE publish runs second so backpressure on a slow
		// dashboard subscriber can't delay queueing.
		existing := d.memorySvc.Notify
		d.memorySvc.Notify = chainedMemoryNotify(d.replicator, existing)
		// Linked-workspace task replication: on every task mutation in a
		// workspace explicitly linked to a peer, silently re-assign the
		// task to that peer (the same direct-assign path task__assign_remote
		// uses). Link-gated, so non-linked workspaces never replicate; the
		// coordinator owns the echo guard. Nil-safe wiring — without a task
		// emitter (mesh disabled) the hook just isn't installed.
		d.replicator.SetTaskReplication(
			&taskReplicationPusher{tasks: d.tasksSvc},
			linkListerAdapter{store: db},
		)
		if taskEmitter != nil {
			rep := d.replicator
			taskEmitter.SetReplicationHook(func(hookCtx context.Context, workspaceID, taskID, source string) {
				rep.OnTaskEvent(hookCtx, workspaceID, taskID, source)
			})
		}
		d.replicator.Start(ctx)
		d.defer_(d.replicator.Stop)
	}

	// Lease sweep (migration 071) — clears expired lease_expires_at
	// rows on a 1-minute tick so abandoned status=doing rows free up
	// the slot + the UI shows the row as un-assigned. The same tick
	// expires stale cross-peer task offers (pending outgoing > 7d,
	// pending incoming > 24h) so the offers inbox stays actionable.
	// Runs for the life of the daemon; cancels with ctx.
	{
		svc := d.tasksSvc
		go func() {
			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := svc.SweepExpiredLeases(ctx); err != nil {
						slog.Debug("tasks: lease sweep failed", "err", err)
					}
					if n, err := svc.SweepExpiredOffers(ctx); err != nil {
						slog.Debug("tasks: offer sweep failed", "err", err)
					} else if n > 0 {
						slog.Info("tasks: expired stale task offers", "count", n)
					}
				}
			}
		}()
	}

	// Agent-directory gossip: fan local agent changes to paired peers
	// + apply remote snapshots/deltas into our mesh_agents table so
	// `mesh__list_agents` and `to_agent` resolution see the cross-peer
	// directory. No-op in slim builds (p2pHost == nil) and when mesh
	// is disabled (settings.MeshEnabled == false).
	if d.p2pHost != nil && d.meshMgr != nil {
		src := p2p.NewAgentDirectorySource(db)
		sink := p2p.NewAgentDirectorySink(db)
		agentDir := p2p.NewAgentDirectoryService(d.p2pHost, d.p2pLookup, src, sink, db, nil, slog.Default())
		d.meshMgr.SetAgentBroadcaster(agentBroadcasterAdapter{svc: agentDir})
		// On pair-success the gossip stream opens via ConnectToPeer
		// (lower peer-id dials, higher accepts). For now the dial is
		// triggered opportunistically by the reconnector when paired
		// peers come back online — full one-shot-after-pair is a
		// Phase-2 hook.
		d.defer_(agentDir.Stop)
	}

	// v0.13.0 — once the mesh transport is up and paired peers have had
	// a chance to reconnect, broadcast our secret-transfer recipient
	// (peer_identity event) so peers can route mesh__send_secret to us.
	// Re-broadcast hourly so newly-paired peers learn the recipient
	// without needing a restart on the announcer side.
	if d.meshMgr != nil && d.secretTransferKey != nil {
		mgr := d.meshMgr
		go func() {
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
			if err := mgr.BroadcastPeerIdentity(ctx); err != nil {
				slog.Debug("peer_identity initial broadcast", "err", err)
			}
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := mgr.BroadcastPeerIdentity(ctx); err != nil {
						slog.Debug("peer_identity periodic broadcast", "err", err)
					}
				}
			}
		}()
	}

	// Workers runner (M0.3) — the in-process AI-agent tool-use loop.
	// Wired here after the engine, downstream manager, mesh manager,
	// secrets manager, and skill registry are all built so the runner
	// can be constructed with real impls of each collaborator. M0.4
	// (scheduler dispatch) and M0.5 (admin run_now) both reach for
	// d.workerRunner; making it nil-tolerant is therefore important.
	d.workerRunner = buildWorkerRunner(d, db, cfg.DBDSN)
	if d.scheduler != nil && d.workerRunner != nil {
		d.scheduler.SetWorkerExecutor(d.workerRunner)
		d.scheduler.SetWorkerStore(db)
	}

	// Schedule bridge (M0.4) — mirrors every enabled Worker as a
	// kind="worker" ScheduledJob row. Constructed with a kicker so the
	// running scheduler reloads its heap whenever the admin service
	// creates/updates/deletes a worker. Boot-time resync below
	// re-establishes rows for workers persisted before the binary
	// restarted.
	scheduleBridge := schedulebridge.New(db, d.scheduler)
	if rerr := scheduleBridge.ResyncAllEnabled(ctx, db, db); rerr != nil {
		slog.Warn("workers: schedule bridge boot resync failed", "error", rerr)
	}

	// Workers admin service (M0.5) — wraps the WorkerStore with
	// validation + defaulting and routes ad-hoc runs through the runner
	// when one is wired. Plugged into the InternalBackend so the
	// mcplexer__*_worker tools dispatch through it. Schedule-spec
	// validation reuses the scheduler's parser so cron + interval specs
	// fail closed at create time, before they hit the DB.
	workerAdminSvc := workersadmin.New(db, workersadmin.Options{
		Workspaces:     db,
		Runner:         d.workerRunner,
		ScheduleBridge: scheduleBridge,
		RunBus:         d.workerRunBus,
		// AuditCounter backs WorkerRun.tool_calls_count_source: for the
		// claude_cli / opencode_cli / grok_cli adapter families whose ToolCalls
		// slice is structurally empty, GetRun/ListRuns derive the count
		// from audit_records inside the run's time window. The sqlite
		// Store satisfies AuditCounter naturally via CountChildCLIToolCalls.
		AuditCounter:    db,
		OpenCodeRuntime: d.opencode,
		ScheduleValidator: func(spec string) error {
			// "manual" sentinel — worker is fired only by mesh
			// triggers / RunNow, never by the scheduler heap.
			if scheduler.IsManualSpec(spec) {
				return nil
			}
			_, err := scheduler.NextRun(scheduler.KindCron, spec, time.Now())
			if err == nil {
				return nil
			}
			// Cron parse failed — fall through to interval.
			if _, ierr := scheduler.NextRun(scheduler.KindInterval, spec, time.Now()); ierr == nil {
				return nil
			}
			return err
		},
	})
	internalBackend.SetWorkerAdmin(workerAdminSvc)
	// M3 — wire the template publisher so PublishAsTemplate +
	// InstallFromTemplate can read/write the skill registry.
	workerAdminSvc.SetTemplatePublisher(d.workerTplRegistry)
	// Audit emission for approval decisions — worker_approval.decided
	// records land in the audit ledger alongside the runner's
	// worker_run.* trail.
	workerAdminSvc.SetAuditor(d.auditor)
	workerAdminSvc.SetMeshStore(db)
	// M4 — wire the mesh-trigger store + peer-scope store so the admin
	// service can CRUD trigger rows and grant per-peer scopes.
	workerAdminSvc.SetMeshTriggerStore(db)
	workerAdminSvc.SetPeerScopeStore(db)
	// Detached delegation dispatches derive their context from the
	// daemon lifecycle ctx so shutdown cancels them (previously they
	// were rooted at context.Background and leaked past shutdown).
	workerAdminSvc.SetLifecycleContext(ctx)
	// Nightly retention tick also archives idle delegation workers.
	pruneExec.SetDelegationSweeper(workerAdminSvc)
	d.workerAdmin = workerAdminSvc

	if n, err := workerAdminSvc.ResumeOrphanedDelegations(ctx); err != nil {
		slog.Warn("failed to resume orphaned delegations", "error", err)
	} else if n > 0 {
		slog.Info("resumed orphaned delegation runs after restart", "count", n)
	}

	// Optional memory-consolidator bootstrapping. Disabled by default so
	// workspaces do not grow implicit workers; operators can opt in with
	// MCPLEXER_AUTO_INSTALL_MEMORY_CONSOLIDATOR=1 or install per workspace
	// from /memory/consolidation. Best-effort; never blocks startup.
	// See consolidator_autoinstall.go.
	autoInstallConsolidator(ctx, db, workerAdminSvc)
	autoInstallLogWatch(ctx, db, workerAdminSvc)

	// M4 — mesh-trigger dispatcher. Subscribes to mesh-message inserts
	// (local + p2p) and fires matching workers via the runner. CRUD
	// mutations on triggers invalidate the in-memory cache via
	// SetDispatcherReloader below. Optional — when mesh or runner is
	// missing the dispatcher reduces to a no-op.
	if d.meshMgr != nil && d.workerRunner != nil {
		triggerDispatcher := mesh4dispatcher(db, d.workerRunner, d.auditor, d.meshMgr)
		unsub := triggerDispatcher.Subscribe(meshSubscribeAdapter{mgr: d.meshMgr})
		d.defer_(unsub)
		workerAdminSvc.SetDispatcherReloader(triggerDispatcher)
	}

	// Two-tool worker surface — build a dedicated gateway.Server bound
	// to worker use, then wire its CallTool into the worker dispatcher's
	// BuiltinToolCaller. This Server never receives an MCP initialize
	// (workers go through CallTool, not the JSON-RPC transport), so its
	// internal SessionManager stays nil-state for its lifetime; the
	// handler's session-keyed paths fall back to global routes and the
	// admin-CWD gate denies admin tools (clientRoot is empty).
	//
	// Concurrency: handleToolsCall reads session state but never mutates
	// it after newHandler returns, so sharing one Server across all
	// concurrent worker runs is safe.
	if d.workerDispatcher != nil {
		workerGWOpts := []gateway.ServerOption{
			gateway.WithSettings(settingsSvc),
			gateway.WithAdminGate(gateway.NewAdminCWDGate(filepath.Dir(cfg.DBDSN))),
		}
		if d.approvalMgr != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithApprovals(d.approvalMgr))
		}
		if d.meshMgr != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithMesh(d.meshMgr))
		}
		if d.addonReg != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithAddons(d.addonReg, d.addonExec))
		}
		if d.secretPromptMgr != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithSecretPrompts(d.secretPromptMgr))
		}
		if d.secretsMgr != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithSecretsManager(d.secretsMgr))
		}
		if d.skillShare != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithSkillShare(d.skillShare))
		}
		if d.registryShare != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithRegistryShare(d.registryShare))
		}
		if d.skillRegistry != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithSkillRegistry(d.skillRegistry))
		}
		if d.memorySvc != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithMemory(d.memorySvc))
		}
		if d.memoryShare != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithMemoryShare(d.memoryShare))
		}
		if d.tasksSvc != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithTasks(d.tasksSvc))
		}
		if d.workerAdmin != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithWorkerAdmin(d.workerAdmin))
		}
		if d.conciergeSvc != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithConcierge(d.conciergeSvc))
		}
		if d.brainEditor != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithBrainEditor(d.brainEditor))
		}
		if d.codeIndex != nil {
			workerGWOpts = append(workerGWOpts, gateway.WithCodeIndex(d.codeIndex))
		}
		d.workerGateway = gateway.NewServer(db, d.engine, d.manager, d.auditor, gateway.TransportInternal, workerGWOpts...)
		if d.addonCreator != nil {
			d.workerGateway.SetAddonCreator(d.addonCreator)
		}
		wireMonitoringGateway(d.workerGateway, db, d.secretsMgr, d.meshMgr)
		if d.telegramMgr != nil {
			registerMonitoringBridgeSenders(d.telegramMgr, d.workerGateway)
		} else {
			registerMonitoringBridgeSenders(nil, d.workerGateway)
		}
		d.workerDispatcher.SetBuiltinCaller(workerBuiltinAdapter{gw: d.workerGateway})
	}

	return d, nil
}

// runServer starts the HTTP API listener, and (when withSocket is true) the
// Unix-socket MCP gateway. Both share the dependencies built by
// buildServerDeps. Returns when ctx is cancelled or any subsystem errors.
func runServer(ctx context.Context, cfg *Config, db *sqlite.DB, cfgSvc *config.Service, settingsSvc *config.SettingsService, withSocket bool) error {
	rdy := readiness.NewTracker()
	api.SetReadinessTracker(rdy)

	httpLn, err := listenTCPWithHandoff(ctx, cfg.HTTPAddr, listenHandoffConfig{})
	if err != nil {
		return err
	}

	// Publish the effective runtime address so out-of-process tools (doctor,
	// rules sync) discover where we actually bound — cfg.HTTPAddr comes from a
	// --addr flag that loadConfig() in those tools never sees. Use the bound
	// listener address so a :0 request resolves to the real port.
	runtimeDataDir := filepath.Dir(cfg.DBDSN)
	if err := config.WriteRuntimeInfo(runtimeDataDir, config.RuntimeInfo{
		HTTPAddr:   httpLn.Addr().String(),
		PublicURL:  cfg.PublicURL,
		SocketPath: cfg.SocketPath,
		PID:        os.Getpid(),
		Version:    mcplexerVersion,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		slog.Warn("could not publish runtime info", "err", err)
	}
	defer config.RemoveRuntimeInfo(runtimeDataDir)

	baseCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(baseCtx)
	router := newSwappableHTTPHandler(startingHTTPHandler())
	h2s := &http2.Server{}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           h2c.NewHandler(router, h2s), //nolint:staticcheck
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr, "state", "starting")
		if err := srv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	g.Go(func() error {
		select {
		case <-ctx.Done():
			rdy.SetDraining()
			slog.Info("draining: refusing new requests, waiting for in-flight to complete")
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutCancel()
			if err := srv.Shutdown(shutCtx); err != nil {
				return err
			}
			if err, ok := <-errCh; ok && err != nil {
				return fmt.Errorf("http server: %w", err)
			}
			return nil
		case err, ok := <-errCh:
			if !ok || err == nil {
				return nil
			}
			if isAddrInUseError(err) {
				return fmt.Errorf("http server: %w\n  Port %s is already in use. Another mcplexer instance may be running.\n  Try: mcplexer daemon status", err, cfg.HTTPAddr)
			}
			return fmt.Errorf("http server: %w", err)
		}
	})

	var socketLn net.Listener
	if withSocket {
		socketLn, err = listenLocalIPCWithHandoff(ctx, cfg.SocketPath, listenHandoffConfig{})
		if err != nil {
			cancel()
			_ = g.Wait()
			return err
		}
		defer func() {
			if socketLn != nil {
				_ = socketLn.Close()
			}
		}()
	}

	d, err := buildServerDeps(ctx, cfg, db, settingsSvc)
	if err != nil {
		cancel()
		_ = g.Wait()
		return fmt.Errorf("initialize server: %w", err)
	}
	defer d.shutdown()

	router.Swap(api.NewRouter(api.RouterDeps{
		APIToken:               d.apiToken,
		PublicURL:              cfg.PublicURL,
		Store:                  db,
		ConfigSvc:              cfgSvc,
		SettingsSvc:            settingsSvc,
		UsageSvc:               d.usageSvc,
		Engine:                 d.engine,
		Manager:                d.manager,
		FlowManager:            d.flow,
		Encryptor:              d.enc,
		AuditBus:               d.auditBus,
		ApprovalManager:        d.approvalMgr,
		ApprovalBus:            d.approvalBus,
		Auditor:                d.auditor,
		NotifyBus:              d.notifyBus,
		NotifyStore:            d.notifyStore,
		NotifyPushStore:        d.notifyPushStore,
		SessionBus:             d.sessionBus,
		ToolCache:              d.toolCache,
		InstallManager:         d.installMgr,
		AddonRegistry:          d.addonReg,
		AddonCreator:           d.addonCreator,
		MeshManager:            d.meshMgr,
		MonitoringNotifier:     ensureMonitoringDispatch(db, d.secretsMgr, d.meshMgr, d.notifyBus),
		AddonPreview:           addon.NewPreviewExecutorWithRequestAuth(d.authInj.HeadersForDownstream, d.authInj.ApplyToRequest),
		OAuthWizard:            oauth.NewWizard(db, db, d.flow, d.enc),
		TelegramManager:        d.telegramMgr,
		GoogleChatManager:      d.googleChatMgr,
		GoogleChatJWTVerifier:  d.googleChatJWT,
		HammerspoonManager:     d.hammerspoonMgr,
		P2PHost:                d.p2pHost,
		P2PPairing:             d.p2pPairing,
		P2PPeerLookup:          d.p2pLookup,
		P2PReconnector:         d.p2pReconnector,
		Collaboration:          d.collaboration,
		CollaborationSync:      d.taskSyncScheduler,
		SecretPrompts:          d.secretPromptMgr,
		SecretPromptBus:        d.secretPromptBus,
		BackupSvc:              d.backupSvc,
		SkillRegistry:          d.skillRegistry,
		WorkerTemplateRegistry: d.workerTplRegistry,
		MemorySvc:              d.memorySvc,
		TasksSvc:               d.tasksSvc,
		ConciergeSvc:           d.conciergeSvc,
		Scheduler:              d.scheduler,
		HookInstaller:          d.hookInstaller,
		Sanitizer:              d.sanitizer,
		SandboxInstall:         d.sandboxInstall,
		WorkerAdmin:            d.workerAdmin,
		OpenCode:               d.opencode,
		BrainGit:               d.brainGit,
		BrainConfig:            d.brainCfg,
		BrainEnabled:           d.brainCfg.Enabled,
		BrainEditor:            d.brainEditor,
		BrainIndexer:           d.brainIndexer,
		BrainAssist:            brainAssistant(db, d.secretsMgr, d.brainEditor),
		TrustedHosts:           cfg.TrustedHosts,
		CatalogSvc:             config.NewCatalogService(),
	}))

	rdy.SetReady()
	slog.Info("daemon ready", "addr", cfg.HTTPAddr)

	if withSocket {
		adminGate := gateway.NewAdminCWDGate(filepath.Dir(cfg.DBDSN))
		ln := socketLn
		socketLn = nil
		g.Go(func() error {
			return serveSocket(ctx, ln, cfg.SocketPath, db, d.engine, d.cachingLister, d.auditor,
				d.manager, d.approvalMgr, d.meshMgr, d.telegramMgr, settingsSvc,
				d.addonReg, d.addonExec, d.addonCreator, d.sessionBus,
				d.secretPromptMgr, d.secretsMgr, d.skillShare, d.registryShare, d.skillRegistry, d.memorySvc, d.memoryShare, d.tasksSvc, d.workerAdmin, d.brainEditor, d.codeIndex, adminGate,
				d.secretTransferKey, d.brainRepoDiscovery, rdy)
		})
	}

	if d.telegramMgr != nil {
		g.Go(func() error {
			if err := d.telegramMgr.Run(ctx); err != nil {
				slog.Warn("bridge: manager exited with error", "error", err)
			}
			return nil
		})
	}

	if d.googleChatMgr != nil {
		g.Go(func() error {
			if err := d.googleChatMgr.Run(ctx); err != nil {
				slog.Warn("googlechat: manager exited with error", "error", err)
			}
			return nil
		})
	}

	// Monitoring collector — pull loop for remote log sources. The
	// single-runner gate (MCPLEXER_MONITORING_RUNNER=0) keeps viewer
	// daemons from double-pulling in a peer group.
	startMonitoringCollector(ctx, db, d.secretsMgr, d.meshMgr)

	err = g.Wait()

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()

	if d.tasksSvc != nil {
		releaseAllSessionLeases(drainCtx, db, d.tasksSvc)
	}

	if cErr := checkpointWAL(drainCtx, db); cErr != nil {
		slog.Warn("drain: WAL checkpoint failed", "error", cErr)
	}

	return err
}

func runStdio(ctx context.Context, cfg *Config, db *sqlite.DB, settingsSvc *config.SettingsService) error {
	authInj, stdioFlow, stdioEnc, secretsMgr, err := buildAuthInjector(cfg, db)
	if err != nil {
		return err
	}

	engine := routing.NewEngine(db)
	manager := downstream.NewManager(db, authInj)
	if stdioFlow != nil {
		manager.SetAuthInvalidationHook(stdioFlow.RevokeToken)
	}
	defer manager.Shutdown(ctx) //nolint:errcheck

	tc := buildToolCache(ctx, db)
	lister := cache.NewCachingToolLister(manager, tc)

	approvalBus := approval.NewBus()
	approvalMgr := approval.NewManager(db, approvalBus)
	approvalMgr.ExpireStale(ctx)
	defer approvalMgr.Shutdown()

	// Dangerous-mode bypass (stdio path). Mirrors buildServerDeps so a
	// raw-stdio MCP client (claude desktop, codex stdio) honours the
	// global toggle without a daemon restart.
	{
		stdioSettingsSvc := settingsSvc
		approvalMgr.SetDangerousModeProvider(func() bool {
			return stdioSettingsSvc.Load(ctx).DangerousModeEnabled
		})
	}

	addonReg, addonExec, addonCreator := loadAddonsWithCreator(ctx, cfg, db, authInj)

	// Self-CRUD over MCP — gated by AdminCWDGate at the gateway layer.
	stdioBackupSvc := backup.New(filepath.Dir(cfg.DBDSN), cfg.DBDSN, mcplexerVersion)
	stdioInternalBackend := control.NewInternalBackend(db, stdioBackupSvc)
	stdioInternalBackend.SetEncryptor(stdioEnc)
	stdioInternalBackend.SetUsageService(buildUsageService(db, secretsMgr))
	stdioInternalBackend.SetRouteInvalidator(engine.InvalidateAllRoutes)
	// Workers admin tools live on the same InternalBackend; stdio mode
	// has no runner so RunNow falls back to the stub placeholder path
	// (the worker row is still authored — exec is picked up next time
	// the daemon proper runs).
	stdioWorkerAdmin := workersadmin.New(db, workersadmin.Options{
		Workspaces: db,
		// AuditCounter backs the derive-at-read-time tool_calls_count
		// for CLI adapter families — see daemon construction above.
		AuditCounter: db,
		ScheduleValidator: func(spec string) error {
			if scheduler.IsManualSpec(spec) {
				return nil
			}
			if _, err := scheduler.NextRun(scheduler.KindCron, spec, time.Now()); err == nil {
				return nil
			}
			_, err := scheduler.NextRun(scheduler.KindInterval, spec, time.Now())
			return err
		},
	})
	// stdio admin agents need the same publish-as-template surface as
	// the daemon. The worker_templates registry is stateless beyond
	// the DB, so we wire a local instance against the shared SQLite
	// connection.
	stdioWorkerTplRegistry := workertemplates.New(db)
	stdioWorkerAdmin.SetTemplatePublisher(stdioWorkerTplRegistry)
	stdioInternalBackend.SetWorkerTemplateRegistry(stdioWorkerTplRegistry)
	stdioInternalBackend.SetWorkerAdmin(stdioWorkerAdmin)
	manager.RegisterInternal("mcplexer", stdioInternalBackend)

	// Mesh (agent communication). No HTTP server here, but we still construct
	// a notifyBus so mesh.Send's notify_user flag doesn't silently drop — the
	// Electron shell runs the HTTP+socket mode and subscribes to the SSE.
	var meshMgr *mesh.Manager
	var meshReaper *mesh.Reaper
	if settingsSvc.Load(ctx).MeshEnabled {
		meshMgr = mesh.NewManager(db)
		meshMgr.SetAuthSync(db, stdioEnc, nil)
		meshMgr.SetAuthSyncRefreshHook(func() {
			engine.InvalidateAllRoutes()
			manager.NotifyToolsChanged()
		})
		wireAuthSyncHooks(meshMgr, secretsMgr, stdioFlow)
		meshReaper = mesh.NewReaper(ctx, db)
	}
	if meshReaper != nil {
		defer meshReaper.Stop()
	}

	auditor := audit.NewLogger(db, db, nil)
	if meshMgr != nil {
		meshMgr.SetAuditor(auditor)
	}
	// Stdio mode shares the same secrets Manager as the daemon; wire the
	// auditor here so every secret read/write from a stdio agent emits an
	// audit row.
	if secretsMgr != nil {
		secretsMgr.SetAuditor(auditor)
	}
	secretMgr, _ := buildSecretPromptManager(ctx, db, nil, auditor, cfg.DBDSN)
	if secretMgr != nil {
		defer secretMgr.Stop()
	}
	// Stdio mode also gets tasks (migration 061) — same construction as
	// the daemon path; cheap and lets agents wire `task__*` from a stdio
	// CLI session without the full daemon. Mesh-aware emission lights up
	// only when mesh is enabled (no listener to nil-emit to otherwise).
	stdioTasksSvc := tasks.New(db)
	if meshMgr != nil {
		stdioTasksSvc.SetEmitter(tasks.NewEmitter(meshMgr))
	}
	stdioCodeIndex := index.NewService(db, nil)
	stdioCodeEmbedder, stdioCodeEmbedModel := resolveCodeIndexEmbedder(ctx, settingsSvc.Load(ctx))
	stdioCodeIndex.ConfigureEmbeddings(ctx, stdioCodeEmbedder, stdioCodeEmbedModel)

	gwOpts := []gateway.ServerOption{
		gateway.WithApprovals(approvalMgr),
		gateway.WithSettings(settingsSvc),
		gateway.WithAdminGate(gateway.NewAdminCWDGate(filepath.Dir(cfg.DBDSN))),
		gateway.WithTasks(stdioTasksSvc),
		gateway.WithWorkerAdmin(stdioWorkerAdmin),
		gateway.WithCodeIndex(stdioCodeIndex),
	}
	if meshMgr != nil {
		gwOpts = append(gwOpts, gateway.WithMesh(meshMgr))
	}
	if addonReg != nil {
		gwOpts = append(gwOpts, gateway.WithAddons(addonReg, addonExec))
	}
	if secretMgr != nil {
		gwOpts = append(gwOpts, gateway.WithSecretPrompts(secretMgr))
	}
	if secretsMgr != nil {
		gwOpts = append(gwOpts, gateway.WithSecretsManager(secretsMgr))
	}
	gw := gateway.NewServer(db, engine, lister, auditor, gateway.TransportStdio, gwOpts...)
	if addonCreator != nil {
		gw.SetAddonCreator(addonCreator)
	}
	wireMonitoringGateway(gw, db, secretsMgr, meshMgr)

	unsub := manager.SubscribeToolsChanged(gw.InvalidateAndNotifyToolsChanged)
	defer unsub()

	return gw.RunStdio(ctx)
}

func releaseAllSessionLeases(ctx context.Context, db *sqlite.DB, svc *tasks.Service) {
	sessions, err := db.ListActiveSessions(ctx)
	if err != nil {
		slog.Warn("drain: list active sessions", "error", err)
		return
	}
	var total int
	for _, s := range sessions {
		// Stamp planned daemon restarts separately from generic
		// disconnects so the next agent can distinguish drain from loss.
		n, err := svc.ReleaseSessionTasksWithReason(ctx, s.ID, "daemon restarting")
		if err != nil {
			slog.Warn("drain: release session tasks", "session", s.ID, "error", err)
			continue
		}
		total += n
	}
	if total > 0 {
		slog.Info("drain: released task leases for daemon restart", "sessions", len(sessions), "tasks", total)
	}
}

func checkpointWAL(ctx context.Context, db *sqlite.DB) error {
	_, err := db.Raw().ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// isDBLockedError checks if the error chain contains a SQLite "database is
// locked" error (SQLITE_BUSY). The modernc.org/sqlite driver surfaces this as
// a wrapped error whose message contains "database is locked".
func isDBLockedError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "database is locked") ||
		strings.Contains(err.Error(), "SQLITE_BUSY"))
}

// isAddrInUseError checks if the error chain indicates the address is already
// in use (EADDRINUSE). Works with both *net.OpError wrapping and direct
// syscall errors.
func isAddrInUseError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, syscall.EADDRINUSE)
	}
	return strings.Contains(err.Error(), "address already in use")
}
