package brain

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/don-works/mcplexer/internal/store"
)

// skillsSubdir is the global folder holding exported skill SKILL.md files.
const skillsSubdir = "skills"

// SkillLister is the narrow store slice the skill exporter needs. The
// composite store.Store satisfies it; kept narrow so the export is testable
// against a fake.
type SkillLister interface {
	ListSkillRegistryHeads(ctx context.Context, scope store.SkillScope, limit int) ([]store.SkillRegistryEntry, error)
}

// ExportSkills writes each registry skill version's native SKILL.md to
// <Dir>/global/skills/<name>/v<N>/SKILL.md. Skills are already
// Markdown+frontmatter (the Body field IS a complete SKILL.md), so export
// is a direct, guarded write of Body — no frontmatter re-synthesis. This is
// a one-way export (the registry remains canonical for skills, which have
// their own versioned publish flow); it exists so a human/agent browsing
// the brain repo sees the skill corpus alongside tasks + memories.
//
// Each write goes through the same hash-CAS + atomic + self-suppress gate
// so a concurrent human edit is never clobbered. Per-skill errors are
// logged, not fatal — one bad skill never aborts the export.
func (s *Serializer) ExportSkills(ctx context.Context, lister SkillLister) error {
	entries, err := lister.ListSkillRegistryHeads(ctx, store.SkillScope{IncludeAll: true}, 0)
	if err != nil {
		return fmt.Errorf("brain: list skills for export: %w", err)
	}
	for i := range entries {
		e := entries[i]
		if e.DeletedAt != nil || e.Body == "" {
			continue
		}
		if err := s.exportSkill(ctx, &e); err != nil {
			s.log.Warn("brain: export skill", "name", e.Name, "version", e.Version, "error", err)
		}
	}
	return nil
}

// exportSkill writes one skill version's SKILL.md.
func (s *Serializer) exportSkill(ctx context.Context, e *store.SkillRegistryEntry) error {
	name, err := safeSlug(e.Name)
	if err != nil {
		// log and skip instead of aborting the entire export
		s.log.Warn("brain: skill name rejected", "name", e.Name, "error", err)
		return nil
	}
	path := filepath.Join(s.cfg.GlobalDir(), skillsSubdir, name,
		"v"+strconv.Itoa(e.Version), "SKILL.md")
	// Skills are a ONE-WAY projection: the registry is canonical (versioned
	// publish flow), the SKILL.md is derived, and there is no write-back path.
	// The hash-CAS guardedWrite gate is for files-are-canonical tasks/memories;
	// applying it here was a bug — skills carry no index_files binding, so the
	// gate saw every prior export as a "concurrent edit" and self-conflicted on
	// EVERY daemon restart, writing a .conflict sidecar + brain_errors row per
	// skill (the bulk of the brain's phantom validation errors). idempotentExport
	// projects the registry body verbatim (no-op when unchanged, overwrite when
	// the registry moved) and heals the stale conflict rows.
	return s.idempotentExport(ctx, path, []byte(e.Body))
}
