package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// This file guards the failure mode this codebase keeps hitting: correct,
// well-tested code that NOTHING CALLS. The whole point of the live catalog is
// that it is REFRESHED; a refresher nobody starts is exactly the static list
// it was built to replace. So the boot start is asserted at BOTH levels — a
// runtime test that drives startModelCatalogRefresher, and a source test that
// fails if serve.go stops calling it (the one link no test binary can run,
// because nothing in a test executes serve()).

// TestModelCatalogBootStartsRefresher is the level-1 guard: the boot call
// actually starts the refresh loop.
func TestModelCatalogBootStartsRefresher(t *testing.T) {
	resetModelCatalogSingleton()
	t.Cleanup(resetModelCatalogSingleton)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "catalog-boot.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	r := newModelCatalogRefresher(db, nil)
	if r == nil {
		t.Fatal("newModelCatalogRefresher returned nil")
	}
	startModelCatalogRefresher(ctx, r)

	if !modelCatalogStarted() {
		t.Fatal("daemon boot did not start the model catalog refresher — the " +
			"catalog would never refresh and preflight would fall back to the " +
			"static list this feature exists to replace")
	}
}

// TestModelCatalogBootIsIdempotent — the once-guard means a second boot call
// does not start a second loop.
func TestModelCatalogBootIsIdempotent(t *testing.T) {
	resetModelCatalogSingleton()
	t.Cleanup(resetModelCatalogSingleton)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "catalog-idem.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	r := newModelCatalogRefresher(db, nil)
	startModelCatalogRefresher(ctx, r)
	startModelCatalogRefresher(ctx, r) // must be a no-op, not a panic/second loop
	if !modelCatalogStarted() {
		t.Fatal("refresher not started")
	}
}

// TestStartModelCatalogRefresherNilSafe — a nil refresher (feature wiring
// absent) must not start anything and must not panic.
func TestStartModelCatalogRefresherNilSafe(t *testing.T) {
	resetModelCatalogSingleton()
	t.Cleanup(resetModelCatalogSingleton)
	startModelCatalogRefresher(context.Background(), nil)
	if modelCatalogStarted() {
		t.Fatal("nil refresher must not mark the loop started")
	}
}

// TestServeBootWiresModelCatalog is the level-2 guard: it reads serve.go and
// fails if the daemon stops constructing the catalog, wiring it into preflight
// and the API, or starting the refresh loop. None of these links is reachable
// from a test binary — and an unexercised boot line is exactly how this class
// of feature has died before.
func TestServeBootWiresModelCatalog(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	s := string(src)

	if !regexp.MustCompile(`startModelCatalogRefresher\([^)]*\)`).MatchString(s) {
		t.Error("serve.go no longer starts the model catalog refresher — the " +
			"catalog would be built but never refreshed, i.e. dead on arrival")
	}
	if !strings.Contains(s, "newModelCatalogRefresher(db, d.meshMgr)") {
		t.Error("serve.go no longer constructs the model catalog refresher with " +
			"the mesh manager — the auth-alert notifier would have no transport")
	}
	if !strings.Contains(s, "SetModelCatalog(d.modelCatalog)") {
		t.Error("serve.go no longer wires the catalog into worker-admin preflight — " +
			"preflight would silently drop back to the static profile union")
	}
	if !strings.Contains(s, "ModelCatalog:") {
		t.Error("serve.go no longer wires the catalog into the API router — " +
			"GET /api/v1/models would not register")
	}
}
