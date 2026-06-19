package skillregistry_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// writeSkill makes a minimal SKILL.md tree under root/name/, returning the
// dir path. Optional extras land alongside SKILL.md so the bundler has
// something to pack.
func writeSkill(t *testing.T, root, name, desc string, body string, extras map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "reference"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	md := body
	if md == "" {
		md = "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n\nBody for " + name + ".\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for path, content := range extras {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir extra: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write extra: %v", err)
		}
	}
	return dir
}

func TestDiscoverLocalSkills_ClassifiesNewDuplicateConflict(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()

	writeSkill(t, src, "alpha", "Use when alpha needed", "", nil)
	writeSkill(t, src, "beta", "Use when beta needed", "", nil)
	writeSkill(t, src, "gamma", "Use when gamma needed", "", nil)

	// Seed registry: alpha already present with identical body, gamma
	// present with different body, beta absent.
	alphaBody := "---\nname: alpha\ndescription: Use when alpha needed\n---\n# alpha\n\nBody for alpha.\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: alphaBody}); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	gammaBody := "---\nname: gamma\ndescription: DIFFERENT description here\n---\n# gamma\n\nOlder body.\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "gamma", Body: gammaBody}); err != nil {
		t.Fatalf("seed gamma: %v", err)
	}

	rows, err := reg.DiscoverLocalSkills(ctx, src)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	got := map[string]skillregistry.MigrationStatus{}
	for _, r := range rows {
		got[r.Name] = r.Status
	}
	if got["alpha"] != skillregistry.StatusDuplicate {
		t.Errorf("alpha = %s; want duplicate", got["alpha"])
	}
	if got["beta"] != skillregistry.StatusNew {
		t.Errorf("beta = %s; want new", got["beta"])
	}
	if got["gamma"] != skillregistry.StatusVersionConflict {
		t.Errorf("gamma = %s; want version-conflict", got["gamma"])
	}
}

func TestDiscoverLocalSkills_SkipsHiddenAndNonSkillDirs(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()

	writeSkill(t, src, "good", "Use when good", "", nil)
	if err := os.Mkdir(filepath.Join(src, ".migrated"), 0o700); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	if err := os.Mkdir(filepath.Join(src, "no-skill-md"), 0o755); err != nil {
		t.Fatalf("mkdir non-skill: %v", err)
	}

	rows, err := reg.DiscoverLocalSkills(ctx, src)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "good" {
		t.Fatalf("expected only 'good', got %+v", rows)
	}
}

func TestDiscoverLocalSkills_FlagsHarnessOwned(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()

	writeSkill(t, src, "using-mcplexer", "the bootstrap skill", "", nil)
	rows, err := reg.DiscoverLocalSkills(ctx, src)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != skillregistry.StatusHarnessOwned {
		t.Fatalf("expected harness-owned, got %+v", rows)
	}
}

func TestImportLocalSkill_RefusesHarnessOwned(t *testing.T) {
	reg, _ := newTestRegistry(t)
	src := t.TempDir()
	dir := writeSkill(t, src, "using-mcplexer", "the bootstrap skill", "", nil)

	res := reg.ImportLocalSkill(context.Background(), skillregistry.MigrateOptions{Path: dir})
	if res.Action != skillregistry.ActionFailed {
		t.Fatalf("action %s, want failed", res.Action)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("harness-owned dir must be left in place: %v", err)
	}
}

