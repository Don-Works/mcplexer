// seed.go — embedded inbuilt worker templates published on first boot.
// Mirrors the skillregistry/seed.go pattern: idempotent on content hash
// so re-seeding is a no-op, individual failures never abort the others.
package workertemplates

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
)

//go:embed seeds/*.json
var seedFS embed.FS

// testOnlySeeds names embedded seed files that exist purely for the
// integration harness (echo-LLM-backed, self-described "NOT for
// production"). They stay in the embed for packaging simplicity but are
// only PUBLISHED when MCPLEXER_TEST_SEEDS=1 — a production boot must not
// advertise a test template in every workspace's registry.
var testOnlySeeds = map[string]bool{
	"consolidator-echo.json": true,
}

// testSeedsEnabled reports whether the harness opt-in is set.
func testSeedsEnabled() bool {
	return os.Getenv("MCPLEXER_TEST_SEEDS") == "1"
}

// Seed publishes every embedded inbuilt worker template at version 1.
// Idempotent on content_hash, so re-runs short-circuit. Individual
// template failures are logged + skipped — one bad seed never blocks
// the rest.
//
// Author tagging:
//   - seeds/<name>.json → author = "system"
//   - seeds/<group>/<name>.json → author = "<group>"
//
// Templates are seeded GLOBAL (workspace_id nil) so every workspace can
// install from them. Workspace-pinned templates are user-created via
// the publish surface — never seeded.
func Seed(ctx context.Context, r *Registry) error {
	if r == nil {
		return nil
	}
	walkErr := fs.WalkDir(seedFS, "seeds", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".json") {
			return nil
		}
		if testOnlySeeds[path.Base(p)] && !testSeedsEnabled() {
			// Harness-only template; skipped unless MCPLEXER_TEST_SEEDS=1.
			return nil
		}
		body, err := seedFS.ReadFile(p)
		if err != nil {
			slog.Warn("worker template seed: read failed",
				"file", p, "error", err)
			return nil
		}
		// Validate the JSON shape via the existing Unmarshal — bad
		// templates are rejected with a warn, never abort the run.
		tmpl, err := Unmarshal(string(body))
		if err != nil {
			slog.Warn("worker template seed: parse failed",
				"file", p, "error", err)
			return nil
		}
		rel := strings.TrimPrefix(p, "seeds/")
		dir := path.Dir(rel)
		author := "system"
		if dir != "." && dir != "" {
			author = dir
		}
		canon, err := Marshal(tmpl)
		if err != nil {
			slog.Warn("worker template seed: canonicalise failed",
				"file", p, "error", err)
			return nil
		}
		_, pubErr := r.Publish(ctx, PublishOptions{
			Body:        string(canon),
			Author:      author,
			Description: tmpl.Description,
		})
		if pubErr != nil {
			slog.Warn("worker template seed: publish failed",
				"file", p, "error", pubErr)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("worker template seed: walk: %w", walkErr)
	}
	return nil
}

// guard against goimports stripping when the only usage is via the
// embed FS — the JSON encoder is also referenced indirectly by Marshal.
var _ = json.Marshal
