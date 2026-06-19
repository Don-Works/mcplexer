package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/harnesssync"
	"github.com/don-works/mcplexer/internal/store"
)

func TestRecordHarnessInitialize_PreservesBootstrapFields(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ver := 2
	hash := harnesssync.RenderedHash(harnesssync.Codex, ver)
	if err := db.UpsertHarnessBootstrap(ctx, &store.HarnessInitialization{
		Key:                "codex",
		BootstrapInstalled: true,
		BootstrapVersion:   &ver,
		BootstrapHash:      hash,
		RegistryVersion:    ver,
		Drifted:            false,
	}); err != nil {
		t.Fatalf("upsert bootstrap: %v", err)
	}

	initAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := db.RecordHarnessInitialize(ctx, "codex", "codex-cli/1.0"); err != nil {
		t.Fatalf("record init: %v", err)
	}

	got, err := db.GetHarnessInitialization(ctx, "codex")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientInfo != "codex-cli/1.0" {
		t.Errorf("client_info = %q, want codex-cli/1.0", got.ClientInfo)
	}
	if got.LastInitializeAt == nil {
		t.Fatal("last_initialize_at should be set")
	}
	if got.LastInitializeAt.Before(initAt) {
		t.Errorf("last_initialize_at too old: %v", got.LastInitializeAt)
	}
	if !got.BootstrapInstalled {
		t.Error("bootstrap_installed should be preserved")
	}
	if got.BootstrapVersion == nil || *got.BootstrapVersion != ver {
		t.Errorf("bootstrap_version = %v, want %d", got.BootstrapVersion, ver)
	}
	if got.BootstrapHash != hash {
		t.Errorf("bootstrap_hash changed: %q", got.BootstrapHash)
	}
	if got.RegistryVersion != ver {
		t.Errorf("registry_version = %d, want %d", got.RegistryVersion, ver)
	}
}

func TestUpsertHarnessBootstrap_TracksVersionAndHash(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.RecordHarnessInitialize(ctx, "claude", "claude-code/2.0"); err != nil {
		t.Fatalf("record init: %v", err)
	}

	ver1 := 1
	hash1 := harnesssync.RenderedHash(harnesssync.Claude, ver1)
	if err := db.UpsertHarnessBootstrap(ctx, &store.HarnessInitialization{
		Key:                "claude",
		BootstrapInstalled: true,
		BootstrapVersion:   &ver1,
		BootstrapHash:      hash1,
		RegistryVersion:    ver1,
		Drifted:            false,
	}); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}

	got, err := db.GetHarnessInitialization(ctx, "claude")
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if got.ClientInfo != "claude-code/2.0" {
		t.Errorf("client_info = %q, want preserved", got.ClientInfo)
	}
	if got.LastInitializeAt == nil {
		t.Fatal("last_initialize_at should be preserved")
	}
	if got.BootstrapHash != hash1 || got.RegistryVersion != ver1 {
		t.Fatalf("v1 receipt wrong: %+v", got)
	}

	ver2 := 2
	hash2 := harnesssync.RenderedHash(harnesssync.Claude, ver2)
	if err := db.UpsertHarnessBootstrap(ctx, &store.HarnessInitialization{
		Key:                "claude",
		BootstrapInstalled: true,
		BootstrapVersion:   &ver2,
		BootstrapHash:      hash2,
		RegistryVersion:    ver2,
		Drifted:            true,
	}); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	got2, err := db.GetHarnessInitialization(ctx, "claude")
	if err != nil {
		t.Fatalf("get v2: %v", err)
	}
	if got2.BootstrapHash != hash2 {
		t.Errorf("bootstrap_hash = %q, want %q", got2.BootstrapHash, hash2)
	}
	if got2.RegistryVersion != ver2 {
		t.Errorf("registry_version = %d, want %d", got2.RegistryVersion, ver2)
	}
	if !got2.Drifted {
		t.Error("drifted should be true")
	}
	if got2.ClientInfo != "claude-code/2.0" || got2.LastInitializeAt == nil {
		t.Errorf("init fields should survive bootstrap update: %+v", got2)
	}
}
