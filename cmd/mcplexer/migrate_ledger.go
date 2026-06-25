package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// cmdMigrateLedger implements `mcplexer migrate-ledger` — a narrow operator
// repair for stale applied_migrations checksums after historical migration
// files were intentionally rewritten before the ledger contract existed.
func cmdMigrateLedger(args []string) error {
	fs := flag.NewFlagSet("migrate-ledger", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print stale ledger rows but change nothing")
	yes := fs.Bool("yes", false, "non-interactive; apply the rehash without prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: mcplexer migrate-ledger [--dry-run] [--yes]")
	}
	if *dryRun && *yes {
		return fmt.Errorf("--dry-run and --yes cannot be used together")
	}

	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if *dryRun {
		report, err := sqlite.RehashMigrationLedger(ctx, db.Raw(), sqlite.LedgerRehashOptions{
			DryRun: true,
		})
		if err != nil {
			return err
		}
		printLedgerRehashReport(report, true)
		return nil
	}

	preview, err := sqlite.RehashMigrationLedger(ctx, db.Raw(), sqlite.LedgerRehashOptions{
		DryRun: true,
	})
	if err != nil {
		return err
	}
	if len(preview.Rows) == 0 {
		printLedgerRehashReport(preview, true)
		return nil
	}
	printLedgerRehashReport(preview, true)
	if !*yes {
		ok, err := confirm(fmt.Sprintf("Rehash %d migration ledger row(s)? [y/N]: ", len(preview.Rows)))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("migration ledger rehash cancelled by user")
		}
	}

	report, err := sqlite.RehashMigrationLedger(ctx, db.Raw(), sqlite.LedgerRehashOptions{})
	if err != nil {
		return err
	}
	printLedgerRehashReport(report, false)
	if hasLedgerErrors(report.VerifyIssues) {
		return fmt.Errorf("migration ledger still has verification errors")
	}
	return nil
}

func printLedgerRehashReport(report sqlite.LedgerRehashReport, dryRun bool) {
	if dryRun {
		fmt.Fprintln(os.Stderr, "Migration ledger rehash dry-run.")
	} else {
		fmt.Fprintln(os.Stderr, "Migration ledger rehash applied.")
	}
	if len(report.Rows) == 0 {
		fmt.Fprintln(os.Stderr, "No stale migration ledger rows found.")
	} else {
		for _, row := range report.Rows {
			status := "pending"
			if row.Updated {
				status = "updated"
			}
			fmt.Fprintf(os.Stderr, "  v%03d  %-42s  %s -> %s  %s\n",
				row.Version,
				truncate(row.CurrentFilename, 42),
				shortChecksum(row.RecordedChecksum),
				shortChecksum(row.CurrentChecksum),
				status,
			)
		}
	}

	if len(report.VerifyIssues) == 0 {
		fmt.Fprintln(os.Stderr, "Ledger verification: clean.")
		return
	}
	fmt.Fprintf(os.Stderr, "Ledger verification: %d issue(s).\n", len(report.VerifyIssues))
	for _, issue := range report.VerifyIssues {
		fmt.Fprintf(os.Stderr, "  %s\n", issue.String())
	}
}

func hasLedgerErrors(issues []sqlite.LedgerIssue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func shortChecksum(checksum string) string {
	if len(checksum) <= 12 {
		return checksum
	}
	return checksum[:12]
}
