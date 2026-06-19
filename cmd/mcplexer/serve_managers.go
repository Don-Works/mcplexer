package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/api"
	"github.com/don-works/mcplexer/internal/assist"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/auth"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/hammerspoon"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/telegram"
)

func buildToolCache(ctx context.Context, db *sqlite.DB) *cache.ToolCache {
	servers, err := db.ListDownstreamServers(ctx)
	if err != nil {
		slog.Warn("failed to load servers for cache config, using defaults", "error", err)
		return cache.NewToolCache(nil)
	}

	configs := make(map[string]cache.ServerCacheConfig, len(servers))
	for _, srv := range servers {
		if len(srv.CacheConfig) == 0 || string(srv.CacheConfig) == "{}" {
			continue
		}
		cfg, err := cache.ParseServerCacheConfig(srv.CacheConfig)
		if err != nil {
			slog.Warn("invalid cache config for server, using defaults",
				"server", srv.ID, "error", err)
			continue
		}
		configs[srv.ID] = cfg
	}

	return cache.NewToolCache(configs)
}

// loadAddonsWithCreator loads addon YAML files from the addons/ directory next to the DB and
// returns an *addon.Creator that the gateway can use to hot-create new addons at runtime via
// mcpx__create_addon. The Creator is non-nil even if the addons dir is currently empty, so
// first-time addon creation works.
func loadAddonsWithCreator(ctx context.Context, cfg *Config, db *sqlite.DB, authInj *auth.Injector) (*addon.Registry, *addon.Executor, *addon.Creator) {
	addonDir := filepath.Join(filepath.Dir(cfg.DBDSN), "addons")
	if _, err := os.Stat(addonDir); err != nil {
		// Create on demand so the create_addon tool can write into it.
		if mkErr := os.MkdirAll(addonDir, 0o755); mkErr != nil {
			slog.Warn("failed to create addons dir", "dir", addonDir, "error", mkErr)
			return nil, nil, nil
		}
	}

	resolver := func(serverID string) (string, error) {
		srv, err := db.GetDownstreamServer(ctx, serverID)
		if err != nil {
			return "", err
		}
		return srv.ToolNamespace, nil
	}

	authScopeResolver := func(scopeName string) string {
		scopes, err := db.ListAuthScopes(ctx)
		if err != nil {
			return ""
		}
		for _, s := range scopes {
			if s.Name == scopeName {
				return s.ID
			}
		}
		return ""
	}

	reg, err := addon.LoadDir(addonDir, resolver, addon.WithAuthScopeResolver(authScopeResolver))
	if err != nil {
		slog.Warn("failed to load addons", "dir", addonDir, "error", err)
		return nil, nil, nil
	}

	creator := &addon.Creator{
		Registry:         reg,
		Dir:              addonDir,
		Resolve:          resolver,
		AuthScopeResolve: authScopeResolver,
	}

	if len(reg.AllTools()) == 0 {
		// No tools yet, but creator is still useful for AI-driven scaffold.
		return reg, addon.NewExecutorWithRequestAuth(authInj.HeadersForDownstream, authInj.ApplyToRequest), creator
	}

	exec := addon.NewExecutorWithRequestAuth(authInj.HeadersForDownstream, authInj.ApplyToRequest)
	return reg, exec, creator
}

// buildAuthInjector creates an auth.Injector and optionally an oauth.FlowManager.
// The returned secrets.Manager is the same instance used by the auth injector;
// the gateway uses it directly to persist captured human-supplied credentials
// during mcpx__provision_mcp without ever exposing values to the agent.
func buildAuthInjector(cfg *Config, db *sqlite.DB) (*auth.Injector, *oauth.FlowManager, *secrets.AgeEncryptor, *secrets.Manager, error) {
	var enc *secrets.AgeEncryptor
	var sm *secrets.Manager
	var fm *oauth.FlowManager

	if cfg.AgeKeyPath != "" {
		var err error
		enc, err = secrets.NewAgeEncryptor(cfg.AgeKeyPath)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		sm = secrets.NewManager(db, enc)
	}

	// Auto-generate a persistent age key alongside the DB if none configured.
	if enc == nil {
		keyPath := cfg.DBDSN + ".age"
		var err error
		enc, err = secrets.EnsureKeyFile(keyPath)
		if err != nil {
			slog.Warn("failed to create auto key file, falling back to ephemeral",
				"path", keyPath, "error", err)
			enc, _ = secrets.NewEphemeralEncryptor()
		} else {
			sm = secrets.NewManager(db, enc)
			slog.Info("using auto-generated age key", "path", keyPath)
		}
	}

	externalURL := cfg.ExternalURL
	if externalURL == "" && cfg.Mode == "http" {
		externalURL = httpURLFromAddr(cfg.HTTPAddr)
	}

	if enc != nil {
		fm = oauth.NewFlowManager(db, enc, externalURL)
		fm.SetPreferExternalURL(cfg.ExternalURL != "")
	}

	return auth.NewInjector(sm, fm, db), fm, enc, sm, nil
}

