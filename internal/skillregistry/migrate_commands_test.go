package skillregistry_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// writeCommand drops a flat command file under root, returning its path.
func writeCommand(t *testing.T, root, fileName, content string) string {
	t.Helper()
	p := filepath.Join(root, fileName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write command %s: %v", fileName, err)
	}
	return p
}

func TestDiscoverLocalCommands_Classifies(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()

	named := "---\nname: alpha-cmd\ndescription: Use when alpha needed\n---\nDo alpha things.\n"
	writeCommand(t, src, "alpha-cmd.md", named)
	writeCommand(t, src, "beta-cmd.md", "---\ndescription: Use when beta needed\n---\nDo beta things.\n")
	writeCommand(t, src, "no-desc.md", "---\ntitle: nope\n---\nBody.\n")
	writeCommand(t, src, "no-frontmatter.md", "Just a body, no fences.\n")
	writeCommand(t, src, "using-mcplexer.md", "---\ndescription: imposter\n---\nBody.\n")
	writeCommand(t, src, ".hidden.md", "---\ndescription: hidden\n---\nBody.\n")
	writeCommand(t, src, "notes.txt", "not markdown")
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// alpha-cmd already in the registry with the identical synthesized body.
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "alpha-cmd", Body: named}); err != nil {
		t.Fatalf("seed alpha-cmd: %v", err)
	}

	rows, err := reg.DiscoverLocalCommands(ctx, src)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	got := map[string]skillregistry.MigrationStatus{}
	for _, r := range rows {
		got[r.DirName] = r.Status
	}
	want := map[string]skillregistry.MigrationStatus{
		"alpha-cmd.md":      skillregistry.StatusDuplicate,
		"beta-cmd.md":       skillregistry.StatusNew,
		"no-desc.md":        skillregistry.StatusUnparseable,
		"no-frontmatter.md": skillregistry.StatusUnparseable,
		"using-mcplexer.md": skillregistry.StatusHarnessOwned,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d: %v", len(want), len(got), got)
	}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("%s: got %s, want %s", name, got[name], status)
		}
	}
	// The injected-name row must classify under the filename stem.
	for _, r := range rows {
		if r.DirName == "beta-cmd.md" && r.Name != "beta-cmd" {
			t.Errorf("beta-cmd.md: name %q, want beta-cmd", r.Name)
		}
	}
}

func TestImportLocalCommand_PublishesAndArchives(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()
	archive := filepath.Join(src, ".migrated", "now")

	p := writeCommand(t, src, "gamma-cmd.md", "---\ndescription: Use when gamma needed\n---\nDo gamma things.\n")
	res := reg.ImportLocalCommand(ctx, skillregistry.MigrateOptions{Path: p, ArchiveDir: archive})
	if res.Action != skillregistry.ActionImported {
		t.Fatalf("action %s (err %s), want imported", res.Action, res.Error)
	}
	if res.Name != "gamma-cmd" {
		t.Errorf("name %q, want gamma-cmd", res.Name)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("source file still present after archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive, "gamma-cmd.md")); err != nil {
		t.Errorf("archived copy missing: %v", err)
	}
	entry, err := reg.Get(ctx, skillregistry.AdminScope(), "gamma-cmd", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get gamma-cmd: %v", err)
	}
	if !strings.Contains(entry.Body, "name: gamma-cmd") {
		t.Errorf("registry body missing injected name:\n%s", entry.Body)
	}
	if !strings.Contains(entry.Body, "Do gamma things.") {
		t.Errorf("registry body missing original content:\n%s", entry.Body)
	}
}

func TestImportLocalCommand_DuplicateArchivesWithoutRepublish(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	src := t.TempDir()
	archive := filepath.Join(src, ".migrated", "now")

	body := "---\nname: delta-cmd\ndescription: Use when delta needed\n---\nDo delta things.\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "delta-cmd", Body: body}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := writeCommand(t, src, "delta-cmd.md", body)
	res := reg.ImportLocalCommand(ctx, skillregistry.MigrateOptions{Path: p, ArchiveDir: archive})
	if res.Action != skillregistry.ActionSkipped {
		t.Fatalf("action %s (err %s), want skipped", res.Action, res.Error)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("duplicate source should still be archived away: %v", err)
	}
}

func TestImportLocalCommand_RefusesHarnessOwned(t *testing.T) {
	reg, _ := newTestRegistry(t)
	src := t.TempDir()
	p := writeCommand(t, src, "using-mcplexer.md", "---\ndescription: imposter\n---\nBody.\n")
	res := reg.ImportLocalCommand(context.Background(), skillregistry.MigrateOptions{Path: p})
	if res.Action != skillregistry.ActionFailed {
		t.Fatalf("action %s, want failed", res.Action)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("harness-owned file must be left in place: %v", err)
	}
}
