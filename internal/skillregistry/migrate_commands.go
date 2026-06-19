// Package skillregistry — migrate_commands.go drains flat slash-command
// files (typically ~/.claude/commands/*.md) into the registry, mirroring
// the SKILL.md-directory ramp in migrate.go.
//
// Command files are agentskills-shaped already (YAML frontmatter + body)
// but frequently omit `name:` — harnesses derive it from the filename.
// Discovery synthesizes a canonical SKILL.md (injecting the name when
// missing) so the registry row parses, then classification and import
// reuse the same status vocabulary and archive flow as skill dirs.
package skillregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// DiscoverLocalCommands walks sourceDir for flat *.md files. Each is
// synthesized into a canonical SKILL.md and classified against the
// registry. Hidden files and subdirectories are skipped; rows come back
// sorted by filename.
func (r *Registry) DiscoverLocalCommands(ctx context.Context, sourceDir string) ([]LocalSkill, error) {
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
		if e.IsDir() || isHidden(e.Name()) || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, classifyLocalCommand(ctx, r, e.Name(), filepath.Join(sourceDir, e.Name())))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DirName < out[j].DirName
	})
	return out, nil
}

// classifyLocalCommand synthesizes the canonical body and compares it
// against the registry. Mirrors classifyLocalSkill: errors land on the
// row, never propagate.
func classifyLocalCommand(ctx context.Context, r *Registry, fileName, path string) LocalSkill {
	row := LocalSkill{DirName: fileName, Path: path}
	stem := strings.TrimSuffix(fileName, ".md")
	if harnessOwnedSkills[stem] {
		row.Name = stem
		row.Status = StatusHarnessOwned
		return row
	}
	parsed, err := readAndParseCommand(path)
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

// ImportLocalCommand publishes the synthesized SKILL.md for one command
// file, then archives the original file. Inline-source publish — flat
// commands carry no assets, so no bundle is packed. Returns one
// MigrationResult, never an error, matching ImportLocalSkill.
func (r *Registry) ImportLocalCommand(ctx context.Context, opts MigrateOptions) MigrationResult {
	res := MigrationResult{
		Path:    opts.Path,
		DirName: filepath.Base(opts.Path),
		DryRun:  opts.DryRun,
	}
	if r == nil || r.store == nil {
		res.Action = ActionFailed
		res.Error = "skillregistry: not initialised"
		return res
	}
	parsed, err := readAndParseCommand(opts.Path)
	if err != nil {
		res.Action = ActionFailed
		res.Error = err.Error()
		return res
	}
	res.Name = parsed.Name
	if harnessOwnedSkills[parsed.Name] {
		res.Action = ActionFailed
		res.Error = fmt.Sprintf("%s is owned by harness sync; refusing to archive or republish it", parsed.Name)
		return res
	}

	head, headErr := r.store.GetSkillRegistryHead(ctx, AdminScope(), parsed.Name)
	if shortCircuit, done := handleHeadLookup(&res, head, headErr, parsed.ContentHash, opts); done {
		return shortCircuit
	}

	if opts.DryRun {
		if errors.Is(headErr, store.ErrNotFound) {
			res.Action = ActionImported
		} else {
			res.Action = ActionUpdated
			res.Version = head.Version
		}
		return res
	}

	return finalizePublish(ctx, r, &res, opts, parsed.Body, parsed.Name, nil, headErr)
}

// readAndParseCommand reads a flat command file and parses the
// synthesized canonical SKILL.md form.
func readAndParseCommand(path string) (*Parsed, error) {
	raw, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".md")
	body, err := commandToSkillMD(stem, string(raw))
	if err != nil {
		return nil, fmt.Errorf("synthesize %s: %w", path, err)
	}
	return Parse(body, "")
}

// commandToSkillMD turns a slash-command file into a canonical SKILL.md
// body. When the frontmatter already declares `name:` the content passes
// through verbatim (hash-stable with the on-disk file); otherwise the
// filename stem is injected as the name. The synthesis is deterministic so
// repeated discovery runs classify duplicates correctly.
func commandToSkillMD(stem, raw string) (string, error) {
	if !nameRE.MatchString(stem) || strings.Contains(stem, "--") {
		return "", fmt.Errorf("filename %q is not a valid skill name", stem)
	}
	frontmatter, _, err := splitFrontmatter(raw)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(frontmatter, "\n") {
		// Top-level key only — an indented "name:" is a nested field.
		if strings.HasPrefix(line, "name:") {
			return raw, nil
		}
	}
	s := strings.TrimLeft(raw, " \t\r\n")
	rest := strings.TrimPrefix(s, "---")
	return "---\nname: " + stem + rest, nil
}