func wireAuthSyncHooks(mgr *mesh.Manager, sm *secrets.Manager, fm *oauth.FlowManager) {
	if mgr == nil {
		return
	}
	hook := func(ctx context.Context, scopeID string) {
		if scopeID == "" {
			return
		}
		go func() {
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := mgr.SendAuthScopeSnapshotToTrustedPeers(syncCtx, scopeID); err != nil {
				slog.Debug("mesh.auth_sync hook failed", "scope", scopeID, "err", err)
			}
		}()
	}
	if sm != nil {
		sm.SetChangeHook(hook)
	}
	if fm != nil {
		fm.SetTokenChangeHook(hook)
	}
}

// buildTelegramManager constructs a telegram.Manager and attaches a Client if
// a bot token has been configured. Always returns a non-nil manager when the
// mesh is enabled so the MCPServer wrapper has something to delegate to; the
// client is optional (tools report "not configured" until a token is set).
func buildTelegramManager(
	ctx context.Context,
	db *sqlite.DB,
	enc *secrets.AgeEncryptor,
	meshMgr *mesh.Manager,
	notifyBus *notify.Bus,
	auditor *audit.Logger,
) *telegram.Manager {
	if meshMgr == nil {
		return nil
	}
	mgr := telegram.NewManager(db, meshMgr, notifyBus)

	var sm *secrets.Manager
	if enc != nil {
		sm = secrets.NewManager(db, enc)
		if auditor != nil {
			sm.SetAuditor(auditor)
		}
	}
	token, err := api.TelegramTokenFromSecrets(ctx, store.Store(db), sm)
	if err != nil {
		slog.Warn("telegram: failed to read token", "error", err)
	}
	if token == "" {
		slog.Info("telegram: no bot token configured — tools advertise but send/receive inactive")
		return mgr
	}
	client, err := telegram.NewClient(token)
	if err != nil {
		slog.Warn("telegram: client init failed", "error", err)
		return mgr
	}
	mgr.SetClient(client)
	slog.Info("telegram: client attached")
	return mgr
}

// buildHammerspoonManager constructs a hammerspoon.Manager honouring the
// "hammerspoon" downstream row's Disabled flag and the bridge env config
// stored in the "hammerspoon-bridge" auth scope.
//
// Returns nil when the downstream row is missing or marked Disabled — that
// short-circuits RegisterInternal so the gateway never advertises the tools
// for a turned-off integration.
//
// When the row is enabled but the bridge isn't reachable / configured, this
// still returns a non-nil Manager backed by a real driver (or nullBridge if
// no secrets are set yet). MCPServer.Call surfaces a clean per-tool error
// rather than a stack trace.
func buildHammerspoonManager(
	ctx context.Context,
	db *sqlite.DB,
	enc *secrets.AgeEncryptor,
	auditor *audit.Logger,
) *hammerspoon.Manager {
	const (
		serverID    = "hammerspoon"
		authScopeID = "hammerspoon-bridge"
	)

	row, err := db.GetDownstreamServer(ctx, serverID)
	if err != nil || row == nil {
		slog.Debug("hammerspoon: server row missing — skipping", "error", err)
		return nil
	}
	if row.Disabled {
		slog.Debug("hammerspoon: server disabled — skipping")
		return nil
	}

	var sm *secrets.Manager
	if enc != nil {
		sm = secrets.NewManager(db, enc)
		if auditor != nil {
			sm.SetAuditor(auditor)
		}
	}

	bridge, allowExecLua := buildHammerspoonBridge(ctx, sm, authScopeID)
	return hammerspoon.NewManager(bridge, allowExecLua)
}

