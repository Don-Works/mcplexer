package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/api"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestCoreProfileGatesExperimentalAndCollaborationConstruction(t *testing.T) {
	t.Setenv("MCPLEXER_BRAIN_ENABLED", "1")
	t.Setenv("MCPLEXER_BRAIN_DIR", filepath.Join(t.TempDir(), "brain-must-not-be-created"))
	t.Setenv("MCPLEXER_AUTO_IMPORT_MEMORY", "0")
	t.Setenv("MCPLEXER_SYNC_ENABLED", "0")
	t.Setenv("MCPLEXER_OPENCODE_AUTOSTART", "0")

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "core-profile.db")
	db, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpdateSettings(context.Background(), json.RawMessage(`{"memory_embed_provider":"none"}`)); err != nil {
		t.Fatalf("disable memory embeddings for test: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := &Config{
		Mode:          "http",
		HTTPAddr:      "127.0.0.1:0",
		DBDSN:         dbPath,
		APITokenPath:  filepath.Join(dataDir, "api-key"),
		ConfigFile:    filepath.Join(dataDir, "mcplexer.yaml"),
		ServerProfile: serverProfileCore,
		P2PEnabled:    true,
	}
	d, err := buildServerDeps(ctx, cfg, db, config.NewSettingsService(db))
	if err != nil {
		t.Fatalf("buildServerDeps: %v", err)
	}
	t.Cleanup(d.shutdown)

	if d.modulePlan != (runtimeModulePlan{Core: true}) {
		t.Fatalf("module plan = %+v, want core-only", d.modulePlan)
	}
	for name, isNil := range map[string]bool{
		"brain indexer":       d.brainIndexer == nil,
		"brain serializer":    d.brainSerial == nil,
		"brain editor":        d.brainEditor == nil,
		"brain assistant":     d.brainAssist == nil,
		"brain watcher":       d.brainWatcher == nil,
		"p2p host":            d.p2pHost == nil,
		"p2p pairing":         d.p2pPairing == nil,
		"p2p lookup":          d.p2pLookup == nil,
		"p2p reconnector":     d.p2pReconnector == nil,
		"collaboration":       d.collaboration == nil,
		"mesh":                d.meshMgr == nil,
		"telegram":            d.telegramMgr == nil,
		"google chat":         d.googleChatMgr == nil,
		"hammerspoon":         d.hammerspoonMgr == nil,
		"skill sharing":       d.skillShare == nil,
		"registry sharing":    d.registryShare == nil,
		"memory sharing":      d.memoryShare == nil,
		"attachment sharing":  d.attachmentShare == nil,
		"task sync":           d.taskSync == nil,
		"replication":         d.replicator == nil,
		"secret transfer key": d.secretTransferKey == nil,
	} {
		if !isNil {
			t.Errorf("%s was constructed for core", name)
		}
	}
	if d.brainCfg.Enabled {
		t.Fatal("brain config remained enabled despite core experimental module being off")
	}

	for name, isPresent := range map[string]bool{
		"routing engine":     d.engine != nil,
		"downstream manager": d.manager != nil,
		"secrets manager":    d.secretsMgr != nil,
		"approval manager":   d.approvalMgr != nil,
		"auditor":            d.auditor != nil,
	} {
		if !isPresent {
			t.Errorf("required core dependency %s was not constructed", name)
		}
	}

	router := api.NewRouter(api.RouterDeps{
		Store:         db,
		ConfigSvc:     config.NewService(db),
		SettingsSvc:   config.NewSettingsService(db),
		Engine:        d.engine,
		Manager:       d.manager,
		Encryptor:     d.enc,
		Auditor:       d.auditor,
		BrainConfig:   d.brainCfg,
		BrainEnabled:  d.brainCfg.Enabled,
		P2PHost:       d.p2pHost,
		P2PPairing:    d.p2pPairing,
		P2PPeerLookup: d.p2pLookup,
		Collaboration: d.collaboration,
	})
	for _, tt := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/api/p2p/identity", wantStatus: http.StatusNotImplemented},
		{path: "/api/v1/collaboration", wantStatus: http.StatusServiceUnavailable},
		{path: "/api/v1/brain/status", wantStatus: http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tt.wantStatus {
			t.Errorf("GET %s status = %d, want %d (body %q)", tt.path, rec.Code, tt.wantStatus, rec.Body.String())
		}
	}
}
