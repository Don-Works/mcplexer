// Package claudecli implements the one-shot "Claude Code auto-memory"
// importer. Claude Code writes per-project memory snippets to
// `~/.claude/projects/<flattened-repo>/memory/*.md`. This package reads
// every such file, parses the YAML frontmatter, and inserts each entry
// as a memory record so the user gets an instant warm start on the
// mcplexer memory subsystem.
//
// # Read-only contract
//
// The importer NEVER writes back to ~/.claude/. Source files are
// opened with os.Open, parsed, and forgotten. If a user re-runs the
// importer, the deterministic ID derived from sha256(path + content)
// makes the operation idempotent — unchanged files are skipped via the
// memories PK uniqueness, edited files insert a fresh row.
//
// # Frontmatter shape
//
//	---
//	name: workers-overnight-complete
//	description: "Workers M0-M3 overnight build complete..."
//	metadata:
//	  node_type: memory
//	  type: project          # project | reference | feedback | user
//	  originSessionId: <uuid>
//	---
//
//	body markdown here…
//
// Unknown / missing fields degrade gracefully: filename → name, no
// frontmatter → whole file as content, no metadata → no derived tags.
package claudecli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ImportOptions controls the import run.
type ImportOptions struct {
	// BaseDir is the directory containing the per-project memory dirs.
	// Defaults to $HOME/.claude/projects when empty.
	BaseDir string

	// WorkspaceID, when non-nil, scopes every imported memory to that
	// workspace. nil = global (visible across all workspaces).
	WorkspaceID *string

	// DryRun reports what would be imported without writing to the store.
	DryRun bool
}

