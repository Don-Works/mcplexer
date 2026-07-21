package skillregistry_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

func TestExportImportSkillPackageRejectsComposition(t *testing.T) {
	source, _ := newTestRegistry(t)
	target, _ := newTestRegistry(t)
	ctx := context.Background()

	dependency := includeBody("sync-fragment", "Portable dependency fixture.", "", "# Fragment\n\nFRAGMENT\n")
	dep, err := source.Publish(ctx, skillregistry.PublishOptions{Name: "sync-fragment", Body: dependency})
	if err != nil {
		t.Fatalf("publish dependency: %v", err)
	}
	declaration := includeDeclaration("fragment", "sync-fragment", "global", dep.Version, dep.ContentHash, "")
	rootBody := includeBody("sync-composed", "Composed sync fixture.", declaration,
		"# Root\n\n<!-- mcpx:include fragment -->\n")
	if _, err := source.Publish(ctx, skillregistry.PublishOptions{Name: "sync-composed", Body: rootBody}); err != nil {
		t.Fatalf("publish root: %v", err)
	}

	if _, err := source.ExportSkill(ctx, skillregistry.GlobalScope(), skillregistry.ExportOptions{
		Name: "sync-composed",
	}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("export error = %v, want ErrCompositionNotPortable", err)
	}

	pkg := skillregistry.SyncPackage{
		Name:        "sync-composed",
		Version:     1,
		ContentHash: skillregistry.ComputeContentHash(rootBody),
		Body:        rootBody,
	}
	for _, opts := range []skillregistry.ImportOptions{
		{Package: pkg, DryRun: true},
		{Package: pkg, Commit: true},
	} {
		if _, err := target.ImportSkillPackage(ctx, opts); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
			t.Fatalf("import error = %v, want ErrCompositionNotPortable", err)
		}
	}
	heads, err := target.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		t.Fatalf("list target heads: %v", err)
	}
	if len(heads) != 0 {
		t.Fatalf("rejected imports mutated target: %+v", heads)
	}
}

func TestExportImportSkillPackageDryRunThenCommit(t *testing.T) {
	source, _ := newTestRegistry(t)
	target, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("sync-demo", "Use when testing skill sync import.")
	if _, err := source.Publish(ctx, skillregistry.PublishOptions{
		Name: "sync-demo", Body: body, Author: "alice",
	}); err != nil {
		t.Fatalf("source publish: %v", err)
	}
	pkg, err := source.ExportSkill(ctx, skillregistry.GlobalScope(), skillregistry.ExportOptions{
		Name: "sync-demo", ExportedBy: "test-agent",
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if pkg.Signature == "" {
		t.Fatal("export missing signature")
	}

	plan, err := target.ImportSkillPackage(ctx, skillregistry.ImportOptions{
		Package: *pkg, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run import: %v", err)
	}
	if plan.Action != skillregistry.SyncImported || !plan.WouldMutate || !plan.RequiresCommit {
		t.Fatalf("unexpected dry-run plan: %+v", plan)
	}
	heads, err := target.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		t.Fatalf("list target heads: %v", err)
	}
	if len(heads) != 0 {
		t.Fatalf("dry-run mutated target registry: %+v", heads)
	}

	plan, err = target.ImportSkillPackage(ctx, skillregistry.ImportOptions{
		Package: *pkg, Commit: true, Author: "puller",
	})
	if err != nil {
		t.Fatalf("commit import: %v", err)
	}
	if plan.Action != skillregistry.SyncImported || plan.PublishedVersion != 1 {
		t.Fatalf("unexpected commit plan: %+v", plan)
	}
	entry, err := target.Get(ctx, skillregistry.GlobalScope(), "sync-demo", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("target get: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(entry.MetadataJSON, &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if _, ok := meta["skill_sync"].(map[string]any); !ok {
		t.Fatalf("imported entry missing skill_sync provenance: %v", meta)
	}
}

func TestImportSkillPackageUpdatePlanIncludesDiff(t *testing.T) {
	source, _ := newTestRegistry(t)
	target, _ := newTestRegistry(t)
	ctx := context.Background()

	oldBody := sampleBody("sync-update", "Use when testing old sync content.")
	newBody := sampleBody("sync-update", "Use when testing new sync content.")
	if _, err := target.Publish(ctx, skillregistry.PublishOptions{Name: "sync-update", Body: oldBody}); err != nil {
		t.Fatalf("target publish: %v", err)
	}
	if _, err := source.Publish(ctx, skillregistry.PublishOptions{Name: "sync-update", Body: newBody}); err != nil {
		t.Fatalf("source publish: %v", err)
	}
	pkg, err := source.ExportSkill(ctx, skillregistry.GlobalScope(), skillregistry.ExportOptions{Name: "sync-update"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	plan, err := target.ImportSkillPackage(ctx, skillregistry.ImportOptions{Package: *pkg})
	if err != nil {
		t.Fatalf("plan import: %v", err)
	}
	if plan.Action != skillregistry.SyncUpdated || !plan.RequiresCommit {
		t.Fatalf("unexpected update plan: %+v", plan)
	}
	if plan.BodyDiff == "" || plan.FrontDiff == "" {
		t.Fatalf("expected body/frontmatter diffs, got %+v", plan)
	}
}

func TestExportImportSkillPackagePreservesBundle(t *testing.T) {
	source, _ := newTestRegistry(t)
	target, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("sync-bundle", "Use when testing bundled sync.")
	bundle := buildBundle(t, "sync-bundle", map[string]string{
		"SKILL.md":        body,
		"scripts/run.mjs": "console.log('sync');\n",
	})
	if _, err := source.Publish(ctx, skillregistry.PublishOptions{
		Name: "sync-bundle", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("source publish: %v", err)
	}
	pkg, err := source.ExportSkill(ctx, skillregistry.GlobalScope(), skillregistry.ExportOptions{
		Name: "sync-bundle", IncludeBundle: true,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if pkg.BundleB64 == "" || pkg.BundleSHA256 == "" {
		t.Fatalf("export missing bundle payload: %+v", pkg)
	}
	if _, err := target.ImportSkillPackage(ctx, skillregistry.ImportOptions{
		Package: *pkg, Commit: true,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	raw, sha, err := target.FetchBundle(ctx, skillregistry.GlobalScope(),
		"sync-bundle", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("fetch imported bundle: %v", err)
	}
	if len(raw) == 0 || sha != pkg.BundleSHA256 {
		t.Fatalf("bundle mismatch len=%d sha=%s want %s", len(raw), sha, pkg.BundleSHA256)
	}
}
