package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeSkillLister returns a fixed set of skill heads for the export test.
type fakeSkillLister struct {
	entries []store.SkillRegistryEntry
}

func (f *fakeSkillLister) ListSkillRegistryHeads(
	_ context.Context, _ store.SkillScope, _ int,
) ([]store.SkillRegistryEntry, error) {
	return f.entries, nil
}

func TestExportSkills_WritesSkillMD(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	body := "---\nname: mcplexer-tasks\nversion: 3\n---\n\nThe tasks skill.\n"
	lister := &fakeSkillLister{entries: []store.SkillRegistryEntry{
		{Name: "mcplexer-tasks", Version: 3, Body: body},
		{Name: "empty-skill", Version: 1, Body: ""}, // skipped (empty body)
	}}

	if err := ser.ExportSkills(ctx, lister); err != nil {
		t.Fatalf("ExportSkills: %v", err)
	}

	path := filepath.Join(cfg.GlobalDir(), "skills", "mcplexer-tasks", "v3", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exported SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "The tasks skill.") {
		t.Errorf("SKILL.md body not exported:\n%s", data)
	}

	// The empty-body skill must NOT be written.
	if _, err := os.Stat(filepath.Join(cfg.GlobalDir(), "skills", "empty-skill", "v1", "SKILL.md")); err == nil {
		t.Error("empty-body skill should have been skipped")
	}
}

// TestExportSkills_ReExportNeverConflicts is the regression guard for the
// brain_errors spam: skills are a one-way registry-canonical projection, so
// re-exporting an unchanged registry (the every-daemon-restart case) must be a
// silent no-op — it must NOT treat its own prior export as a "concurrent edit",
// write a .conflict sidecar, or record a brain_errors row. Before the fix this
// recorded one conflict per skill per restart, unbounded.
func TestExportSkills_ReExportNeverConflicts(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	lister := &fakeSkillLister{entries: []store.SkillRegistryEntry{
		{Name: "alpha", Version: 1, Body: "---\nname: alpha\n---\n\nAlpha body.\n"},
		{Name: "beta", Version: 2, Body: "---\nname: beta\n---\n\nBeta body.\n"},
	}}

	// Export three times — once to create, twice to simulate daemon restarts.
	for i := range 3 {
		if err := ser.ExportSkills(ctx, lister); err != nil {
			t.Fatalf("ExportSkills run %d: %v", i, err)
		}
	}

	errs, err := st.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-export recorded %d brain_errors, want 0: %+v", len(errs), errs)
	}

	// No .conflict sidecars must exist anywhere under the skills tree.
	skillsRoot := filepath.Join(cfg.GlobalDir(), "skills")
	_ = filepath.WalkDir(skillsRoot, func(p string, _ os.DirEntry, _ error) error {
		if strings.HasSuffix(p, ".conflict") {
			t.Errorf("unexpected conflict sidecar: %s", p)
		}
		return nil
	})

	// A registry move (changed body) overwrites the projection — registry wins.
	lister.entries[0].Body = "---\nname: alpha\n---\n\nAlpha body v2.\n"
	if err := ser.ExportSkills(ctx, lister); err != nil {
		t.Fatalf("ExportSkills after change: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(skillsRoot, "alpha", "v1", "SKILL.md"))
	if err != nil {
		t.Fatalf("read updated alpha: %v", err)
	}
	if !strings.Contains(string(got), "Alpha body v2.") {
		t.Errorf("registry change not projected; SKILL.md still stale:\n%s", got)
	}
}

// TestExportSkills_HealsStaleConflict proves a re-export drains the
// false-positive conflict bookkeeping left by the old guarded path: a
// pre-existing _file brain_errors row + .conflict sidecar for a skill path are
// both cleared once the (unchanged) skill is re-exported.
func TestExportSkills_HealsStaleConflict(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	lister := &fakeSkillLister{entries: []store.SkillRegistryEntry{
		{Name: "gamma", Version: 1, Body: "---\nname: gamma\n---\n\nGamma body.\n"},
	}}
	if err := ser.ExportSkills(ctx, lister); err != nil {
		t.Fatalf("seed export: %v", err)
	}
	path := filepath.Join(cfg.GlobalDir(), "skills", "gamma", "v1", "SKILL.md")

	// Simulate the old path's residue: a stale conflict row + sidecar.
	if err := st.RecordBrainError(ctx, &store.BrainError{
		Path: path, EntityKind: "skill", Field: "_file",
		Reason: "outbound write conflicted with a concurrent edit; wrote SKILL.md.conflict",
	}); err != nil {
		t.Fatalf("seed brain error: %v", err)
	}
	if err := os.WriteFile(path+".conflict", []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	if err := ser.ExportSkills(ctx, lister); err != nil {
		t.Fatalf("re-export: %v", err)
	}

	errs, err := st.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("stale conflict not healed: %d errors remain: %+v", len(errs), errs)
	}
	if _, err := os.Stat(path + ".conflict"); !os.IsNotExist(err) {
		t.Errorf("stale .conflict sidecar not removed (stat err=%v)", err)
	}
}

// TestExportSkills_HostileSkillNameNoEscape verifies that a skill whose name
// contains path traversal characters is silently skipped (never writes outside
// the brain root).
func TestExportSkills_HostileSkillNameNoEscape(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	// A skill whose name is an absolute path or parent traversal.
	lister := &fakeSkillLister{entries: []store.SkillRegistryEntry{
		{Name: "../../escape", Version: 1, Body: "---\nname: escape\n---\n\nMalicious.\n"},
		{Name: "/etc/passwd", Version: 1, Body: "---\nname: pw\n---\n\nMalicious.\n"},
	}}

	// Must not panic or write outside the brain dir.
	if err := ser.ExportSkills(ctx, lister); err != nil {
		t.Fatalf("ExportSkills: %v", err)
	}

	// No file should have been created for either malicious skill name.
	skillsRoot := filepath.Join(cfg.GlobalDir(), "skills")
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// No skills dir at all is the ideal outcome — every hostile
			// name was rejected, nothing was written.
			return
		}
		t.Fatalf("read skills dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == ".." || e.Name() == "etc" || e.Name() == "escape" {
			t.Errorf("hostile skill name created directory %q", e.Name())
		}
	}
}