// buildHammerspoonBridge picks the driver (http / cli) and reads any required
// secrets out of the auth scope. Returns (nullBridge{}, false) when secrets
// aren't yet configured so MCPServer.Call surfaces "downstream not enabled".
func buildHammerspoonBridge(ctx context.Context, sm *secrets.Manager, scopeID string) (hammerspoon.Bridge, bool) {
	if sm == nil {
		return nil, false // → Manager replaces with nullBridge
	}
	read := func(key string) string {
		v, err := sm.Get(ctx, scopeID, key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(v))
	}
	driver := strings.ToLower(read("HAMMERSPOON_DRIVER"))
	if driver == "" {
		driver = "http"
	}
	allowExecLua := strings.EqualFold(read("HAMMERSPOON_ALLOW_EXEC_LUA"), "true")

	switch driver {
	case "http":
		password := read("HAMMERSPOON_BRIDGE_PASSWORD")
		if password == "" {
			slog.Info("hammerspoon: HTTP driver selected but no bridge password — bridge disabled until configured")
			return nil, allowExecLua
		}
		baseURL := read("HAMMERSPOON_BRIDGE_URL")
		if baseURL == "" {
			baseURL = "http://127.0.0.1:27123"
		}
		return hammerspoon.NewHTTPDriver(baseURL, password), allowExecLua
	case "cli":
		// CLI driver shells out to `hs` from PATH. No password needed —
		// auth is implicit via being able to execute as the user.
		return hammerspoon.NewCLIDriver(""), allowExecLua
	default:
		slog.Warn("hammerspoon: unknown driver — bridge disabled", "driver", driver)
		return nil, allowExecLua
	}
}

// buildGoogleChatManager constructs a googlechat.Manager and attaches a Client
// when a service account JSON has been stored. Returns nil only when the mesh
// is disabled (matches buildTelegramManager).
func buildGoogleChatManager(
	ctx context.Context,
	db *sqlite.DB,
	enc *secrets.AgeEncryptor,
	meshMgr *mesh.Manager,
	notifyBus *notify.Bus,
	auditor *audit.Logger,
) *googlechat.Manager {
	if meshMgr == nil {
		return nil
	}
	mgr := googlechat.NewManager(db, meshMgr, notifyBus)

	var sm *secrets.Manager
	if enc != nil {
		sm = secrets.NewManager(db, enc)
		if auditor != nil {
			sm.SetAuditor(auditor)
		}
	}
	raw, err := api.GoogleChatServiceAccountFromSecrets(ctx, store.Store(db), sm)
	if err != nil {
		slog.Warn("googlechat: failed to read service account", "error", err)
	}
	if len(raw) == 0 {
		slog.Info("googlechat: no service account configured — webhook + send/receive inactive")
		return mgr
	}
	key, err := googlechat.ParseServiceAccountKey(raw)
	if err != nil {
		slog.Warn("googlechat: parse service account failed", "error", err)
		return mgr
	}
	client := googlechat.NewClient(key)
	mgr.SetClient(client)
	slog.Info("googlechat: client attached", "client_email", key.ClientEmail)
	return mgr
}

// brainAssistant builds the lean assist engine and, when the brain editor is
// wired, grounds the link-related-memory nudge against the live index so it can
// never propose a [[ref]] that resolves to nothing (DESIGN §4.4). Without the
// editor the assistant still serves ghost-text + deterministic guidance; the
// model-backed link-memory nudge is simply suppressed.
func brainAssistant(db *sqlite.DB, sm *secrets.Manager, ed *brain.Editor) *assist.Assistant {
	a := assist.New(db, sm, nil)
	if ed != nil {
		a = a.WithMemoryIndex(brainMemoryIndex{ed: ed})
	}
	return a
}

// brainMemoryIndex adapts the brain Editor's three-tier search onto the slim
// assist.MemoryIndex interface, returning only memory hits. The candidate Name
// is the record id the typeahead inserts (so the proposed [[ref]] resolves the
// same way a hand-picked one would); Title carries the memory name for the
// model's relevance judgment.
type brainMemoryIndex struct {
	ed *brain.Editor
}

func (b brainMemoryIndex) SearchMemories(ctx context.Context, query, workspace string, limit int) ([]assist.MemoryCandidate, error) {
	res, err := b.ed.Search(ctx, query, brain.EntityKindMemory, workspace, limit)
	if err != nil {
		return nil, err
	}
	out := make([]assist.MemoryCandidate, 0, len(res.Hits))
	for _, h := range res.Hits {
		if h.Kind != brain.EntityKindMemory {
			continue
		}
		out = append(out, assist.MemoryCandidate{Name: h.ID, Title: h.Title})
	}
	return out, nil
}
