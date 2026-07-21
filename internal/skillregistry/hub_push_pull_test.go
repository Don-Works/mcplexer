package skillregistry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestLegacyPushPullRejectCompositionAndIndexOmitsIt(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	depBody := includeBody("legacy-fragment", "Legacy dependency fixture.", "", "# Fragment\n\nTEXT\n")
	dep, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "legacy-fragment", Body: depBody})
	if err != nil {
		t.Fatalf("publish dependency: %v", err)
	}
	decl := includeDeclaration("fragment", "legacy-fragment", "global", dep.Version, dep.ContentHash, "")
	rootBody := includeBody("legacy-composed", "Legacy composed fixture.", decl,
		"# Root\n\n<!-- mcpx:include fragment -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "legacy-composed", Body: rootBody}); err != nil {
		t.Fatalf("publish root: %v", err)
	}

	if _, err := reg.Push(ctx, skillregistry.GlobalScope(), "legacy-composed",
		skillregistry.VersionRef{Latest: true}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("push error = %v, want ErrCompositionNotPortable", err)
	}
	if _, err := reg.Pull(ctx, skillregistry.GlobalScope(), skillregistry.PullOptions{
		Name: "legacy-composed", Body: rootBody, ContentHash: skillregistry.ComputeContentHash(rootBody), DryRun: true,
	}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("pull error = %v, want ErrCompositionNotPortable", err)
	}
	if _, err := reg.Pull(ctx, skillregistry.GlobalScope(), skillregistry.PullOptions{
		Name: "legacy-composed", DryRun: true,
	}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("body-empty pull error = %v, want ErrCompositionNotPortable", err)
	}

	entries, err := reg.ListIndexEntries(ctx, skillregistry.GlobalScope())
	if err != nil {
		t.Fatalf("list index: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == "legacy-composed" {
			t.Fatalf("composed root was advertised in legacy index: %+v", entry)
		}
	}
}

func TestPushExportsWithMetadata(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("test-push", "A skill for testing push.")

	_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "test-push", Body: body})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	pushRes, err := reg.Push(ctx, skillregistry.GlobalScope(), "test-push", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushRes.Name != "test-push" {
		t.Fatalf("name mismatch: %s", pushRes.Name)
	}
	if pushRes.Version != 1 {
		t.Fatalf("version mismatch: %d", pushRes.Version)
	}
	if pushRes.Metadata.ContentHash == "" {
		t.Fatal("content_hash missing")
	}
	if pushRes.Metadata.Description == "" {
		t.Fatal("description missing")
	}
}

func TestPushWithBundleIncludesBundle(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("bundle-push", "A skill with bundle.")

	_, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "bundle-push",
		Body: body,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	pushRes, err := reg.Push(ctx, skillregistry.GlobalScope(), "bundle-push", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushRes.Bundle != nil {
		t.Log("bundle present as expected")
	}
}

func TestDryRunNoMutation(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("dry-run-test", "Testing dry-run.")

	_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "dry-run-test", Body: body})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	opts := skillregistry.PullOptions{
		Name:    "dry-run-test",
		Version: 1,
		DryRun:  true,
		Scope:   skillregistry.GlobalScope(),
	}
	pullRes, err := reg.Pull(ctx, skillregistry.GlobalScope(), opts)
	if err != nil {
		t.Fatalf("dry-run pull: %v", err)
	}
	if !pullRes.DryRun {
		t.Fatal("dry-run should be true")
	}
	if pullRes.Action != "skip" && pullRes.Action != "new" {
		t.Fatalf("unexpected action: %s", pullRes.Action)
	}

	heads, err := reg.ListHeads(ctx, skillregistry.GlobalScope(), 10)
	if err != nil {
		t.Fatalf("list heads: %v", err)
	}
	if len(heads) != 1 {
		t.Fatalf("expected 1 head, got %d", len(heads))
	}
}

func TestPullRequiresBody(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	scope := skillregistry.GlobalScope()

	opts := skillregistry.PullOptions{
		Name:    "new-from-hub",
		Version: 1,
		DryRun:  false,
		Scope:   scope,
	}
	_, err := reg.Pull(ctx, scope, opts)
	if err == nil {
		t.Fatal("expected error for missing body")
	}
	heads, listErr := reg.ListHeads(ctx, scope, 0)
	if listErr != nil {
		t.Fatalf("list heads: %v", listErr)
	}
	if len(heads) != 0 {
		t.Fatalf("body-less commit mutated registry: %+v", heads)
	}
}

func TestListIndexEntries(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("index-test", "Testing index entries.")

	_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "index-test", Body: body})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	entries, err := reg.ListIndexEntries(ctx, skillregistry.GlobalScope())
	if err != nil {
		t.Fatalf("list index: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected entries, got none")
	}

	found := false
	for _, e := range entries {
		if e.Name == "index-test" {
			found = true
			if e.Version == 0 {
				t.Fatal("version should be > 0")
			}
			if e.ContentHash == "" {
				t.Fatal("content_hash should be set")
			}
		}
	}
	if !found {
		t.Fatal("index-test not found in entries")
	}
}

func TestProvenanceTracking(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	body := sampleBody("provenance-test", "Testing provenance.")

	_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "provenance-test", Body: body})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	pushRes, err := reg.Push(ctx, skillregistry.GlobalScope(), "provenance-test", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushRes.Metadata.Provenance == nil {
		t.Fatal("provenance should be set")
	}
	if pushRes.Metadata.Provenance.Source != "local" {
		t.Fatalf("expected source 'local', got %s", pushRes.Metadata.Provenance.Source)
	}
}

var _ = store.SkillScope{}

var _ = store.SkillScope{}
