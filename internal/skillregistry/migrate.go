// Package skillregistry — migrate.go discovers SKILL.md directories under
// a local source (typically ~/.claude/skills/), bundles them, publishes
// them into the registry, and archives the originals so the registry
// becomes the single source of truth.
//
// Why this lives next to the registry rather than internal/skills/:
// internal/skills/ targets the older `manifest.toml`-format installed-skill
// bundles. The registry stores agentskills.io-format SKILL.md documents.
// The migration ramp belongs with the consumer (the registry), not the
// legacy package.
//
// Surface split:
//   - discovery + classification (this file)
//   - per-skill import + publish (migrate_import.go)
//   - tarball packing + archive helpers (migrate_pack.go)
package skillregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// MigrationStatus describes the relationship between a local skill dir
// and what's already in the registry.
type MigrationStatus string

const (
	// StatusNew — registry has no skill with this name.
	StatusNew MigrationStatus = "new"
	// StatusDuplicate — registry has the same name + same content_hash;
	// nothing to do.
	StatusDuplicate MigrationStatus = "duplicate"
	// StatusVersionConflict — registry has the same name but a different
	// content_hash. Requires explicit overwrite to publish as a new version.
	StatusVersionConflict MigrationStatus = "version-conflict"
	// StatusUnparseable — the SKILL.md couldn't be parsed (bad frontmatter,
	// missing fields, oversize, etc.). Surfaces in the API so the dashboard
	// can show "fix this file" hints without crashing the discovery list.
	StatusUnparseable MigrationStatus = "unparseable"
	// StatusHarnessOwned — the directory is materialized and owned by
	// harness sync (e.g. using-mcplexer). Migration must never archive or
	// republish it; harness sync is its only writer.
	StatusHarnessOwned MigrationStatus = "harness-owned"
)

// harnessOwnedSkills are skill dirs materialized by internal/harnesssync.
// They already live in the registry — the on-disk copy is a render target,
// not a migration source.
var harnessOwnedSkills = map[string]bool{
	"using-mcplexer": true,
}

// LocalSkill is one candidate skill on disk.
type LocalSkill struct {
	// DirName is the directory's basename (typically equals Name).
	DirName string `json:"dir"`
	// Path is the absolute path of the skill directory.
	Path string `json:"path"`
	// Name is the canonical name parsed from frontmatter (empty when
	// Status=StatusUnparseable).
	Name string `json:"name"`
	// Description is the parsed description (empty when unparseable).
	Description string `json:"description"`
	// ContentHash is the parsed body's sha256 (empty when unparseable).
	ContentHash string `json:"content_hash"`
	// Status is the comparison result against the current registry.
	Status MigrationStatus `json:"status"`
	// RegistryVersion is the head version already in the registry, when
	// Status is StatusDuplicate or StatusVersionConflict.
	RegistryVersion int `json:"registry_version,omitempty"`
	// ParseError, when non-empty, carries the reason a directory was
	// flagged StatusUnparseable.
	ParseError string `json:"parse_error,omitempty"`
}

// DiscoverLocalSkills walks sourceDir for subdirectories containing a
// SKILL.md. Each is parsed and classified against the registry. Returns
// candidates sorted by name. Hidden dirs (leading '.') and the archive
// directory are skipped.
func (r *Registry) DiscoverLocalSkills(ctx context.Context, sourceDir string) ([]LocalSkill, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	if sourceDir == "" {
		return nil, errors.New("source dir is required")
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("read source dir %s: %w", sourceDir, err)
	}
	out := make([]LocalSkill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || isHidden(e.Name()) {
			continue
		}
		dir := filepath.Join(sourceDir, e.Name())
		mdPath := filepath.Join(dir, "SKILL.md")
		if _, statErr := os.Stat(mdPath); statErr != nil {
			continue
		}
		out = append(out, classifyLocalSkill(ctx, r, e.Name(), dir, mdPath))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DirName < out[j].DirName
	})
	return out, nil
}

// isHidden returns true for dotted-directory names like ".migrated".
func isHidden(name string) bool {
	return len(name) > 0 && name[0] == '.'
}

// classifyLocalSkill reads + parses SKILL.md and compares against the
// registry. Errors don't propagate — the LocalSkill row carries them so
// the discovery list stays useful even when one entry is malformed.
func classifyLocalSkill(ctx context.Context, r *Registry, dirName, dir, mdPath string) LocalSkill {
	row := LocalSkill{DirName: dirName, Path: dir}
	if harnessOwnedSkills[dirName] {
		row.Name = dirName
		row.Status = StatusHarnessOwned
		return row
	}
	body, err := os.ReadFile(mdPath) //nolint:gosec
	if err != nil {
		row.Status = StatusUnparseable
		row.ParseError = err.Error()
		return row
	}
	parsed, err := Parse(string(body), "")
	if err != nil {
		row.Status = StatusUnparseable
		row.ParseError = err.Error()
		return row
	}
	row.Name = parsed.Name
	row.Description = parsed.Description
	row.ContentHash = parsed.ContentHash
	head, err := r.store.GetSkillRegistryHead(ctx, AdminScope(), parsed.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		row.Status = StatusNew
	case err != nil:
		// Stay conservative: treat fetch errors like StatusNew so the
		// operator can retry without losing the candidate. The parse
		// error field carries the diagnostic.
		row.Status = StatusNew
		row.ParseError = err.Error()
	case head.ContentHash == parsed.ContentHash:
		row.Status = StatusDuplicate
		row.RegistryVersion = head.Version
	default:
		row.Status = StatusVersionConflict
		row.RegistryVersion = head.Version
	}
	return row
}

// DefaultArchiveDir returns ~/.claude/skills/.migrated/<RFC3339>/ for use
// as a default when no --archive-to is supplied. Errors fall back to a
// CWD-relative path so callers still get a sensible value.
func DefaultArchiveDir(now time.Time) string {
	stamp := now.UTC().Format("20060102T150405Z")
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".migrated", stamp)
	}
	return filepath.Join(home, ".claude", "skills", ".migrated", stamp)
}
