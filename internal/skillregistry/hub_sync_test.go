package skillregistry_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestHubSyncPushRejectsCompositionAndManifestOmitsIt(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	depBody := includeBody("hub-fragment", "Hub dependency fixture.", "", "# Fragment\n\nTEXT\n")
	dep, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "hub-fragment", Body: depBody})
	if err != nil {
		t.Fatalf("publish dependency: %v", err)
	}
	decl := includeDeclaration("fragment", "hub-fragment", "global", dep.Version, dep.ContentHash, "")
	rootBody := includeBody("hub-composed", "Hub composed fixture.", decl,
		"# Root\n\n<!-- mcpx:include fragment -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "hub-composed", Body: rootBody}); err != nil {
		t.Fatalf("publish root: %v", err)
	}

	if result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(), Names: []string{"hub-fragment", "hub-composed"},
	}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) || result != nil {
		t.Fatalf("push error = %v, want ErrCompositionNotPortable", err)
	}
	if manifest, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(), Names: []string{"hub-fragment", "hub-composed"},
	}); !errors.Is(err, skillregistry.ErrCompositionNotPortable) || manifest != nil {
		t.Fatalf("explicit manifest = %+v, error = %v; want typed rejection", manifest, err)
	}
	manifest, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{Scope: skillregistry.GlobalScope()})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	for _, entry := range manifest.Entries {
		if entry.Name == "hub-composed" {
			t.Fatalf("composed root was advertised in manifest: %+v", entry)
		}
	}
}

func TestHubSyncPullPreflightRejectsCompositionWithoutPartialWrites(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()
	plainBody := sampleSkillBody("hub-plain-remote", "Plain remote fixture")
	composedBody := includeBody("hub-composed-remote", "Composed remote fixture.",
		includeDeclaration("fragment", "missing-fragment", "global", 1, fmt.Sprintf("%064x", 1), ""),
		"# Root\n\n<!-- mcpx:include fragment -->\n")
	packages := []skillregistry.HubPackageEnvelope{
		sealTestPackage(t, skillregistry.HubPackage{
			Manifest: skillregistry.HubManifestEntry{
				Name: "hub-plain-remote", Version: 1, ContentHash: skillregistry.ComputeContentHash(plainBody),
			},
			Body: plainBody,
		}),
		sealTestPackage(t, skillregistry.HubPackage{
			Manifest: skillregistry.HubManifestEntry{
				Name: "hub-composed-remote", Version: 1, ContentHash: skillregistry.ComputeContentHash(composedBody),
			},
			Body: composedBody,
		}),
	}

	if _, err := svc.PlanPull(ctx, packages); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("plan error = %v, want ErrCompositionNotPortable", err)
	}
	for _, opts := range []skillregistry.HubSyncPullOptions{
		{Scope: skillregistry.GlobalScope(), Packages: packages, DryRun: true},
		{Scope: skillregistry.GlobalScope(), Packages: packages, Commit: true},
	} {
		if _, err := svc.Pull(ctx, opts); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
			t.Fatalf("pull error = %v, want ErrCompositionNotPortable", err)
		}
	}
	if _, err := svc.DiffPull(ctx, packages); !errors.Is(err, skillregistry.ErrCompositionNotPortable) {
		t.Fatalf("diff error = %v, want ErrCompositionNotPortable", err)
	}
	heads, err := reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		t.Fatalf("list heads: %v", err)
	}
	if len(heads) != 0 {
		t.Fatalf("failed batch partially mutated registry: %+v", heads)
	}
}

func newTestHubSync(t *testing.T) (*skillregistry.HubSyncService, *skillregistry.Registry) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := skillregistry.New(db)
	return skillregistry.NewHubSyncService(reg), reg
}

func sampleSkillBody(name, desc string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n# %s\n\nBody for %s.\n", name, desc, name, name)
}

func publishSkill(t *testing.T, reg *skillregistry.Registry, ctx context.Context, name, desc string) *skillregistry.PublishResult {
	t.Helper()
	res, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:   name,
		Body:   sampleSkillBody(name, desc),
		Author: "test-author",
	})
	if err != nil {
		t.Fatalf("publish %s: %v", name, err)
	}
	return res
}

