// Package harnessimport provides a unified importer that ingests
// harness-native memory files (Claude Code, MiMoCode, generic
// markdown) into the mcplexer memory store. This is the bridge that
// makes mcplexer the single source of truth: every harness's existing
// memory is imported once, then the harness is redirected to use
// mcplexer memory going forward.
//
// # Harness formats
//
//   - Claude Code: ~/.claude/projects/<repo>/memory/*.md with YAML
//     frontmatter. Delegates to internal/memory/claudecli.
//   - MiMoCode: ~/.local/share/mimocode/memory/projects/<uuid>/*.md
//     and global/MEMORY.md. Plain markdown with section headers.
//   - Generic: any .md file with optional YAML frontmatter; body is
//     the full content.
//
// # Idempotency
//
// Every import uses deterministic IDs derived from (path + content).
// Re-importing unchanged files is a no-op; changed files insert a
// fresh row (the old one stays as history).
package harnessimport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Harness identifies a known harness type.
type Harness string

const (
	HarnessClaudeCode Harness = "claude-code"
	HarnessMiMoCode   Harness = "mimocode"
	HarnessGeneric    Harness = "generic"
)

// MemoryWriter is the slim store contract the importer needs.
type MemoryWriter interface {
	WriteMemory(ctx context.Context, e *store.MemoryEntry) error
	GetMemory(ctx context.Context, id string) (*store.MemoryEntry, error)
}

// ImportResult is the aggregate of an import run for one harness.
type ImportResult struct {
	Harness  Harness        `json:"harness"`
	Imported int            `json:"imported"`
	Skipped  int            `json:"skipped"`
	Errors   []string       `json:"errors,omitempty"`
	Files    []ImportedFile `json:"files,omitempty"`
}

