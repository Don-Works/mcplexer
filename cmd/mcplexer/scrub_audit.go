// scrub_audit.go — `mcplexer scrub-audit` CLI surface.
//
// Closes the data half of incident 01KSM6D2F24VA7P406VKRZ2GHJ. The code fix
// at ba44b65 added 7 missing secret-shape regexes to audit.Redact, but is
// forward-only: existing audit_records rows that captured plaintext before the
// fix are still in the DB. This subcommand rescans audit_records against the
// (now-complete) valueRedactPatterns set and lets the operator scrub the
// matching rows.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
)

type scrubOpts struct {
	apply bool
	limit int
}

func cmdScrubAudit(args []string) error {
	fs := flag.NewFlagSet("scrub-audit", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "actually delete matching rows (default: dry-run report only)")
	limit := fs.Int("limit", 0, "scan at most N rows (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runScrubAudit(context.Background(), scrubOpts{apply: *apply, limit: *limit})
}

type leakHit struct {
	id        string
	tool      string
	timestamp string
}

func runScrubAudit(ctx context.Context, opts scrubOpts) error {
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	hits, total, err := scanAuditLeaks(ctx, db.Raw(), opts.limit)
	if err != nil {
		return err
	}
	printScrubReport(hits, total, opts)
	if !opts.apply || len(hits) == 0 {
		return nil
	}
	return applyScrub(ctx, db.Raw(), hits)
}

// scanAuditLeaks streams every audit row's params_redacted + error_message
// through audit.PatternMatches. Returns the matching rows and the total
// scanned (so the report shows "N of M scanned").
func scanAuditLeaks(ctx context.Context, raw *sql.DB, limit int) ([]leakHit, int, error) {
	q := `SELECT id, tool_name, timestamp, params_redacted, error_message FROM audit_records`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := raw.QueryContext(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("scan audit_records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var hits []leakHit
	total := 0
	for rows.Next() {
		total++
		var id, tool, ts, params, errMsg sql.NullString
		if err := rows.Scan(&id, &tool, &ts, &params, &errMsg); err != nil {
			return nil, total, fmt.Errorf("scan row: %w", err)
		}
		if audit.PatternMatches(params.String) || audit.PatternMatches(errMsg.String) {
			hits = append(hits, leakHit{id: id.String, tool: tool.String, timestamp: ts.String})
		}
	}
	return hits, total, rows.Err()
}

// printScrubReport renders the dry-run / pre-apply summary. Counts only —
// never the matched plaintext (which is the whole reason we're scrubbing).
func printScrubReport(hits []leakHit, total int, opts scrubOpts) {
	mode := "DRY-RUN"
	if opts.apply {
		mode = "APPLY"
	}
	fmt.Printf("mcplexer scrub-audit (%s)\n", mode)
	fmt.Printf("  scanned: %d audit_records rows\n", total)
	fmt.Printf("  leaks:   %d row(s) match value-pattern regex (would be deleted)\n", len(hits))
	if len(hits) == 0 {
		fmt.Println("  ✓ no plaintext leaks remaining in audit_records")
		return
	}
	byTool := make(map[string]int, 8)
	for _, h := range hits {
		byTool[h.tool]++
	}
	fmt.Println("  top tools by leak count:")
	for tool, n := range byTool {
		fmt.Printf("    %-40s %d\n", truncate(tool, 40), n)
	}
	if !opts.apply {
		fmt.Println("\n  → re-run with --apply to scrub. Matched rows will be copied")
		fmt.Println("    to a timestamped backup table first.")
	}
}

// applyScrub copies matched rows into a timestamped backup table and
// deletes them from audit_records. One transaction: either both happen or
// neither does. Backup table is left in place so the operator can audit
// the scrub itself; drop manually after verifying.
func applyScrub(ctx context.Context, raw *sql.DB, hits []leakHit) error {
	backup := fmt.Sprintf("audit_records_scrub_%s", time.Now().UTC().Format("20060102_150405"))
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.id)
	}
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scrub tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	placeholders := "?" + strings.Repeat(",?", len(ids)-1)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`CREATE TABLE %s AS SELECT * FROM audit_records WHERE id IN (%s)`, backup, placeholders),
		args...); err != nil {
		return fmt.Errorf("backup matched rows: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM audit_records WHERE id IN (%s)`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("delete matched rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scrub: %w", err)
	}
	deleted, _ := res.RowsAffected()
	fmt.Printf("\n  ✓ backup table: %s (%d rows)\n", backup, len(ids))
	fmt.Printf("  ✓ deleted: %d row(s) from audit_records\n", deleted)
	fmt.Printf("  ! keep the backup table until you've verified — drop with:\n")
	fmt.Printf("      DROP TABLE %s;\n", backup)
	return nil
}
