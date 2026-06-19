package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// cmdMigrateCommands drains flat slash-command files (~/.claude/commands)
// into the skill registry. Mirrors cmdMigrateSkills; the discovery and
// import semantics live in internal/skillregistry/migrate_commands.go.
func cmdMigrateCommands(args []string) error {
	fs := flag.NewFlagSet("migrate-commands", flag.ContinueOnError)
	source := fs.String("source", "", "directory to walk (default ~/.claude/commands)")
	archiveTo := fs.String("archive-to", "", "destination root for moved sources (default <source>.migrated/<stamp>)")
	dryRun := fs.Bool("dry-run", false, "print actions but change nothing")
	yes := fs.Bool("yes", false, "non-interactive; auto-confirm version overwrites")
	author := fs.String("author", "migrate-cli", "author tag attached to published rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: mcplexer migrate-commands [--source DIR] [--archive-to DIR] [--dry-run] [--yes] [--author NAME]")
	}

	src := resolveCommandsSource(*source)
	archive := resolveCommandsArchive(*archiveTo, src)
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

	rows, err := reg.DiscoverLocalCommands(ctx, src)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No command files found.")
		return nil
	}

	results := make([]skillregistry.MigrationResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, processOneCommand(ctx, reg, row, archive, *dryRun, *yes, *author))
	}
	printSummary(results)
	return nil
}

// processOneCommand classifies + imports a single command file, mirroring
// processOne for skill directories.
func processOneCommand(
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

	return reg.ImportLocalCommand(ctx, skillregistry.MigrateOptions{
		Path:       row.Path,
		ArchiveDir: archive,
		Overwrite:  overwrite,
		DryRun:     dryRun,
		Author:     author,
	})
}

// resolveCommandsSource honours --source, expands ~, defaults to
// ~/.claude/commands.
func resolveCommandsSource(flagVal string) string {
	if flagVal != "" {
		return skillregistry.ExpandUserHome(flagVal)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/commands"
	}
	return filepath.Join(home, ".claude", "commands")
}

// resolveCommandsArchive honours --archive-to, defaulting to a stamped
// sibling of the source. Keeping archives outside ~/.claude/commands
// prevents harness command loaders from rediscovering archived files.
func resolveCommandsArchive(flagVal, src string) string {
	if flagVal != "" {
		return skillregistry.ExpandUserHome(flagVal)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(src+".migrated", stamp)
}
