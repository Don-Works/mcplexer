package skillregistry

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
)

//go:embed seeds/*.md seeds/*/*.md
var seedFS embed.FS

// SeedBody returns the raw markdown for an embedded top-level seed skill
// (filename without .md). Used by harness-sync to materialize the
// using-mcplexer SKILL.md sidecar without duplicating the seed file.
func SeedBody(name string) (string, error) {
	body, err := seedFS.ReadFile("seeds/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("seed body %q: %w", name, err)
	}
	return string(body), nil
}

// Seed publishes the embedded SKILL.md files as version 1 iff the
// registry has no rows yet. Idempotent on content_hash, so a re-run
// after a fresh seed is a no-op.
//
// Author tagging:
//   - seeds at seeds/<name>.md → author = "system"
//   - seeds at seeds/<group>/<name>.md → author = "<group>"
//
// Failure of one seed never aborts the others — first-run loss of one
// seed file is fine; the registry can still serve the rest.
func Seed(ctx context.Context, r *Registry) error {
	if r == nil {
		return nil
	}

	// If any skill row exists, the registry is already seeded.
	// Worker templates live in their own table since migration 057,
	// so the presence of templates doesn't block skill seeding.
	heads, err := r.ListHeads(ctx, AdminScope(), 0)
	if err != nil {
		return fmt.Errorf("seed: list heads: %w", err)
	}
	if len(heads) > 0 {
		return nil
	}

	walkErr := fs.WalkDir(seedFS, "seeds", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".md") {
			return nil
		}
		body, err := seedFS.ReadFile(p)
		if err != nil {
			slog.Warn("skill seed: read failed", "file", p, "error", err)
			return nil
		}
		// Author = parent dir relative to seeds/, or "system" at top level.
		rel := strings.TrimPrefix(p, "seeds/")
		dir := path.Dir(rel)
		author := "system"
		if dir != "." && dir != "" {
			author = dir
		}
		// Filename (no .md) is the canonical name; Parse re-validates
		// and pulls the real name from frontmatter.
		_, pubErr := r.Publish(ctx, PublishOptions{
			Name:   strings.TrimSuffix(path.Base(p), ".md"),
			Body:   string(body),
			Author: author,
		})
		if pubErr != nil {
			slog.Warn("skill seed: publish failed",
				"file", p, "error", pubErr)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("seed: walk: %w", walkErr)
	}
	return nil
}
