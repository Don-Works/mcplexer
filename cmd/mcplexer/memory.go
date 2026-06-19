// memory.go — `mcplexer memory <subcommand>` CLI surface. Currently
// hosts the one-shot Claude Code auto-memory importer (`import-claude`).
// Future memory subcommands (export, prune, etc.) hang off the same
// dispatcher.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/memory/claudecli"
	"github.com/don-works/mcplexer/internal/memory/harnessimport"
)

// cmdMemory dispatches `mcplexer memory <subcommand>`.
func cmdMemory(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer memory <import|import-claude> [args...]")
	}
	switch args[0] {
	case "import":
		return runMemoryImport(args[1:])
	case "import-claude":
		return runMemoryImportClaude(args[1:])
	default:
		return fmt.Errorf("unknown memory subcommand: %s\nUsage: mcplexer memory <import|import-claude>", args[0])
	}
}

// runMemoryImportClaude implements `mcplexer memory import-claude`. It
// scans ~/.claude/projects/*/memory/*.md (or --base-dir override),
// prompts the user for confirmation, and writes each file into the
// mcplexer memory store. The import is idempotent — re-running is a
// no-op for unchanged files.
//
// Flags:
//
//	--yes              skip the y/N confirmation prompt
//	--dry-run          show what would be imported without writing
//	--base-dir <path>  override the default ~/.claude/projects scan root
//	--workspace <id>   scope the imports to one workspace (default: global)
func runMemoryImportClaude(args []string) error {
	fs := flag.NewFlagSet("memory import-claude", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the y/N confirmation prompt")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing")
	baseDir := fs.String("base-dir", "", "override ~/.claude/projects scan root")
	workspace := fs.String("workspace", "", "workspace ID; empty = global scope")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	preview, err := previewClaudeImport(ctx, *baseDir)
	if err != nil {
		return err
	}
	if preview == 0 {
		fmt.Println("No Claude Code memory files found — nothing to import.")
		return nil
	}
	resolvedBase := resolveClaudeBase(*baseDir)
	if !*yes {
		if ok, perr := confirmImport(preview, resolvedBase); !ok || perr != nil {
			if perr != nil {
				return perr
			}
			fmt.Println("Aborted.")
			return nil
		}
	}
	return runImport(ctx, *baseDir, *workspace, *dryRun)
}

// runMemoryImport implements `mcplexer memory import` — the unified
// importer that discovers and ingests memory files from all known
// harness locations (Claude Code, MiMoCode, etc.) in one pass.
func runMemoryImport(args []string) error {
	fs := flag.NewFlagSet("memory import", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the y/N confirmation prompt")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing")
	harness := fs.String("harness", "", "import from a specific harness only (claude-code, mimocode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	// Preview: discover files without opening DB
	var previewCount int
	if *harness != "" {
		// Single harness preview would need store access for idempotency
		// check — just let the user confirm and we'll do the full import
		previewCount = -1 // unknown count
	} else {
		// Count files across all harnesses
		for _, h := range []harnessimport.Harness{
			harnessimport.HarnessClaudeCode,
			harnessimport.HarnessMiMoCode,
		} {
			res, err := harnessimport.ImportHarness(ctx, nil, h, home)
			if err == nil && res != nil {
				previewCount += res.Imported + res.Skipped
			}
		}
	}
	if previewCount == 0 && *harness == "" {
		fmt.Println("No harness memory files found — nothing to import.")
		return nil
	}
	if !*yes {
		if previewCount > 0 {
			fmt.Printf("Found %d harness memory file(s) to import.\n", previewCount)
		} else {
			fmt.Printf("Importing from harness: %s\n", *harness)
		}
		fmt.Print("Import all into mcplexer? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, rerr := reader.ReadString('\n')
		if rerr != nil {
			return nil
		}
		resp := strings.ToLower(strings.TrimSpace(line))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var results []*harnessimport.ImportResult
	if *harness != "" {
		h := harnessimport.Harness(*harness)
		res, err := harnessimport.ImportHarness(ctx, db, h, home)
		if err != nil {
			return fmt.Errorf("import %s: %w", *harness, err)
		}
		if res != nil {
			results = append(results, res)
		}
	} else {
		results, err = harnessimport.ImportAll(ctx, db, home)
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
	}
	printUnifiedImportSummary(results, *dryRun)
	return nil
}

// previewClaudeImport runs a DryRun against a nil store via
// claudecli.DiscoverFiles only — we don't open the DB until the user
// confirms. Returns the file count to drive the prompt.
func previewClaudeImport(_ context.Context, baseDir string) (int, error) {
	files, err := claudecli.DiscoverFiles(baseDir)
	if err != nil {
		return 0, fmt.Errorf("discover: %w", err)
	}
	return len(files), nil
}

// resolveClaudeBase mirrors the package-internal default for display
// purposes (the user sees the actual path in the confirmation prompt).
func resolveClaudeBase(in string) string {
	if strings.TrimSpace(in) != "" {
		return in
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.claude/projects"
	}
	return home + "/.claude/projects"
}

// confirmImport prints the y/N prompt and reads one line from stdin.
// Empty input / "n" / "no" → false. "y" / "yes" → true.
func confirmImport(count int, base string) (bool, error) {
	fmt.Printf("Found %d Claude Code memory file(s) in %s.\n", count, base)
	fmt.Print("Import all into mcplexer? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF (e.g. piped empty stdin) is treated as a "no".
		return false, nil
	}
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "y" || resp == "yes", nil
}

// runImport opens the store and dispatches the importer. Splits the
// store-open + import calls out of runMemoryImportClaude to keep that
// function under the 50-line cap.
func runImport(ctx context.Context, baseDir, workspace string, dryRun bool) error {
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	opts := claudecli.ImportOptions{BaseDir: baseDir, DryRun: dryRun}
	if strings.TrimSpace(workspace) != "" {
		w := workspace
		opts.WorkspaceID = &w
	}
	res, err := claudecli.Import(ctx, db, opts)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	printImportSummary(res, dryRun)
	return nil
}

// printImportSummary writes the post-run report to stdout. We deliberately
// surface per-file errors so the user sees which files (if any) were
// rejected — but never block on them.
func printImportSummary(res *claudecli.ImportResult, dryRun bool) {
	prefix := "Imported"
	if dryRun {
		prefix = "Would import"
	}
	fmt.Printf("%s %d memory file(s); skipped %d (already present).\n",
		prefix, res.Imported, res.Skipped)
	if len(res.Errors) > 0 {
		fmt.Printf("\n%d error(s):\n", len(res.Errors))
		for _, e := range res.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}
}

// printUnifiedImportSummary writes the post-run report for the unified
// importer to stdout.
func printUnifiedImportSummary(results []*harnessimport.ImportResult, dryRun bool) {
	if len(results) == 0 {
		fmt.Println("No harness memory files found — nothing to import.")
		return
	}
	prefix := "Imported"
	if dryRun {
		prefix = "Would import"
	}
	totalImported, totalSkipped, totalErrors := 0, 0, 0
	for _, r := range results {
		totalImported += r.Imported
		totalSkipped += r.Skipped
		totalErrors += len(r.Errors)
		fmt.Printf("  [%s] %s %d; skipped %d\n", r.Harness, prefix, r.Imported, r.Skipped)
	}
	fmt.Printf("\nTotal: %s %d memory file(s); skipped %d.\n",
		prefix, totalImported, totalSkipped)
	if totalErrors > 0 {
		fmt.Printf("\n%d error(s) across all harnesses.\n", totalErrors)
	}
}