func TestBuildManifest(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "alpha", "Alpha skill")
	publishSkill(t, reg, ctx, "beta", "Beta skill")
	publishSkill(t, reg, ctx, "gamma", "Gamma skill")

	manifest, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}

	if manifest.Version != skillregistry.HubSyncManifestVersion {
		t.Errorf("manifest version = %d, want %d", manifest.Version, skillregistry.HubSyncManifestVersion)
	}
	if len(manifest.Entries) != 3 {
		t.Fatalf("manifest entries = %d, want 3", len(manifest.Entries))
	}
	if manifest.ManifestSHA == "" {
		t.Error("manifest sha is empty")
	}

	names := make([]string, len(manifest.Entries))
	for i, e := range manifest.Entries {
		names[i] = e.Name
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing entry %q in manifest", want)
		}
	}
}

func TestBuildManifestFiltersByName(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "alpha", "Alpha skill")
	publishSkill(t, reg, ctx, "beta", "Beta skill")

	manifest, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
		Names: []string{"alpha"},
	})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}

	if len(manifest.Entries) != 1 {
		t.Fatalf("manifest entries = %d, want 1", len(manifest.Entries))
	}
	if manifest.Entries[0].Name != "alpha" {
		t.Errorf("entry name = %q, want alpha", manifest.Entries[0].Name)
	}
}

func TestPushCreatesValidEnvelopes(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "push-test", "Push test skill")

	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope:  skillregistry.GlobalScope(),
		Names:  []string{"push-test"},
		Author: "test-pusher",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(result.Packaged) != 1 {
		t.Fatalf("packaged = %d, want 1", len(result.Packaged))
	}

	env := result.Packaged[0]
	if env.Package.Manifest.Name != "push-test" {
		t.Errorf("name = %q, want push-test", env.Package.Manifest.Name)
	}
	if env.SHA256 == "" {
		t.Error("envelope SHA256 is empty")
	}
	if env.SignedBy != "test-pusher" {
		t.Errorf("signed_by = %q, want test-pusher", env.SignedBy)
	}
	if env.SignedAt.IsZero() {
		t.Error("signed_at is zero")
	}
	if env.Package.Body == "" {
		t.Error("package body is empty")
	}
}

func TestPushSkipsMissing(t *testing.T) {
	svc, _ := newTestHubSync(t)
	ctx := context.Background()

	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
		Names: []string{"nonexistent"},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(result.Packaged) != 0 {
		t.Errorf("packaged = %d, want 0", len(result.Packaged))
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "nonexistent" {
		t.Errorf("skipped = %v, want [nonexistent]", result.Skipped)
	}
}

func TestVerifyEnvelopeAcceptsValid(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "verify-test", "Verify test")

	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
		Names: []string{"verify-test"},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if err := skillregistry.VerifyEnvelope(&result.Packaged[0]); err != nil {
		t.Errorf("VerifyEnvelope on valid envelope: %v", err)
	}
}

func TestVerifyEnvelopeRejectsTampered(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "tamper-test", "Tamper test")

	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
		Names: []string{"tamper-test"},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	env := result.Packaged[0]
	env.SHA256 = "deadbeef"
	if err := skillregistry.VerifyEnvelope(&env); err == nil {
		t.Error("expected error for tampered envelope")
	}
}

func TestPullDryRunNoMutation(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "existing", "Existing skill")

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "new-skill",
			Version:     1,
			ContentHash: "abc123",
			Description: "A new remote skill",
		},
		Body: sampleSkillBody("new-skill", "A new remote skill"),
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     true,
		Commit:     false,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull dry-run: %v", err)
	}

	if !result.DryRun {
		t.Error("result.DryRun = false, want true")
	}
	if len(result.Applied) != 0 {
		t.Errorf("applied = %d, want 0 in dry-run", len(result.Applied))
	}
	if len(result.Plan.ToAdd) != 1 {
		t.Errorf("plan.to_add = %d, want 1", len(result.Plan.ToAdd))
	}
	if result.Plan.ToAdd[0].Name != "new-skill" {
		t.Errorf("plan.to_add[0].Name = %q, want new-skill", result.Plan.ToAdd[0].Name)
	}

	_, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "new-skill", skillregistry.VersionRef{Latest: true})
	if getErr == nil {
		t.Error("dry-run should not have written to registry")
	}
}

