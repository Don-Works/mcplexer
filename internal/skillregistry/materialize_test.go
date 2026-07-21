package skillregistry_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func newTestMaterializer(t *testing.T) (*skillregistry.Materializer, *skillregistry.Registry, string) {
	t.Helper()
	reg, _ := newTestRegistry(t)
	mat := skillregistry.NewMaterializer(reg)
	target := t.TempDir()
	return mat, reg, target
}

func publishSkillWithBundle(t *testing.T, reg *skillregistry.Registry, name, desc string, bundleFiles map[string]string) {
	t.Helper()
	ctx := context.Background()
	body := sampleBody(name, desc)

	dir := t.TempDir()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for path, content := range bundleFiles {
		full := filepath.Join(skillDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir bundle file: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write bundle file: %v", err)
		}
	}

	res := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{
		Path:   skillDir,
		Author: "test",
	})
	if res.Error != "" {
		t.Fatalf("import %s: %s", name, res.Error)
	}
}

func readManagedManifest(t *testing.T, root string) map[string]skillregistry.ManagedEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".mcplexer-managed.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m skillregistry.ManagedManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m.Entries
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func dirExists(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMaterialize_CreatesSkillsFromRegistry(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha skill")
	publish(t, reg, nil, "beta", "Beta skill")

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(res.Created) != 2 {
		t.Errorf("created: got %d, want 2", len(res.Created))
	}
	if len(res.Skipped)+len(res.Updated)+len(res.Pruned) != 0 {
		t.Errorf("unexpected non-create actions: %+v", res)
	}

	for _, name := range []string{"alpha", "beta"} {
		md := fileContent(t, filepath.Join(target, name, "SKILL.md"))
		if !strings.Contains(md, "name: "+name) {
			t.Errorf("alpha SKILL.md missing frontmatter: %s", md[:100])
		}
	}

	manifest := readManagedManifest(t, target)
	if len(manifest) != 2 {
		t.Errorf("manifest entries: got %d, want 2", len(manifest))
	}
}

func TestMaterialize_RendersIncludesAfterBundleExtraction(t *testing.T) {
	mat, reg, targetRoot := newTestMaterializer(t)
	ctx := context.Background()
	fragmentBody := sampleBody("material-fragment", "Neutral materializer fragment.")
	fragment, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "material-fragment", Body: fragmentBody})
	if err != nil {
		t.Fatalf("publish fragment: %v", err)
	}
	rootBody := includeBody("material-root", "Neutral materializer root.",
		includeDeclaration("fragment", "material-fragment", "global", fragment.Version, fragment.ContentHash, ""),
		"ROOT BEFORE\n<!-- mcpx:include fragment -->\nROOT AFTER\n")
	bundle := buildBundle(t, "material-root", map[string]string{
		"SKILL.md":       rootBody,
		"scripts/run.sh": "echo neutral\n",
	})
	root, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "material-root", Body: rootBody, Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("publish root: %v", err)
	}

	result, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: targetRoot,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !containsString(result.Created, "material-root") {
		t.Fatalf("root not created: %+v", result)
	}
	installed := fileContent(t, filepath.Join(targetRoot, "material-root", "SKILL.md"))
	if !strings.Contains(installed, "Body content for material-fragment.") || strings.Contains(installed, "mcpx:include") {
		t.Fatalf("bundle raw SKILL.md won over rendered body: %s", installed)
	}
	if !dirExists(t, filepath.Join(targetRoot, "material-root", "scripts")) {
		t.Fatal("bundle asset directory was not extracted")
	}
	manifest := readManagedManifest(t, targetRoot)
	managed := manifest["material-root"]
	if managed.ContentHash != root.ContentHash || managed.RenderedHash != skillregistry.ComputeContentHash(installed) ||
		managed.RendererVersion != skillregistry.CompositionRendererVersion {
		t.Fatalf("managed raw/rendered integrity fields incorrect: %+v", managed)
	}

	second, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: targetRoot,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	if !containsString(second.Skipped, "material-root") {
		t.Fatalf("unchanged composed root was not skipped: %+v", second)
	}
}

func TestMaterialize_IdempotentOnSecondRun(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha skill")

	res1, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}
	if len(res1.Created) != 1 {
		t.Fatalf("first run: expected 1 created, got %d", len(res1.Created))
	}

	res2, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res2.Skipped) != 1 {
		t.Errorf("second run: expected 1 skipped, got created=%d skipped=%d updated=%d",
			len(res2.Created), len(res2.Skipped), len(res2.Updated))
	}
	if len(res2.Created)+len(res2.Updated)+len(res2.Pruned) != 0 {
		t.Errorf("second run should be no-op: %+v", res2)
	}
}

