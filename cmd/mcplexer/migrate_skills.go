package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// cmdMigrateSkills implements `mcplexer migrate-skills` — walks a local
// directory of agentskills.io SKILL.md folders (default
// ~/.claude/skills/) and publishes them into the on-disk registry so the
// registry becomes the single source of truth.
//
// Flags:
//
//	--source       directory to walk (default ~/.claude/skills)
//	--archive-to   destination for moved sources (default
//	               ~/.claude/skills/.migrated/<RFC3339-stamp>/)
//	--dry-run      print actions, change nothing
//	--yes          non-interactive; auto-confirm version overwrites
//	--author       author tag attached to published rows (default "migrate-cli")
func cmdMigrateSkills(args []string) error {
	fs := flag.NewFlagSet("migrate-skills", flag.ContinueOnError)
	source := fs.String("source", "", "directory to walk (default ~/.claude/skills)")
	archiveTo := fs.String("archive-to", "", "destination root for moved sources (default ~/.claude/skills/.migrated/<RFC3339>)")
	dryRun := fs.Bool("dry-run", false, "print actions but change nothing")
	yes := fs.Bool("yes", false, "non-interactive; auto-confirm version overwrites")
	author := fs.String("author", "migrate-cli", "author tag attached to published rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: mcplexer migrate-skills [--source DIR] [--archive-to DIR] [--dry-run] [--yes] [--author NAME]")
	}

	src := resolveSource(*source)
	archive := resolveArchive(*archiveTo)
	if *dryRun {
		fmt.Fprintf(os.Stderr, "DRY RUN — no changes will be written.\n")
	}
	fmt.Fprintf(os.Stderr, "Source:      %s\n", src)
	fmt.Fprintf(os.Stderr, "Archive to:  %s\n", archive)
	fmt.Fprintln(os.Stderr)

	ctx := context.Background()
	reg, cleanup, err := openMigrationRegistry(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	rows, err := reg.DiscoverLocalSkills(ctx, src)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No skill directories found.")
		return nil
	}

	results := make([]skillregistry.MigrationResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, processOne(ctx, reg, row, archive, *dryRun, *yes, *author))
	}
	printSummary(results)
	return nil
}

// resolveSource returns the absolute source dir to walk. Honours --source,
// expands a leading ~, and defaults to ~/.claude/skills.
func resolveSource(flagVal string) string {
	if strings.TrimSpace(flagVal) != "" {
		return skillregistry.ExpandUserHome(flagVal)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/skills"
	}
	return filepath.Join(home, ".claude", "skills")
}

// resolveArchive returns the dest root for moved sources. Honours
// --archive-to, expands ~, and defaults to .migrated/<RFC3339-stamp>
// alongside the source so audit-trails sit next to the originals.
func resolveArchive(flagVal string) string {
	if strings.TrimSpace(flagVal) != "" {
		return skillregistry.ExpandUserHome(flagVal)
	}
	return skillregistry.DefaultArchiveDir(time.Now())
}

// openMigrationRegistry constructs a registry backed by the on-disk
// SQLite store. Returns a cleanup that closes the DB.
func openMigrationRegistry(ctx context.Context) (*skillregistry.Registry, func(), error) {
	db, err := openStore(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = db.Close() }
	return skillregistry.New(db), cleanup, nil
}

// processOne handles classification + interactive confirmation + import
// for a single local skill row.
func processOne(
	ctx context.Context,
	reg *skillregistry.Registry,
	row skillregistry.LocalSkill,
	archive string,
	dryRun, yes bool,
	author string,
) skillregistry.MigrationResult {
	switch row.Status {
	case skillregistry.StatusUnparseable:
		printRow(row.DirName, "unparseable", row.ParseError)
		return skillregistry.MigrationResult{
			Name: row.Name, DirName: row.DirName, Path: row.Path,
			Action: skillregistry.ActionFailed, Error: row.ParseError,
			DryRun: dryRun,
		}
	case skillregistry.StatusHarnessOwned:
		printRow(row.DirName, "harness-owned", "managed by harness sync; left in place")
		return skillregistry.MigrationResult{
			Name: row.Name, DirName: row.DirName, Path: row.Path,
			Action: skillregistry.ActionSkipped, DryRun: dryRun,
		}
	case skillregistry.StatusDuplicate:
		printRow(row.DirName, "duplicate", fmt.Sprintf("registry v%d matches", row.RegistryVersion))
	case skillregistry.StatusNew:
		printRow(row.DirName, "new", "will publish")
	case skillregistry.StatusVersionConflict:
		printRow(row.DirName, "conflict", fmt.Sprintf("registry v%d has different hash", row.RegistryVersion))
	}

	overwrite := false
	if row.Status == skillregistry.StatusVersionConflict {
		if yes || dryRun {
			overwrite = true
		} else {
			ok, err := confirm(fmt.Sprintf("  Overwrite %s as a new version? [y/N]: ", row.Name))
			if err != nil || !ok {
				return skillregistry.MigrationResult{
					Name: row.Name, DirName: row.DirName, Path: row.Path,
					Action: skillregistry.ActionFailed,
					Error:  "skipped by user",
					DryRun: dryRun,
				}
			}
			overwrite = true
		}
	}

	return reg.ImportLocalSkill(ctx, skillregistry.MigrateOptions{
		Path:       row.Path,
		ArchiveDir: archive,
		Overwrite:  overwrite,
		DryRun:     dryRun,
		Author:     author,
	})
}

// printRow renders a single line during the discovery walk. Aligns
// columns so a 20-skill batch reads cleanly in a terminal.
func printRow(name, status, detail string) {
	fmt.Fprintf(os.Stderr, "  %-32s  %-15s  %s\n", truncate(name, 32), status, detail)
}

// printSummary writes the IMPORTED/SKIPPED/UPDATED/FAILED tallies plus
// the per-row outcome, identical-looking in dry-run and live modes.
func printSummary(results []skillregistry.MigrationResult) {
	var imp, skp, upd, fail int
	for _, r := range results {
		switch r.Action {
		case skillregistry.ActionImported:
			imp++
		case skillregistry.ActionSkipped:
			skp++
		case skillregistry.ActionUpdated:
			upd++
		case skillregistry.ActionFailed:
			fail++
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].DirName < results[j].DirName })

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Per-skill results:")
	for _, r := range results {
		extra := ""
		switch {
		case r.Action == skillregistry.ActionFailed && r.Error != "":
			extra = " — " + r.Error
		case r.Version > 0 && r.ArchivedTo != "":
			extra = fmt.Sprintf(" v%d → %s", r.Version, r.ArchivedTo)
		case r.Version > 0:
			extra = fmt.Sprintf(" v%d", r.Version)
		}
		fmt.Fprintf(os.Stderr, "  %-32s  %s%s\n", truncate(r.DirName, 32), r.Action, extra)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "IMPORTED: %d | SKIPPED: %d (already in registry) | UPDATED: %d | FAILED: %d\n",
		imp, skp, upd, fail)
}
