package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestApply_UpsertAndPrune exercises the source=yaml upsert + auto-prune path
// (Apply -> applyDownstreamServers -> pruneStaleDownstreams). This is
// load-bearing logic that DELETES DB rows, so it has to:
//   - upsert yaml rows from the file (create when absent, update when present)
//   - preserve CreatedAt + CapabilitiesCache across an update
//   - never touch a source="api" row (neither update nor prune)
//   - prune a source="yaml" row that drops out of the file, while leaving the
//     api row alive
func TestApply_UpsertAndPrune(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// A source="api" row that must survive every Apply untouched.
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "api-srv", Name: "api-srv", Transport: "stdio", ToolNamespace: "apins",
		Args: json.RawMessage("[]"), Source: "api",
	}); err != nil {
		t.Fatalf("seed api server: %v", err)
	}

	// A pre-existing source="yaml" row with a populated CapabilitiesCache,
	// that the file's upsert must update in place while preserving both the
	// original CreatedAt and the CapabilitiesCache.
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "yaml-keep", Name: "old-name", Transport: "stdio", ToolNamespace: "keepns",
		Args: json.RawMessage("[]"), Source: "yaml",
	}); err != nil {
		t.Fatalf("seed yaml-keep: %v", err)
	}
	// Capture the store-stamped CreatedAt so we can assert Apply preserves it.
	seeded, err := db.GetDownstreamServer(ctx, "yaml-keep")
	if err != nil {
		t.Fatalf("get yaml-keep: %v", err)
	}
	createdAt := seeded.CreatedAt
	caps := json.RawMessage(`{"tools":["a","b"]}`)
	if err := db.UpdateCapabilitiesCache(ctx, "yaml-keep", caps); err != nil {
		t.Fatalf("seed caps: %v", err)
	}

	// A pre-existing source="yaml" row absent from the file below — must prune.
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "yaml-stale", Name: "yaml-stale", Transport: "stdio", ToolNamespace: "stalens",
		Args: json.RawMessage("[]"), Source: "yaml",
	}); err != nil {
		t.Fatalf("seed yaml-stale: %v", err)
	}

	// Apply a file that updates yaml-keep (new name) and adds yaml-new.
	cfg := &FileConfig{DownstreamServers: []downstreamServerConfig{
		{ID: "yaml-keep", Name: "new-name", Transport: "stdio", ToolNamespace: "keepns"},
		{ID: "yaml-new", Name: "yaml-new", Transport: "stdio", ToolNamespace: "newns"},
	}}
	if err := Apply(ctx, db, cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	all, err := db.ListDownstreamServers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]store.DownstreamServer{}
	for _, d := range all {
		byID[d.ID] = d
	}

	// api row untouched (still present, source unchanged).
	if a, ok := byID["api-srv"]; !ok {
		t.Error("api-srv was deleted by Apply")
	} else if a.Source != "api" {
		t.Errorf("api-srv source mutated to %q", a.Source)
	}

	// yaml-keep updated in place: name changed, source stays yaml,
	// CreatedAt preserved, CapabilitiesCache preserved.
	keep, ok := byID["yaml-keep"]
	if !ok {
		t.Fatal("yaml-keep was pruned/lost")
	}
	if keep.Name != "new-name" {
		t.Errorf("yaml-keep name = %q, want new-name", keep.Name)
	}
	if keep.Source != "yaml" {
		t.Errorf("yaml-keep source = %q, want yaml", keep.Source)
	}
	if !keep.CreatedAt.Equal(createdAt) {
		t.Errorf("yaml-keep CreatedAt = %v, want %v (not preserved across update)", keep.CreatedAt, createdAt)
	}
	if string(keep.CapabilitiesCache) != string(caps) {
		t.Errorf("yaml-keep CapabilitiesCache = %q, want %q (dropped on update)", keep.CapabilitiesCache, caps)
	}

	// yaml-new created with source=yaml.
	if n, ok := byID["yaml-new"]; !ok {
		t.Error("yaml-new not created")
	} else if n.Source != "yaml" {
		t.Errorf("yaml-new source = %q, want yaml", n.Source)
	}

	// yaml-stale pruned (absent from the file, source=yaml).
	if _, ok := byID["yaml-stale"]; ok {
		t.Error("yaml-stale should have been pruned")
	}
}

// TestApply_DoesNotPruneNonYaml verifies the prune is scoped strictly to
// source="yaml": an api row absent from the file is NOT deleted (only yaml
// rows are eligible for the stale sweep).
func TestApply_DoesNotPruneNonYaml(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "api-only", Name: "api-only", Transport: "stdio", ToolNamespace: "apins",
		Args: json.RawMessage("[]"), Source: "api",
	}); err != nil {
		t.Fatalf("seed api-only: %v", err)
	}

	// Empty file: nothing to upsert, nothing yaml-sourced to prune.
	if err := Apply(ctx, db, &FileConfig{}); err != nil {
		t.Fatalf("Apply empty: %v", err)
	}

	if _, err := db.GetDownstreamServer(ctx, "api-only"); err != nil {
		t.Fatalf("api-only was deleted by an empty Apply: %v", err)
	}
}
