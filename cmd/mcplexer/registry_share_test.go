package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newRegistryShareTestAdapter(t *testing.T) (*registryShareAdapter, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "registry-share.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	reg := skillregistry.New(db)
	return &registryShareAdapter{reg: reg}, db
}

func testSkillBody(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
}

func TestRegistryShareAdapterSearchIndexEntries(t *testing.T) {
	ctx := context.Background()
	adapter, db := newRegistryShareTestAdapter(t)
	defer db.Close() //nolint:errcheck

	_, err := adapter.reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "deploy-fly",
		Body: testSkillBody("deploy-fly", "Use when deploying services to Fly.io."),
	})
	if err != nil {
		t.Fatalf("publish deploy-fly: %v", err)
	}
	_, err = adapter.reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "pdf-extract",
		Body: testSkillBody("pdf-extract", "Use when extracting text from PDF files."),
	})
	if err != nil {
		t.Fatalf("publish pdf-extract: %v", err)
	}

	hits, err := adapter.SearchIndexEntries(ctx, "deploy to fly", 5)
	if err != nil {
		t.Fatalf("SearchIndexEntries: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit")
	}
	if hits[0].Name != "deploy-fly" {
		t.Fatalf("top hit = %q, want deploy-fly", hits[0].Name)
	}
	if hits[0].ContentHash == "" || hits[0].Description == "" {
		t.Fatalf("expected search metadata, got %+v", hits[0])
	}
}

func TestRegistryShareAdapterIncomingPublishesGlobalEntry(t *testing.T) {
	ctx := context.Background()
	adapter, db := newRegistryShareTestAdapter(t)
	defer db.Close() //nolint:errcheck

	body := testSkillBody("remote-skill", "Use when testing remote registry imports.")
	if err := adapter.HandleIncomingRegistryEntry(ctx, "peer-123", "remote-skill", body, nil); err != nil {
		t.Fatalf("HandleIncomingRegistryEntry: %v", err)
	}
	got, err := adapter.reg.Get(ctx, skillregistry.GlobalScope(),
		"remote-skill", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("Get imported skill: %v", err)
	}
	if got.Body != body {
		t.Fatalf("imported body mismatch")
	}
	if got.Author != "p2p:peer-123" {
		t.Fatalf("author = %q, want p2p:peer-123", got.Author)
	}
}