func TestPullRequiresCommitForMutation(t *testing.T) {
	svc, _ := newTestHubSync(t)
	ctx := context.Background()

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "commit-test",
			Version:     1,
			ContentHash: "abc123",
			Description: "Commit test",
		},
		Body: sampleSkillBody("commit-test", "Commit test"),
	}
	env := sealTestPackage(t, pkg)

	_, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages: []skillregistry.HubPackageEnvelope{env},
		DryRun:   false,
		Commit:   false,
	})
	if err == nil {
		t.Error("expected error when commit=false and dry_run=false")
	}
}

func TestPullCommitWritesToRegistry(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	body := sampleSkillBody("pull-commit", "Pulled skill")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "pull-commit",
			Version:     1,
			ContentHash: "abc123",
			Description: "Pulled skill",
		},
		Body: body,
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
		Author:     "pull-agent",
	})
	if err != nil {
		t.Fatalf("Pull commit: %v", err)
	}

	if len(result.Applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(result.Applied))
	}

	applied := result.Applied[0]
	if applied.Name != "pull-commit" {
		t.Errorf("applied name = %q, want pull-commit", applied.Name)
	}
	if applied.Provenance.Source != "hub_pull" {
		t.Errorf("provenance source = %q, want hub_pull", applied.Provenance.Source)
	}
	if applied.Provenance.SourcePeer != "test-hub" {
		t.Errorf("provenance source_peer = %q, want test-hub", applied.Provenance.SourcePeer)
	}
	if applied.Provenance.OriginalSHA == "" {
		t.Error("provenance original_sha is empty")
	}
	if applied.Provenance.PulledAt.IsZero() {
		t.Error("provenance pulled_at is zero")
	}

	entry, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "pull-commit", skillregistry.VersionRef{Latest: true})
	if getErr != nil {
		t.Fatalf("get pulled skill: %v", getErr)
	}
	if entry.Body != body {
		t.Error("pulled entry body does not match original")
	}
	if entry.SourceType != "hub" {
		t.Errorf("source_type = %q, want hub", entry.SourceType)
	}
}

func TestPullSkipsIdentical(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	res := publishSkill(t, reg, ctx, "identical-test", "Same skill")

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "identical-test",
			Version:     res.Version,
			ContentHash: res.ContentHash,
			Description: "Same skill",
		},
		Body: sampleSkillBody("identical-test", "Same skill"),
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(result.Applied) != 0 {
		t.Errorf("applied = %d, want 0 for identical", len(result.Applied))
	}
	if len(result.Plan.ToSkip) != 1 {
		t.Errorf("plan.to_skip = %d, want 1", len(result.Plan.ToSkip))
	}
}

func TestPullDetectsConflicts(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "conflict-test", "Local version")

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "conflict-test",
			Version:     1,
			ContentHash: "fake-hash-different",
			Description: "Remote version",
		},
		Body: sampleSkillBody("conflict-test", "Remote version"),
	}
	env := sealTestPackage(t, pkg)

	_, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages: []skillregistry.HubPackageEnvelope{env},
		DryRun:   false,
		Commit:   true,
	})
	if err == nil {
		t.Error("expected conflict error")
	}
}