// ImportedFile is one row in the result manifest.
type ImportedFile struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	MemoryID string `json:"memory_id"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// ImportAll discovers and imports memory files from all known harness
// locations. Returns one result per harness that had files. Harnesses
// with no files are omitted from the result.
func ImportAll(
	ctx context.Context, s MemoryWriter, homeDir string,
) ([]*ImportResult, error) {
	var results []*ImportResult
	for _, h := range knownHarnesses() {
		res, err := importHarness(ctx, s, h, homeDir)
		if err != nil {
			return results, fmt.Errorf("import %s: %w", h, err)
		}
		if res != nil && (res.Imported+res.Skipped) > 0 {
			results = append(results, res)
		}
	}
	return results, nil
}

// ImportHarness imports memory files from a specific harness.
func ImportHarness(
	ctx context.Context, s MemoryWriter, h Harness, homeDir string,
) (*ImportResult, error) {
	return importHarness(ctx, s, h, homeDir)
}

func knownHarnesses() []Harness {
	return []Harness{HarnessClaudeCode, HarnessMiMoCode}
}

func importHarness(
	ctx context.Context, s MemoryWriter, h Harness, homeDir string,
) (*ImportResult, error) {
	switch h {
	case HarnessClaudeCode:
		return importClaudeCode(ctx, s, homeDir)
	case HarnessMiMoCode:
		return importMiMoCode(ctx, s, homeDir)
	default:
		return nil, nil
	}
}

// importClaudeCode delegates to the existing claudecli package.
func importClaudeCode(
	ctx context.Context, s MemoryWriter, homeDir string,
) (*ImportResult, error) {
	// Import the existing claudecli package inline to avoid circular
	// imports — we use the same store interface.
	baseDir := filepath.Join(homeDir, ".claude", "projects")
	files, err := discoverClaudeFiles(baseDir)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	res := &ImportResult{Harness: HarnessClaudeCode, Files: make([]ImportedFile, 0, len(files))}
	for _, f := range files {
		if err := importClaudeFile(ctx, s, f, res); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f, err))
		}
	}
	return res, nil
}

// importMiMoCode imports MiMoCode's project and global memory files.
// MiMoCode stores memory at:
//
//	~/.local/share/mimocode/memory/projects/<uuid>/*.md
//	~/.local/share/mimocode/memory/global/MEMORY.md
//
// Files are plain markdown with section headers (no frontmatter).
// Session checkpoint files are excluded — they're transient.
func importMiMoCode(
	ctx context.Context, s MemoryWriter, homeDir string,
) (*ImportResult, error) {
	baseDir := filepath.Join(homeDir, ".local", "share", "mimocode", "memory")
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return nil, nil
	}
	files, err := discoverMiMoCodeFiles(baseDir)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	res := &ImportResult{Harness: HarnessMiMoCode, Files: make([]ImportedFile, 0, len(files))}
	for _, f := range files {
		if err := importMiMoCodeFile(ctx, s, f, baseDir, res); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f, err))
		}
	}
	return res, nil
}

// discoverMiMoCodeFiles finds all .md files in MiMoCode's memory
// directory, excluding session checkpoints and progress files.
func discoverMiMoCodeFiles(baseDir string) ([]string, error) {
	var out []string
	// Global memory
	globalFile := filepath.Join(baseDir, "global", "MEMORY.md")
	if _, err := os.Stat(globalFile); err == nil {
		out = append(out, globalFile)
	}
	// Project memory
	projectsDir := filepath.Join(baseDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read projects dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(projectsDir, e.Name(), "*.md"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			base := filepath.Base(m)
			// Skip non-memory files (checkpoint, progress, notes, tasks)
			if base == "checkpoint.md" || base == "progress.md" ||
				base == "notes.md" || base == "tasks.md" {
				continue
			}
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

// importMiMoCodeFile imports one MiMoCode memory file. The file is
// plain markdown with section headers. We split on ## headers to
// create one memory per major section, or import the whole file as
// one memory if there are no ## headers.
func importMiMoCodeFile(
	ctx context.Context, s MemoryWriter, path, baseDir string,
	res *ImportResult,
) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return nil
	}
	// Derive name from filename
	fileName := deriveNameFromPath(path)
	// Derive project ID from path if it's a project memory
	workspaceID := deriveWorkspaceID(path, baseDir)
	// Try splitting into sections
	sections := splitIntoSections(body)
	if len(sections) == 0 {
		return nil
	}
	// If there's only one section with a generic title, use the filename
	if len(sections) == 1 && sections[0].title == "MiMoCode Memory" {
		sections[0].title = fileName
	}
	for _, sec := range sections {
		id := deterministicID(path, sec.title, []byte(sec.content))
		if existing, err := s.GetMemory(ctx, id); err == nil && existing != nil {
			res.Skipped++
			res.Files = append(res.Files, ImportedFile{
				Path: path, Name: sec.title, MemoryID: id, Skipped: true,
			})
			continue
		}
		entry := &store.MemoryEntry{
			ID:       id,
			Name:     sec.title,
			Kind:     store.MemoryKindNote,
			Content:  sec.content,
			TagsJSON: mustJSONArray([]string{"harness-import", "mimocode"}),
			MetadataJSON: mustJSONObject(map[string]any{
				"imported_from": path,
				"harness":       string(HarnessMiMoCode),
			}),
			WorkspaceID: workspaceID,
			SourceKind:  store.MemorySourceImported,
		}
		if err := s.WriteMemory(ctx, entry); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		res.Imported++
		res.Files = append(res.Files, ImportedFile{
			Path: path, Name: sec.title, MemoryID: id,
		})
	}
	return nil
}

// section represents a parsed markdown section.
type section struct {
	title   string
	content string
}

// splitIntoSections splits markdown on ## headers. If no ## headers
// exist, returns the whole body as a single section with the filename
// as title.
func splitIntoSections(body string) []section {
	lines := strings.Split(body, "\n")
	var sections []section
	var current *section
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if current != nil && strings.TrimSpace(current.content) != "" {
				sections = append(sections, *current)
			}
			current = &section{
				title:   strings.TrimPrefix(line, "## "),
				content: "",
			}
		} else if current != nil {
			current.content += line + "\n"
		}
	}
	if current != nil && strings.TrimSpace(current.content) != "" {
		sections = append(sections, *current)
	}
	// If no sections found, treat the whole body as one
	if len(sections) == 0 {
		title := "MiMoCode Memory"
		// Try to use the first # header as title
		for _, line := range lines {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimPrefix(line, "# ")
				break
			}
		}
		sections = []section{{title: title, content: body}}
	}
	return sections
}

// deriveNameFromPath extracts a human-readable name from a file path.
func deriveNameFromPath(path string) string {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "MEMORY" {
		// Use parent dir name
		parent := filepath.Base(filepath.Dir(path))
		if parent != "global" && parent != "memory" {
			return parent + " Memory"
		}
		return "Global Memory"
	}
	return name
}

// deriveWorkspaceID extracts a workspace ID from the path if it's a
// project-scoped memory. Returns nil for global memories.
func deriveWorkspaceID(path, baseDir string) *string {
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	// Pattern: projects/<uuid>/file.md
	if len(parts) >= 3 && parts[0] == "projects" && parts[1] != "global" {
		return &parts[1]
	}
	return nil
}

// discoverClaudeFiles delegates to the claudecli package's file
// discovery. We re-implement here to avoid importing the full package
// (which includes the YAML parser we don't need for the unified path).
func discoverClaudeFiles(baseDir string) ([]string, error) {
	info, err := os.Stat(baseDir)
	if os.IsNotExist(err) || (err == nil && !info.IsDir()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		memDir := filepath.Join(baseDir, e.Name(), "memory")
		matches, _ := filepath.Glob(filepath.Join(memDir, "*.md"))
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

// importClaudeFile imports one Claude Code memory file. We parse the
// YAML frontmatter and write to the store.
func importClaudeFile(
	ctx context.Context, s MemoryWriter, path string,
	res *ImportResult,
) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	parsed, body := parseClaudeFrontmatter(raw)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	name := parsed.Name
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	id := deterministicID(path, "", raw)
	if existing, err := s.GetMemory(ctx, id); err == nil && existing != nil {
		res.Skipped++
		res.Files = append(res.Files, ImportedFile{
			Path: path, Name: name, MemoryID: id, Skipped: true,
		})
		return nil
	}
	tags := []string{"harness-import", "claude-code"}
	if parsed.Metadata.Type != "" {
		tags = append(tags, "claude-cli-"+parsed.Metadata.Type)
	}
	meta := map[string]any{
		"imported_from": path,
		"harness":       string(HarnessClaudeCode),
	}
	if parsed.Metadata.Type != "" {
		meta["claude_cli_type"] = parsed.Metadata.Type
	}
	entry := &store.MemoryEntry{
		ID:           id,
		Name:         name,
		Kind:         store.MemoryKindNote,
		Content:      body,
		TagsJSON:     mustJSONArray(tags),
		MetadataJSON: mustJSONObject(meta),
		SourceKind:   store.MemorySourceImported,
	}
	if err := s.WriteMemory(ctx, entry); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	res.Imported++
	res.Files = append(res.Files, ImportedFile{
		Path: path, Name: name, MemoryID: id,
	})
	return nil
}
