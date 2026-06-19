package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BrainSchemaVersion is the on-disk brain repo format version, recorded
// in brain.json. Bumped when the repo layout or entity formats change in
// a way the indexer must migrate.
const BrainSchemaVersion = 1

// GeneratorVersion identifies the code that wrote the repo. Surfaced in
// brain.json for diagnostics; not load-bearing in M0.
const GeneratorVersion = "m0"

// File contents for the scaffold. Kept as package-level constants so
// tests can assert on them directly.
const (
	// gitignoreContent keeps the derived index DB + caches OUT of git —
	// only the source text (Markdown/YAML) is ever tracked (SPEC §7).
	gitignoreContent = `# MCPlexer Brain — derived index DB is never committed.
brain-index.db
brain-index.db-wal
brain-index.db-shm
*.sqlite-wal
*.sqlite-shm
.cache/
.attachments/
`

	// gitattributesContent normalises line endings + sets merge=union on
	// append-only logs so concurrent appends both survive (SPEC §7).
	gitattributesContent = `*.md text eol=lf
*.yaml text eol=lf
*.yml text eol=lf
*.jsonl text merge=union
*.db binary
`

	// readmeContent is the human-facing orientation note.
	readmeContent = `# MCPlexer Brain

This is your MCPlexer Brain — a git-backed, Markdown-canonical state
repository. Edit the ` + "`.md`" + ` files; the gateway indexes them into
SQLite for fast querying.

- ` + "`workspaces/<slug>/`" + ` — one folder per workspace (tasks, memory, config).
- ` + "`global/`" + ` — cross-workspace skills, worker templates, secrets, config.
- ` + "`clients/<slug>/`" + ` — client/org-level state spanning child workspaces.

The derived index database is gitignored and rebuildable at any time.
`
)

// brainManifest is the structure serialized to brain.json.
type brainManifest struct {
	SchemaVersion    int    `json:"schema_version"`
	GeneratorVersion string `json:"generator_version"`
}

// scaffoldFile pairs a relative path with its content for idempotent
// writing.
type scaffoldFile struct {
	rel     string
	content []byte
}

// ScaffoldRepo creates the brain repo skeleton at dir, writing
// .gitignore, .gitattributes, brain.json and README.md plus the top-level
// directory structure. It is idempotent: existing files are never
// clobbered (re-stat first), so re-running on a live repo is a no-op for
// any file the user (or a prior run) has already created.
func ScaffoldRepo(dir string) error {
	if dir == "" {
		return fmt.Errorf("brain: ScaffoldRepo: empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("brain: create brain dir: %w", err)
	}

	for _, sub := range []string{
		"global/config",
		"global/secrets",
		"global/skills",
		"global/worker-templates",
		"workspaces",
		"clients",
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return fmt.Errorf("brain: create %s: %w", sub, err)
		}
	}

	manifest, err := json.MarshalIndent(brainManifest{
		SchemaVersion:    BrainSchemaVersion,
		GeneratorVersion: GeneratorVersion,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("brain: marshal brain.json: %w", err)
	}
	manifest = append(manifest, '\n')

	files := []scaffoldFile{
		{".gitignore", []byte(gitignoreContent)},
		{".gitattributes", []byte(gitattributesContent)},
		{"brain.json", manifest},
		{"README.md", []byte(readmeContent)},
	}
	for _, f := range files {
		if err := writeIfAbsent(filepath.Join(dir, f.rel), f.content); err != nil {
			return err
		}
	}
	return nil
}

// writeIfAbsent writes content to path only when path does not already
// exist. An existing file (regardless of content) is left untouched —
// this is what makes ScaffoldRepo idempotent and non-destructive to
// hand-edited files.
func writeIfAbsent(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists — never clobber
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("brain: stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("brain: write %s: %w", path, err)
	}
	return nil
}