func TestPullPlanDetectsConflict(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	localRes := publishSkill(t, reg, ctx, "plan-conflict", "Local")

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "plan-conflict",
			Version:     localRes.Version,
			ContentHash: "different-hash",
			Description: "Remote",
		},
		Body: sampleSkillBody("plan-conflict", "Remote body"),
	}
	env := sealTestPackage(t, pkg)

	plan, err := svc.PlanPull(ctx, []skillregistry.HubPackageEnvelope{env})
	if err != nil {
		t.Fatalf("PlanPull: %v", err)
	}

	if len(plan.Conflict) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(plan.Conflict))
	}
	if plan.Conflict[0].Name != "plan-conflict" {
		t.Errorf("conflict name = %q, want plan-conflict", plan.Conflict[0].Name)
	}
	if plan.Conflict[0].Change != "conflict" {
		t.Errorf("change = %q, want conflict", plan.Conflict[0].Change)
	}
}

func TestPullUpdatesExistingSkill(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	localRes := publishSkill(t, reg, ctx, "update-test", "V1")

	remoteBody := sampleSkillBody("update-test", "V2 remote content")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "update-test",
			Version:     localRes.Version + 1,
			ContentHash: "remote-hash-v2",
			Description: "V2 remote content",
		},
		Body: remoteBody,
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(result.Applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(result.Applied))
	}
	if result.Applied[0].Provenance.LocalAction != "created" {
		t.Errorf("local action = %q, want created (new version)", result.Applied[0].Provenance.LocalAction)
	}

	entry, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "update-test", skillregistry.VersionRef{Latest: true})
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if entry.Body != remoteBody {
		t.Error("entry body should match remote body")
	}
	if entry.Version <= localRes.Version {
		t.Errorf("version = %d, want > %d", entry.Version, localRes.Version)
	}
}

func TestPushPreservesBundle(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	bundle := buildBundle(t, "", map[string]string{"SKILL.md": sampleSkillBody("bundle-push", "Bundle push")})
	res, pubErr := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:   "bundle-push",
		Body:   sampleSkillBody("bundle-push", "Bundle push"),
		Author: "test",
		Bundle: bundle,
	})
	if pubErr != nil {
		t.Fatalf("publish: %v", pubErr)
	}
	if res.BundleSize == 0 {
		t.Fatal("bundle size is 0")
	}

	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
		Names: []string{"bundle-push"},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(result.Packaged) != 1 {
		t.Fatalf("packaged = %d, want 1", len(result.Packaged))
	}
	env := result.Packaged[0]
	if len(env.Package.Bundle) == 0 {
		t.Error("package bundle is empty")
	}
	if env.Package.Manifest.BundleSHA256 == "" {
		t.Error("manifest bundle_sha256 is empty")
	}
}

func TestPullWithBundlePreservesBundle(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	body := sampleSkillBody("pull-bundle", "Pull with bundle")
	bundle := buildBundle(t, "", map[string]string{"SKILL.md": body})

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:         "pull-bundle",
			Version:      1,
			ContentHash:  "bundle-hash",
			Description:  "Pull with bundle",
			BundleSHA256: "will-be-computed",
		},
		Body:   body,
		Bundle: bundle,
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(result.Applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(result.Applied))
	}
	if result.Applied[0].BundleSize == 0 {
		t.Error("applied bundle size is 0")
	}
	if result.Applied[0].BundleSHA256 == "" {
		t.Error("applied bundle sha256 is empty")
	}

	fetchedBundle, sha, fetchErr := reg.FetchBundle(ctx, skillregistry.GlobalScope(), "pull-bundle", skillregistry.VersionRef{Latest: true})
	if fetchErr != nil {
		t.Fatalf("FetchBundle: %v", fetchErr)
	}
	if len(fetchedBundle) == 0 {
		t.Error("fetched bundle is empty")
	}
	if sha == "" {
		t.Error("fetched bundle sha is empty")
	}
}

func TestDiffPullShowsChanges(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "diff-pull", "V1")

	newBody := sampleSkillBody("diff-pull", "V2 changed content")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "diff-pull",
			Version:     2,
			ContentHash: "diff-hash-v2",
			Description: "V2 changed content",
		},
		Body: newBody,
	}
	env := sealTestPackage(t, pkg)

	diffs, err := svc.DiffPull(ctx, []skillregistry.HubPackageEnvelope{env})
	if err != nil {
		t.Fatalf("DiffPull: %v", err)
	}

	if len(diffs) != 1 {
		t.Fatalf("diffs = %d, want 1", len(diffs))
	}
	d := diffs[0]
	if d.Name != "diff-pull" {
		t.Errorf("name = %q, want diff-pull", d.Name)
	}
	if d.OldVersion != 1 {
		t.Errorf("old_version = %d, want 1", d.OldVersion)
	}
	if d.NewVersion != 2 {
		t.Errorf("new_version = %d, want 2", d.NewVersion)
	}
	if d.BodyDiff == "" {
		t.Error("body_diff is empty, expected changes")
	}
}

