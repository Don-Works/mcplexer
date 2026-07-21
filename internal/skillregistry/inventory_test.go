package skillregistry_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestInventory_RegistryEntries(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "alpha",
		Body: sampleBody("alpha", "Use when alpha work is needed."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope: skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Name != "alpha" {
		t.Errorf("name=%s, want alpha", r.Name)
	}
	if r.SourceKind != skillregistry.SourceRegistry {
		t.Errorf("source_kind=%s, want registry", r.SourceKind)
	}
	if r.Version != 1 {
		t.Errorf("version=%d, want 1", r.Version)
	}
	if !r.Managed {
		t.Errorf("managed=false, want true for registry entries")
	}
	if r.Scope != "global" {
		t.Errorf("scope=%s, want global", r.Scope)
	}
}

func TestInventory_LocalDirUnmanaged(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	src := t.TempDir()
	writeSkill(t, src, "local-only", "Use when local only", "", nil)

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope:      skillregistry.GlobalScope(),
		SourceDirs: []string{src},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}

	found := false
	for _, r := range rows {
		if r.Name == "local-only" {
			found = true
			if r.SourceKind != skillregistry.SourceLocalDir {
				t.Errorf("source_kind=%s, want local-dir", r.SourceKind)
			}
			if r.Managed {
				t.Errorf("managed=true for unmanaged local dir")
			}
			if r.Scope != "local" {
				t.Errorf("scope=%s, want local", r.Scope)
			}
		}
	}
	if !found {
		t.Fatal("local-only skill not found in inventory")
	}
}

func TestInventory_DuplicateNameRegistryShadowsLocal(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "shared",
		Body: sampleBody("shared", "Use when shared is needed."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	src := t.TempDir()
	writeSkill(t, src, "shared", "Use when shared local", "", nil)

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope:      skillregistry.GlobalScope(),
		SourceDirs: []string{src},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}

	count := 0
	for _, r := range rows {
		if r.Name == "shared" {
			count++
			if r.SourceKind != skillregistry.SourceRegistry {
				t.Errorf("expected registry winner for shared, got %s", r.SourceKind)
			}
			if r.SourcePath == "" {
				t.Errorf("expected SourcePath to be enriched from local dir")
			}
			if !r.Managed {
				t.Errorf("expected managed=true when both sources present")
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 'shared' entry, got %d", count)
	}
}

func TestInventory_ParseFailure(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	src := t.TempDir()
	dir := filepath.Join(src, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("no frontmatter"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope:      skillregistry.GlobalScope(),
		SourceDirs: []string{src},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (unparseable), got %d", len(rows))
	}
	if rows[0].ParseError == "" {
		t.Errorf("expected ParseError, got empty")
	}
}

func TestInventory_ScopedRegistry(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	ws := "ws-test"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:        "ws-skill",
		Body:        sampleBody("ws-skill", "Workspace scoped skill."),
		WorkspaceID: &ws,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "global-skill",
		Body: sampleBody("global-skill", "Global scoped skill."),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	names := map[string]bool{}
	for _, r := range rows {
		names[r.Name] = true
	}
	if !names["ws-skill"] {
		t.Error("workspace scope should see ws-skill")
	}
	if !names["global-skill"] {
		t.Error("workspace scope should see global-skill")
	}
}

func TestInventory_BoundedOutput(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	for i := range 30 {
		name := fmt.Sprintf("skill-%03d", i)
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: name,
			Body: sampleBody(name, "Use when "+name),
		}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope: skillregistry.AdminScope(),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 10 {
		t.Errorf("expected 10 rows (bounded), got %d", len(rows))
	}
}

func TestInventory_LexicalSearch(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	skills := map[string]string{
		"pdf-extract":   "Use when extracting text from PDF documents.",
		"image-resize":  "Use when resizing or cropping image files.",
		"json-validate": "Use when validating JSON schemas.",
	}
	for name, desc := range skills {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: name,
			Body: sampleBody(name, desc),
		}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
	}

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope: skillregistry.GlobalScope(),
		Query: "pdf text extract",
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected search results")
	}
	if rows[0].Name != "pdf-extract" {
		t.Errorf("expected pdf-extract first, got %s", rows[0].Name)
	}
}

func TestInventory_EmptySources(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	rows, err := reg.Inventory(ctx, skillregistry.InventoryOptions{
		Scope: skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for empty registry, got %d", len(rows))
	}
}

func TestInventory_NilRegistry(t *testing.T) {
	var reg *skillregistry.Registry
	rows, err := reg.Inventory(context.Background(), skillregistry.InventoryOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows != nil {
		t.Errorf("expected nil rows")
	}
}

func TestInventorySearchIndex_RebuildAndSearch(t *testing.T) {
	idx := skillregistry.NewInventorySearchIndex()
	entries := []skillregistry.InventoryEntry{
		{Name: "alpha", Description: "Use when alpha is needed"},
		{Name: "beta", Description: "Use when beta is needed"},
	}
	idx.Rebuild(entries)

	hits := idx.Search("alpha", 10)
	if len(hits) != 1 || hits[0].Name != "alpha" {
		t.Errorf("expected alpha, got %+v", hits)
	}

	hits2 := idx.Search("", 10)
	if len(hits2) != 2 {
		t.Errorf("expected 2 for empty query, got %d", len(hits2))
	}
}