// ImportedFile is one row in the result manifest.
type ImportedFile struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	MemoryID string `json:"memory_id"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// ImportResult is the aggregate of an import run.
type ImportResult struct {
	Imported int            `json:"imported"`
	Skipped  int            `json:"skipped"`
	Errors   []string       `json:"errors,omitempty"`
	Files    []ImportedFile `json:"files,omitempty"`
}

// memoryWriter is the slim contract Import needs from the store. We
// take only the calls we actually need so tests can stub a fake.
type memoryWriter interface {
	WriteMemory(ctx context.Context, e *store.MemoryEntry) error
	GetMemory(ctx context.Context, id string) (*store.MemoryEntry, error)
}

// Import scans BaseDir for `<project>/memory/*.md` files (excluding
// MEMORY.md), parses each, and writes the body into the memory store.
// Errors on individual files are collected but never abort the run.
func Import(
	ctx context.Context, s memoryWriter, opts ImportOptions,
) (*ImportResult, error) {
	if s == nil {
		return nil, errors.New("claudecli: store is nil")
	}
	base, err := resolveBaseDir(opts.BaseDir)
	if err != nil {
		return nil, err
	}
	files, err := DiscoverFiles(base)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{Files: make([]ImportedFile, 0, len(files))}
	for _, f := range files {
		if err := importOne(ctx, s, f, opts, res); err != nil {
			res.Errors = append(res.Errors,
				fmt.Sprintf("%s: %v", f, err))
		}
	}
	return res, nil
}

// resolveBaseDir defaults BaseDir to ~/.claude/projects when blank.
func resolveBaseDir(in string) (string, error) {
	if strings.TrimSpace(in) != "" {
		return in, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudecli: user home: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// DiscoverFiles walks `<base>/*/memory/*.md`, returning absolute paths
// in deterministic order. Missing base directories return an empty
// slice + nil error so the "user has no claude-cli history" case is
// frictionless. MEMORY.md (the index file) is skipped. Exported so the
// CLI can preview the file count before opening the DB / prompting.
// An empty base falls back to ~/.claude/projects.
func DiscoverFiles(base string) ([]string, error) {
	if strings.TrimSpace(base) == "" {
		resolved, err := resolveBaseDir("")
		if err != nil {
			return nil, err
		}
		base = resolved
	}
	info, err := os.Stat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claudecli: stat base: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("claudecli: %s is not a directory", base)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("claudecli: read base: %w", err)
	}
	out := make([]string, 0, 32)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		memDir := filepath.Join(base, e.Name(), "memory")
		matches, err := filepath.Glob(filepath.Join(memDir, "*.md"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if strings.EqualFold(filepath.Base(m), "MEMORY.md") {
				continue
			}
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

// importOne handles one file: read, parse, derive write options, and
// either record an idempotent skip or call WriteMemory.
func importOne(
	ctx context.Context, s memoryWriter, path string,
	opts ImportOptions, res *ImportResult,
) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	parsed, body := parseFrontmatter(raw)
	if strings.TrimSpace(body) == "" {
		return errors.New("empty body")
	}
	name := deriveName(parsed, path)
	id := deterministicID(path, raw)
	if existing, err := s.GetMemory(ctx, id); err == nil && existing != nil {
		res.Skipped++
		res.Files = append(res.Files, ImportedFile{
			Path: path, Name: name, MemoryID: id, Skipped: true,
		})
		return nil
	}
	if opts.DryRun {
		res.Imported++
		res.Files = append(res.Files, ImportedFile{
			Path: path, Name: name, MemoryID: id,
		})
		return nil
	}
	entry := buildEntry(id, name, body, path, parsed, opts.WorkspaceID)
	if err := s.WriteMemory(ctx, entry); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	res.Imported++
	res.Files = append(res.Files, ImportedFile{
		Path: path, Name: name, MemoryID: id,
	})
	return nil
}

// deriveName prefers frontmatter.name; falls back to the filename minus
// the .md suffix.
func deriveName(p parsedFrontmatter, path string) string {
	if strings.TrimSpace(p.Name) != "" {
		return strings.TrimSpace(p.Name)
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// deterministicID hashes (absolute path + raw file content) to give a
// stable ID. Re-importing the same file produces the same store ID, so
// the WriteMemory unique-PK collision is the idempotency gate.
func deterministicID(path string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(content)
	sum := h.Sum(nil)
	return "ccli-" + hex.EncodeToString(sum[:16])
}

// buildEntry assembles the MemoryEntry. Tags + metadata are derived
// from frontmatter; SourceKind is always MemorySourceImported so the
// dashboard can filter "imported from claude-cli" cleanly.
func buildEntry(
	id, name, body, path string,
	p parsedFrontmatter, workspaceID *string,
) *store.MemoryEntry {
	tags := deriveTags(p)
	tagsJSON := mustJSONArray(tags)
	meta := map[string]any{
		"imported_from":          path,
		"claude_cli_origin_repo": unflattenRepoPath(path),
	}
	if t := strings.TrimSpace(p.Metadata.Type); t != "" {
		meta["claude_cli_type"] = t
	}
	if d := strings.TrimSpace(p.Description); d != "" {
		meta["claude_cli_description"] = d
	}
	metaJSON := mustJSONObject(meta)
	return &store.MemoryEntry{
		ID:              id,
		Name:            name,
		Kind:            store.MemoryKindNote,
		Content:         body,
		TagsJSON:        tagsJSON,
		MetadataJSON:    metaJSON,
		WorkspaceID:     workspaceID,
		SourceKind:      store.MemorySourceImported,
		SourceSessionID: strings.TrimSpace(p.Metadata.OriginSessionID),
	}
}

// deriveTags maps the frontmatter metadata.type field into a single
// canonical mcplexer tag namespace (claude-cli-<type>). When the type
// is empty we drop a generic claude-cli tag so all imports remain
// discoverable as a group.
func deriveTags(p parsedFrontmatter) []string {
	t := strings.ToLower(strings.TrimSpace(p.Metadata.Type))
	if t == "" {
		return []string{"claude-cli"}
	}
	return []string{"claude-cli", "claude-cli-" + t}
}

// unflattenRepoPath reverses Claude Code's `/` → `-` flattening so we
// can stamp the origin repo on the metadata. Best-effort: repos with
// embedded hyphens (cmux-browser) won't round-trip cleanly. We keep
// the result anyway — it's a metadata hint, not a key.
func unflattenRepoPath(path string) string {
	dir := filepath.Dir(filepath.Dir(path)) // strip /memory/<file>.md
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, "-") {
		return base
	}
	return strings.ReplaceAll(base, "-", "/")
}