func TestDiffPullNewSkill(t *testing.T) {
	svc, _ := newTestHubSync(t)
	ctx := context.Background()

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "brand-new",
			Version:     1,
			ContentHash: "new-hash",
			Description: "Brand new skill",
		},
		Body: sampleSkillBody("brand-new", "Brand new skill"),
	}
	env := sealTestPackage(t, pkg)

	diffs, err := svc.DiffPull(ctx, []skillregistry.HubPackageEnvelope{env})
	if err != nil {
		t.Fatalf("DiffPull: %v", err)
	}

	if len(diffs) != 1 {
		t.Fatalf("diffs = %d, want 1", len(diffs))
	}
	if diffs[0].OldVersion != 0 {
		t.Errorf("old_version = %d, want 0 (new)", diffs[0].OldVersion)
	}
	if diffs[0].BodyDiff == "" {
		t.Error("expected body_diff for new skill")
	}
}

func TestPullProvenanceInMetadata(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	body := sampleSkillBody("prov-test", "Provenance test")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "prov-test",
			Version:     1,
			ContentHash: "prov-hash",
			Description: "Provenance test",
		},
		Body: body,
	}
	env := sealTestPackage(t, pkg)

	_, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "hub-peer-123",
		Author:     "pull-agent",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	entry, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "prov-test", skillregistry.VersionRef{Latest: true})
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal(entry.MetadataJSON, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}

	provRaw, ok := meta["hub_provenance"]
	if !ok {
		t.Fatal("metadata missing hub_provenance key")
	}
	var prov skillregistry.ProvenanceInfo
	if err := json.Unmarshal(provRaw, &prov); err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if prov.Source != "hub_pull" {
		t.Errorf("provenance source = %q, want hub_pull", prov.Source)
	}
	if prov.SourcePeer != "hub-peer-123" {
		t.Errorf("provenance source_peer = %q, want hub-peer-123", prov.SourcePeer)
	}
	if prov.PulledAt.IsZero() {
		t.Error("provenance pulled_at is zero")
	}
	if prov.OriginalSHA == "" {
		t.Error("provenance original_sha is empty")
	}
}

func TestPullScopedToGlobal(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	body := sampleSkillBody("scope-test", "Scoped skill")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "scope-test",
			Version:     1,
			ContentHash: "scope-hash",
			Description: "Scoped skill",
		},
		Body: body,
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(result.Applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(result.Applied))
	}

	entry, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "scope-test", skillregistry.VersionRef{Latest: true})
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if entry.WorkspaceID != nil {
		t.Error("hub-pulled skill should be global (nil workspace)")
	}
}

func TestPullScopedToWorkspace(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	ws := "ws-hub-pull"
	body := sampleSkillBody("scope-workspace", "Workspace scoped skill")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "scope-workspace",
			Version:     1,
			ContentHash: "scope-workspace-hash",
			Description: "Workspace scoped skill",
		},
		Body: body,
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Scope:      store.SkillScope{WorkspaceIDs: []string{ws}},
		Packages:   []skillregistry.HubPackageEnvelope{env},
		DryRun:     false,
		Commit:     true,
		SourcePeer: "test-hub",
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(result.Applied))
	}

	entry, getErr := reg.Get(ctx, store.SkillScope{WorkspaceIDs: []string{ws}}, "scope-workspace", skillregistry.VersionRef{Latest: true})
	if getErr != nil {
		t.Fatalf("get scoped entry: %v", getErr)
	}
	if entry.WorkspaceID == nil || *entry.WorkspaceID != ws {
		t.Fatalf("workspace_id = %v, want %s", entry.WorkspaceID, ws)
	}
	if _, globalErr := reg.Get(ctx, skillregistry.GlobalScope(), "scope-workspace", skillregistry.VersionRef{Latest: true}); globalErr == nil {
		t.Fatal("workspace pull should not create a global entry")
	}
}