func TestDiscoverLocalSkills_FlagsUnparseable(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()

	dir := filepath.Join(src, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("no frontmatter at all"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rows, err := reg.DiscoverLocalSkills(ctx, src)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != skillregistry.StatusUnparseable {
		t.Fatalf("expected unparseable, got %+v", rows)
	}
	if rows[0].ParseError == "" {
		t.Errorf("expected ParseError, got empty")
	}
}

func TestImportLocalSkill_NewPublishesAndArchives(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", "Use when alpha needed", "", map[string]string{
		"reference/extra.md": "extra notes",
	})
	archive := filepath.Join(t.TempDir(), "migrated")

	res := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{
		Path:       dir,
		ArchiveDir: archive,
	})
	if res.Action != skillregistry.ActionImported {
		t.Fatalf("action=%s err=%s", res.Action, res.Error)
	}
	if res.Version != 1 {
		t.Errorf("version=%d, want 1", res.Version)
	}
	if res.BundleSHA256 == "" {
		t.Errorf("expected bundle sha")
	}
	if res.ArchivedTo == "" {
		t.Errorf("expected archive path")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("source dir should be moved, but still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.ArchivedTo, "SKILL.md")); err != nil {
		t.Errorf("archived SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.ArchivedTo, "reference", "extra.md")); err != nil {
		t.Errorf("archived extras missing: %v", err)
	}

	// Verify the registry got the bundle.
	bundle, sha, fetchErr := reg.FetchBundle(ctx, skillregistry.AdminScope(), "alpha", skillregistry.VersionRef{Latest: true})
	if fetchErr != nil {
		t.Fatalf("fetch bundle: %v", fetchErr)
	}
	if len(bundle) == 0 || sha != res.BundleSHA256 {
		t.Errorf("bundle mismatch: len=%d sha=%s want %s", len(bundle), sha, res.BundleSHA256)
	}
}

func TestImportLocalSkill_DuplicateSkipsAndStillArchives(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := "---\nname: alpha\ndescription: Use when alpha needed\n---\n# alpha\n\nBody for alpha.\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: body}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", "Use when alpha needed", body, nil)
	archive := filepath.Join(t.TempDir(), "migrated")

	res := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{
		Path:       dir,
		ArchiveDir: archive,
	})
	if res.Action != skillregistry.ActionSkipped {
		t.Fatalf("action=%s err=%s", res.Action, res.Error)
	}
	if res.ArchivedTo == "" {
		t.Errorf("expected archive path even on skip")
	}
}

func TestImportLocalSkill_VersionConflictRequiresOverwrite(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	oldBody := "---\nname: alpha\ndescription: original\n---\n# alpha\n\nold body.\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha", Body: oldBody}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", "rewritten", "", nil)

	// Without --overwrite: refuses.
	res := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{Path: dir})
	if res.Action != skillregistry.ActionFailed {
		t.Fatalf("expected failed without overwrite, got %s", res.Action)
	}
	if !strings.Contains(res.Error, "version-conflict") {
		t.Errorf("error=%q, expected version-conflict", res.Error)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("source dir should not be moved on failure, got %v", err)
	}

	// With overwrite: publishes v2.
	res2 := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{Path: dir, Overwrite: true})
	if res2.Action != skillregistry.ActionUpdated {
		t.Fatalf("expected updated, got %s err=%s", res2.Action, res2.Error)
	}
	if res2.Version != 2 {
		t.Errorf("expected v2, got %d", res2.Version)
	}
}

func TestImportLocalSkill_DryRunChangesNothing(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()
	dir := writeSkill(t, src, "alpha", "Use when alpha needed", "", nil)

	res := reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{Path: dir, DryRun: true})
	if res.Action != skillregistry.ActionImported {
		t.Errorf("dry-run action=%s; want imported (proposed)", res.Action)
	}
	if !res.DryRun {
		t.Errorf("DryRun should round-trip")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry-run must not move source: %v", err)
	}
	if _, err := reg.Get(ctx, skillregistry.AdminScope(), "alpha", skillregistry.VersionRef{Latest: true}); err == nil {
		t.Errorf("registry should not contain alpha after dry-run")
	}
}

func TestExpandUserHome(t *testing.T) {
	if got := skillregistry.ExpandUserHome(""); got != "" {
		t.Errorf("empty in/out: got %q", got)
	}
	if got := skillregistry.ExpandUserHome("/abs/path"); got != "/abs/path" {
		t.Errorf("abs path unchanged: got %q", got)
	}
	got := skillregistry.ExpandUserHome("~/.claude/skills")
	if !strings.HasSuffix(got, ".claude/skills") || strings.HasPrefix(got, "~/") {
		t.Errorf("home expansion failed: %q", got)
	}
}

func TestDefaultArchiveDir_ContainsTimestamp(t *testing.T) {
	dir := skillregistry.DefaultArchiveDir(mustParse(t, "2026-05-25T12:34:56Z"))
	if !strings.Contains(dir, "20260525T123456Z") {
		t.Errorf("missing timestamp: %s", dir)
	}
	if !strings.Contains(dir, ".migrated") {
		t.Errorf("missing .migrated segment: %s", dir)
	}
}