func TestMaterialize_UpdatesOnNewVersion(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha v1")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}

	publish(t, reg, nil, "alpha", "Alpha v2 revised")

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "alpha" {
		t.Errorf("expected alpha updated, got %+v", res)
	}

	md := fileContent(t, filepath.Join(target, "alpha", "SKILL.md"))
	if !strings.Contains(md, "Alpha v2 revised") {
		t.Errorf("SKILL.md not updated: %s", md[:100])
	}
}

func TestMaterialize_PrunesDeletedSkills(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha")
	publish(t, reg, nil, "beta", "Beta")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}

	if err := reg.SoftDelete(ctx, nil, "alpha", 0); err != nil {
		t.Fatalf("soft delete alpha: %v", err)
	}

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0] != "alpha" {
		t.Errorf("expected alpha pruned, got %+v", res)
	}
	if dirExists(t, filepath.Join(target, "alpha")) {
		t.Error("alpha dir should have been removed")
	}
	if !dirExists(t, filepath.Join(target, "beta")) {
		t.Error("beta dir should still exist")
	}
}

func TestMaterialize_PreservesUnmanagedDirs(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	unmanagedDir := filepath.Join(target, "my-custom-skill")
	if err := os.MkdirAll(unmanagedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	customContent := "---\nname: my-custom-skill\ndescription: Hand-authored\n---\n# Custom\n"
	if err := os.WriteFile(filepath.Join(unmanagedDir, "SKILL.md"), []byte(customContent), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	publish(t, reg, nil, "alpha", "Alpha")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if !dirExists(t, unmanagedDir) {
		t.Error("unmanaged dir should be preserved")
	}
	got := fileContent(t, filepath.Join(unmanagedDir, "SKILL.md"))
	if got != customContent {
		t.Error("unmanaged SKILL.md was modified")
	}
}

func TestMaterialize_AdoptsMatchingUnmanagedDir(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	body := sampleBody("alpha", "Alpha skill")
	if err := os.MkdirAll(filepath.Join(target, "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "alpha", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	publish(t, reg, nil, "alpha", "Alpha skill")

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if len(res.Adopted) != 1 || res.Adopted[0] != "alpha" {
		t.Errorf("expected alpha adopted, got %+v", res)
	}

	manifest := readManagedManifest(t, target)
	if _, ok := manifest["alpha"]; !ok {
		t.Error("alpha should be in manifest after adoption")
	}
}

func TestMaterialize_DriftRefusesAdoption(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(target, "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	driftedBody := "---\nname: alpha\ndescription: DIFFERENT\n---\n# Alpha\n\nModified locally.\n"
	if err := os.WriteFile(filepath.Join(target, "alpha", "SKILL.md"), []byte(driftedBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	publish(t, reg, nil, "alpha", "Alpha skill")

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if len(res.Skipped) != 1 {
		t.Errorf("expected alpha skipped (drifted), got %+v", res)
	}
	if len(res.Created)+len(res.Adopted) != 0 {
		t.Errorf("drifted file should not be created or adopted: %+v", res)
	}

	manifest := readManagedManifest(t, target)
	if _, ok := manifest["alpha"]; ok {
		t.Error("drifted alpha should NOT be in manifest")
	}

	got := fileContent(t, filepath.Join(target, "alpha", "SKILL.md"))
	if got != driftedBody {
		t.Error("drifted SKILL.md was modified despite refusal to adopt")
	}
}

func TestMaterialize_BundleAssetsCopied(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)

	publishSkillWithBundle(t, reg, "bundled-skill", "Has extras", map[string]string{
		"reference/guide.md": "# Guide\nInstructions here.",
		"scripts/run.sh":     "#!/bin/bash\necho hello",
	})

	res, err := mat.Materialize(context.Background(), skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("expected 1 created, got %+v", res)
	}

	guide := fileContent(t, filepath.Join(target, "bundled-skill", "reference", "guide.md"))
	if !strings.Contains(guide, "Instructions here") {
		t.Errorf("guide.md not extracted or wrong content: %s", guide)
	}
	script := fileContent(t, filepath.Join(target, "bundled-skill", "scripts", "run.sh"))
	if !strings.Contains(script, "echo hello") {
		t.Errorf("run.sh not extracted or wrong content: %s", script)
	}
}

func TestMaterialize_WorkspaceScope(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "global-skill", "Global")

	wsID := "ws-test"
	wsScope := store.SkillScope{WorkspaceIDs: []string{wsID}}
	publish(t, reg, ptr(wsID), "ws-skill", "Workspace-only")

	wsTarget := filepath.Join(target, "ws")
	if err := os.MkdirAll(wsTarget, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: wsTarget,
		Scope:      wsScope,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	created := map[string]bool{}
	for _, n := range res.Created {
		created[n] = true
	}
	if !created["global-skill"] {
		t.Error("workspace scope should see global-skill")
	}
	if !created["ws-skill"] {
		t.Error("workspace scope should see ws-skill")
	}
}

func TestMaterialize_EmptyRegistryPrunesAll(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha")
	publish(t, reg, nil, "beta", "Beta")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}

	if err := reg.SoftDelete(ctx, nil, "alpha", 0); err != nil {
		t.Fatalf("delete alpha: %v", err)
	}
	if err := reg.SoftDelete(ctx, nil, "beta", 0); err != nil {
		t.Fatalf("delete beta: %v", err)
	}

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res.Pruned) != 2 {
		t.Errorf("expected 2 pruned, got %d", len(res.Pruned))
	}

	entries, _ := os.ReadDir(target)
	for _, e := range entries {
		if e.Name() == ".mcplexer-managed.json" {
			continue
		}
		t.Errorf("unexpected entry after prune-all: %s", e.Name())
	}

	manifest := readManagedManifest(t, target)
	if len(manifest) != 0 {
		t.Errorf("manifest should be empty, got %d entries", len(manifest))
	}
}

func TestMaterialize_ManifestPreservesBetweenRuns(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	manifest := readManagedManifest(t, target)
	if e, ok := manifest["alpha"]; !ok {
		t.Error("alpha missing from manifest")
	} else if e.Version != 1 {
		t.Errorf("alpha version: got %d, want 1", e.Version)
	}
}

func TestMaterialize_AdoptedManagedThenUpdated(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	body := sampleBody("alpha", "Alpha v1")
	if err := os.MkdirAll(filepath.Join(target, "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "alpha", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	publish(t, reg, nil, "alpha", "Alpha v1")

	res1, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}
	if len(res1.Adopted) != 1 {
		t.Fatalf("expected adoption, got %+v", res1)
	}

	publish(t, reg, nil, "alpha", "Alpha v2 revised")

	res2, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res2.Updated) != 1 || res2.Updated[0] != "alpha" {
		t.Errorf("adopted skill should be updatable, got %+v", res2)
	}

	md := fileContent(t, filepath.Join(target, "alpha", "SKILL.md"))
	if !strings.Contains(md, "Alpha v2 revised") {
		t.Errorf("SKILL.md not updated after adoption: %s", md[:100])
	}
}

func TestMaterialize_NoClobberUnmanagedOnPrune(t *testing.T) {
	mat, reg, target := newTestMaterializer(t)
	ctx := context.Background()

	publish(t, reg, nil, "alpha", "Alpha")

	_, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}

	if err := reg.SoftDelete(ctx, nil, "alpha", 0); err != nil {
		t.Fatalf("delete: %v", err)
	}

	unmanagedDir := filepath.Join(target, "handcrafted")
	if err := os.MkdirAll(unmanagedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unmanagedDir, "SKILL.md"), []byte("---\nname: handcrafted\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0] != "alpha" {
		t.Errorf("expected only alpha pruned, got %+v", res)
	}
	if !dirExists(t, unmanagedDir) {
		t.Error("unmanaged handcrafted dir was incorrectly pruned")
	}
}

func TestMaterialize_PruneRejectsManifestPathEscape(t *testing.T) {
	mat, _, target := newTestMaterializer(t)
	ctx := context.Background()

	outside := filepath.Join(filepath.Dir(target), "outside-skill")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}

	manifest := skillregistry.ManagedManifest{
		Entries: map[string]skillregistry.ManagedEntry{
			"../outside-skill": {Version: 1, ContentHash: "bad"},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, ".mcplexer-managed.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	res, err := mat.Materialize(ctx, skillregistry.MaterializeScope{
		TargetRoot: target,
		Scope:      skillregistry.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0] != "../outside-skill" {
		t.Fatalf("expected invalid manifest entry pruned, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "sentinel.txt")); err != nil {
		t.Fatalf("outside sentinel should remain untouched: %v", err)
	}

	entries := readManagedManifest(t, target)
	if len(entries) != 0 {
		t.Fatalf("invalid manifest entry should be removed, got %+v", entries)
	}
}