func TestPullRejectsMultiWorkspaceCommit(t *testing.T) {
	svc, _ := newTestHubSync(t)
	ctx := context.Background()

	body := sampleSkillBody("scope-multi", "Multi workspace pull")
	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "scope-multi",
			Version:     1,
			ContentHash: "scope-multi-hash",
			Description: "Multi workspace pull",
		},
		Body: body,
	}
	env := sealTestPackage(t, pkg)

	_, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Scope:    store.SkillScope{WorkspaceIDs: []string{"ws-one", "ws-two"}},
		Packages: []skillregistry.HubPackageEnvelope{env},
		Commit:   true,
	})
	if err == nil {
		t.Fatal("expected error for multi-workspace commit")
	}
}

func TestManifestSHAStable(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "sha-test", "SHA test")

	m1, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("BuildManifest 1: %v", err)
	}

	m2, err := svc.BuildManifest(ctx, skillregistry.HubSyncPushOptions{
		Scope: skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("BuildManifest 2: %v", err)
	}

	if m1.ManifestSHA == "" {
		t.Fatal("manifest SHA is empty")
	}

	m2CleanSHA := m2.ManifestSHA
	if m1.ManifestSHA != m2CleanSHA {
		if m1.GeneratedAt.Equal(m2.GeneratedAt) {
			t.Errorf("manifest SHA unstable: %s vs %s", m1.ManifestSHA, m2CleanSHA)
		}
	}
}

func TestPullDryRunPreservesConflictsInPlan(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	localRes := publishSkill(t, reg, ctx, "dry-conflict", "Local")

	pkg := skillregistry.HubPackage{
		Manifest: skillregistry.HubManifestEntry{
			Name:        "dry-conflict",
			Version:     localRes.Version,
			ContentHash: "different",
			Description: "Remote",
		},
		Body: sampleSkillBody("dry-conflict", "Remote"),
	}
	env := sealTestPackage(t, pkg)

	result, err := svc.Pull(ctx, skillregistry.HubSyncPullOptions{
		Packages: []skillregistry.HubPackageEnvelope{env},
		DryRun:   true,
		Commit:   false,
	})
	if err != nil {
		t.Fatalf("Pull dry-run: %v", err)
	}

	if !result.DryRun {
		t.Error("expected DryRun=true")
	}
	if len(result.Conflicts) != 1 {
		t.Errorf("conflicts = %d, want 1", len(result.Conflicts))
	}
	if result.Conflicts[0] != "dry-conflict" {
		t.Errorf("conflict = %q, want dry-conflict", result.Conflicts[0])
	}
}

func TestEnvelopeTimestamps(t *testing.T) {
	svc, reg := newTestHubSync(t)
	ctx := context.Background()

	publishSkill(t, reg, ctx, "ts-test", "Timestamp test")

	before := time.Now().UTC()
	result, err := svc.Push(ctx, skillregistry.HubSyncPushOptions{
		Scope:  skillregistry.GlobalScope(),
		Names:  []string{"ts-test"},
		Author: "ts-tester",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	after := time.Now().UTC()

	env := result.Packaged[0]
	if env.SignedAt.Before(before) || env.SignedAt.After(after) {
		t.Errorf("signed_at %v outside [%v, %v]", env.SignedAt, before, after)
	}
}

func sealTestPackage(t *testing.T, pkg skillregistry.HubPackage) skillregistry.HubPackageEnvelope {
	t.Helper()
	data, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	sum := sha256Of(data)
	return skillregistry.HubPackageEnvelope{
		Package:    pkg,
		SHA256:     sum,
		SignedBy:   "test",
		SignedAt:   time.Now().UTC(),
		Provenance: "test-source",
	}
}

func sha256Of(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}
