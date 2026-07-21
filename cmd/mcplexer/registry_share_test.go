package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestRegistryShareAdapterRejectsAndOmitsComposition(t *testing.T) {
	ctx := context.Background()
	adapter, db := newRegistryShareTestAdapter(t)
	defer db.Close() //nolint:errcheck

	depBody := testSkillBody("p2p-fragment", "P2P dependency fixture.")
	dep, err := adapter.reg.Publish(ctx, skillregistry.PublishOptions{Name: "p2p-fragment", Body: depBody})
	if err != nil {
		t.Fatalf("publish dependency: %v", err)
	}
	rootBody := "---\nname: p2p-composed\ndescription: P2P composed fixture.\nincludes:\n" +
		"  - id: fragment\n    skill: p2p-fragment\n    scope: global\n    version: 1\n" +
		"    content_hash: \"" + dep.ContentHash + "\"\n---\n# Root\n\n<!-- mcpx:include fragment -->\n"
	if _, err := adapter.reg.Publish(ctx, skillregistry.PublishOptions{Name: "p2p-composed", Body: rootBody}); err != nil {
		t.Fatalf("publish root: %v", err)
	}

	if _, _, _, err := adapter.GetRegistryEntry(ctx, "p2p-composed", 0); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("provider error = %v, want ErrCompositionNotPortable", err)
	}
	if err := adapter.HandleIncomingRegistryEntry(ctx, "peer-example", "p2p-composed-remote",
		strings.Replace(rootBody, "p2p-composed", "p2p-composed-remote", 1), nil,
	); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("receiver error = %v, want ErrCompositionNotPortable", err)
	}
	if _, err := adapter.reg.Get(ctx, skillregistry.GlobalScope(), "p2p-composed-remote",
		skillregistry.VersionRef{Latest: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rejected receiver mutated registry: %v", err)
	}

	index, err := adapter.ListIndexEntries(ctx)
	if err != nil {
		t.Fatalf("list index: %v", err)
	}
	for _, entry := range index {
		if entry.Name == "p2p-composed" {
			t.Fatalf("composed root was advertised in P2P index: %+v", entry)
		}
	}
	hits, err := adapter.SearchIndexEntries(ctx, "p2p composed fixture", 10)
	if err != nil {
		t.Fatalf("search index: %v", err)
	}
	for _, hit := range hits {
		if hit.Name == "p2p-composed" {
			t.Fatalf("composed root was advertised in P2P search: %+v", hit)
		}
	}
}

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
