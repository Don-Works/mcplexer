package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"filippo.io/age"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/index"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/readiness"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/telegram"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

func serveSocket(
	ctx context.Context,
	ln net.Listener,
	path string,
	s *sqlite.DB,
	engine *routing.Engine,
	lister gateway.ToolLister,
	auditor *audit.Logger,
	manager *downstream.Manager,
	approvalMgr *approval.Manager,
	meshMgr *mesh.Manager,
	telegramMgr *telegram.Manager,
	settingsSvc *config.SettingsService,
	addonReg *addon.Registry,
	addonExec *addon.Executor,
	addonCreator *addon.Creator,
	sessionBus *session.Bus,
	secretMgr *ephemeral.Manager,
	secretsMgr *secrets.Manager,
	skillShare *p2p.SkillShareService,
	registryShare *p2p.RegistryShareService,
	skillRegistry *skillregistry.Registry,
	memorySvc *memory.Service,
	memoryShare *p2p.MemoryShareService,
	tasksSvc *tasks.Service,
	workerAdmin *workersadmin.Service,
	brainEditor *brain.Editor,
	codeIndex *index.Service,
	adminGate *gateway.AdminCWDGate,
	secretTransferKey *age.X25519Identity,
	repoBrainDiscovery func(ctx context.Context, clientRoot, workspaceID string),
	rdy *readiness.Tracker,
) error {
	defer func() { _ = ln.Close() }()
	slog.Info(localIPCDescription()+" listening", "path", path)

	// Close listener when context is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("accept: %w", err)
		}
		go handleSocketConn(ctx, conn, s, engine, lister, auditor, manager, approvalMgr, meshMgr, telegramMgr, settingsSvc, addonReg, addonExec, addonCreator, sessionBus, secretMgr, secretsMgr, skillShare, registryShare, skillRegistry, memorySvc, memoryShare, tasksSvc, workerAdmin, brainEditor, codeIndex, adminGate, secretTransferKey, repoBrainDiscovery, rdy)
	}
}

func handleSocketConn(
	ctx context.Context,
	conn net.Conn,
	s *sqlite.DB,
	engine *routing.Engine,
	lister gateway.ToolLister,
	auditor *audit.Logger,
	manager *downstream.Manager,
	approvalMgr *approval.Manager,
	meshMgr *mesh.Manager,
	telegramMgr *telegram.Manager,
	settingsSvc *config.SettingsService,
	addonReg *addon.Registry,
	addonExec *addon.Executor,
	addonCreator *addon.Creator,
	sessionBus *session.Bus,
	secretMgr *ephemeral.Manager,
	secretsMgr *secrets.Manager,
	skillShare *p2p.SkillShareService,
	registryShare *p2p.RegistryShareService,
	skillRegistry *skillregistry.Registry,
	memorySvc *memory.Service,
	memoryShare *p2p.MemoryShareService,
	tasksSvc *tasks.Service,
	workerAdmin *workersadmin.Service,
	brainEditor *brain.Editor,
	codeIndex *index.Service,
	adminGate *gateway.AdminCWDGate,
	secretTransferKey *age.X25519Identity,
	repoBrainDiscovery func(ctx context.Context, clientRoot, workspaceID string),
	rdy *readiness.Tracker,
) {
	defer func() { _ = conn.Close() }()
	slog.Info("socket connection accepted", "remote", conn.RemoteAddr())

	gwOpts := []gateway.ServerOption{
		gateway.WithApprovals(approvalMgr),
		gateway.WithSettings(settingsSvc),
		gateway.WithKeepalive(30 * time.Second),
		gateway.WithAdminGate(adminGate),
		gateway.WithReadiness(rdy),
	}
	if meshMgr != nil {
		gwOpts = append(gwOpts, gateway.WithMesh(meshMgr))
	}
	if telegramMgr != nil {
		gwOpts = append(gwOpts, gateway.WithTelegram(telegramMgr))
	}
	if addonReg != nil {
		gwOpts = append(gwOpts, gateway.WithAddons(addonReg, addonExec))
	}
	if sessionBus != nil {
		gwOpts = append(gwOpts, gateway.WithSessionBus(sessionBus))
	}
	if secretMgr != nil {
		gwOpts = append(gwOpts, gateway.WithSecretPrompts(secretMgr))
	}
	if secretsMgr != nil {
		gwOpts = append(gwOpts, gateway.WithSecretsManager(secretsMgr))
	}
	if skillShare != nil {
		gwOpts = append(gwOpts, gateway.WithSkillShare(skillShare))
	}
	if registryShare != nil {
		gwOpts = append(gwOpts, gateway.WithRegistryShare(registryShare))
	}
	if skillRegistry != nil {
		gwOpts = append(gwOpts, gateway.WithSkillRegistry(skillRegistry))
	}
	if memorySvc != nil {
		gwOpts = append(gwOpts, gateway.WithMemory(memorySvc))
	}
	if memoryShare != nil {
		gwOpts = append(gwOpts, gateway.WithMemoryShare(memoryShare))
	}
	if tasksSvc != nil {
		gwOpts = append(gwOpts, gateway.WithTasks(tasksSvc))
	}
	if workerAdmin != nil {
		gwOpts = append(gwOpts, gateway.WithWorkerAdmin(workerAdmin))
	}
	if brainEditor != nil {
		gwOpts = append(gwOpts, gateway.WithBrainEditor(brainEditor))
	}
	if codeIndex != nil {
		gwOpts = append(gwOpts, gateway.WithCodeIndex(codeIndex))
	}
	if secretTransferKey != nil {
		gwOpts = append(gwOpts, gateway.WithSecretTransferKey(secretTransferKey))
	}
	if repoBrainDiscovery != nil {
		gwOpts = append(gwOpts, gateway.WithRepoBrainDiscovery(repoBrainDiscovery))
	}
	gw := gateway.NewServer(s, engine, lister, auditor, gateway.TransportSocket, gwOpts...)
	if addonCreator != nil {
		gw.SetAddonCreator(addonCreator)
	}

	// Per-session subscription so that when any downstream's tool surface
	// changes (discover, internal registration, downstream-emitted
	// list_changed), this specific MCP client gets a notification and
	// re-runs tools/list without needing a full reconnect.
	var unsub func()
	if manager != nil {
		unsub = manager.SubscribeToolsChanged(gw.InvalidateAndNotifyToolsChanged)
	}
	if err := gw.RunConn(ctx, conn, conn); err != nil {
		slog.Error("socket connection error", "err", err)
	}
	if unsub != nil {
		unsub()
	}
	slog.Info("socket connection closed", "remote", conn.RemoteAddr())
}
