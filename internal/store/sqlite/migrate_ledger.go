package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// LedgerIssue describes a single integrity problem detected by
// verifyLedger(). Severity is "error" (daemon-side invariant broken)
// or "warning" (informational, e.g. a file that's ahead of the
// watermark). Code is one of: "orphan_row", "skipped_migration",
// "checksum_mismatch", "checksum_error", "future_migration".
type LedgerIssue struct {
	Code     string
	Severity string
	Version  int
	Filename string
	Message  string
}

func (i LedgerIssue) String() string {
	if i.Filename != "" {
		return fmt.Sprintf("[%s] %s: %s (version %d, file %s)",
			i.Severity, i.Code, i.Message, i.Version, i.Filename)
	}
	return fmt.Sprintf("[%s] %s: %s (version %d)",
		i.Severity, i.Code, i.Message, i.Version)
}

// LedgerRehashOptions controls RehashMigrationLedger.
type LedgerRehashOptions struct {
	DryRun bool
}

// LedgerRehashRow describes a ledger row whose recorded file metadata does not
// match the migration file currently embedded in this build.
type LedgerRehashRow struct {
	Version          int
	RecordedFilename string
	CurrentFilename  string
	RecordedChecksum string
	CurrentChecksum  string
	Updated          bool
}

// LedgerRehashReport is returned by RehashMigrationLedger. Rows lists the
// records that needed repair; VerifyIssues is the read-only ledger verification
// result after the operation, or the current verification result for dry-runs.
type LedgerRehashReport struct {
	Rows         []LedgerRehashRow
	VerifyIssues []LedgerIssue
}

// ensureLedgerTable creates the applied_migrations table if it
// doesn't already exist. Idempotent. Mirrors ensureSchemaTable for
// the legacy schema_version table — both must be present before the
// first migration loop iteration.
func ensureLedgerTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS applied_migrations (
			version    INTEGER PRIMARY KEY,
			filename   TEXT NOT NULL,
			checksum   TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`)
	return err
}

// computeMigrationChecksum returns the lowercase hex SHA256 of the
// named migration file's content. Used by applyMigration (to record
// what was applied) and verifyLedger (to compare against what's
// recorded). Deterministic and stable across runs.
func computeMigrationChecksum(filename string) (string, error) {
	data, err := migrationsFS.ReadFile("migrations/" + filename)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filename, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// backfillLedger populates applied_migrations with rows for every
// version already in schema_version that isn't yet in the ledger.
// Idempotent (INSERT OR IGNORE keyed on the primary key).
//
// Required for old DBs upgrading to a daemon that understands the
// ledger — without it, the first verifyLedger() run on a pre-100
// install would flag every existing migration as "skipped". For each
// row in schema_version, this function looks up the on-disk file by
// version and records its current checksum. If a file is missing on
// disk for a given version, the row is intentionally left out of the
// ledger so verifyLedger can surface it as an orphan rather than a
// (false) skipped-migration error.
func backfillLedger(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx,
		`SELECT version FROM schema_version ORDER BY version ASC`)
	if err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scan version: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate versions: %w", err)
	}
	if len(versions) == 0 {
		return nil
	}

	files, err := listMigrations()
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	byVersion := make(map[int]migrationFile, len(files))
	for _, f := range files {
		byVersion[f.version] = f
	}

	for _, v := range versions {
		f, ok := byVersion[v]
		if !ok {
			// No on-disk file for this version. Skip the
			// backfill; verifyLedger will surface it as
			// orphan_row so the operator notices.
			continue
		}
		sum, err := computeMigrationChecksum(f.filename)
		if err != nil {
			return fmt.Errorf("checksum for %s: %w", f.filename, err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO applied_migrations
				(version, filename, checksum, applied_at)
			SELECT ?, ?, ?, applied_at
			  FROM schema_version
			 WHERE version = ?`,
			v, f.filename, sum, v,
		); err != nil {
			return fmt.Errorf("backfill applied_migrations v=%d: %w", v, err)
		}
	}
	return nil
}

// RehashMigrationLedger updates stale applied_migrations filename/checksum
// fields to match the migration files embedded in the current binary. It is
// intentionally narrow: it only repairs rows whose version still exists on disk,
// and it never inserts missing rows or deletes orphan rows. Structural migration
// problems remain visible through verifyLedger().
func RehashMigrationLedger(
	ctx context.Context,
	db *sql.DB,
	opts LedgerRehashOptions,
) (LedgerRehashReport, error) {
	var report LedgerRehashReport
	if db == nil {
		return report, fmt.Errorf("nil database")
	}

	files, err := listMigrations()
	if err != nil {
		return report, fmt.Errorf("list migrations: %w", err)
	}
	if err := detectCollisions(files); err != nil {
		return report, err
	}
	byVersion := make(map[int]migrationFile, len(files))
	for _, f := range files {
		byVersion[f.version] = f
	}

	rows, err := db.QueryContext(ctx,
		`SELECT version, filename, checksum FROM applied_migrations ORDER BY version ASC`)
	if err != nil {
		return report, fmt.Errorf("read applied_migrations: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var version int
		var filename, checksum string
		if err := rows.Scan(&version, &filename, &checksum); err != nil {
			return report, fmt.Errorf("scan ledger row: %w", err)
		}
		file, ok := byVersion[version]
		if !ok {
			continue
		}
		currentChecksum, err := computeMigrationChecksum(file.filename)
		if err != nil {
			return report, fmt.Errorf("checksum for %s: %w", file.filename, err)
		}
		if filename == file.filename && strings.EqualFold(checksum, currentChecksum) {
			continue
		}
		report.Rows = append(report.Rows, LedgerRehashRow{
			Version:          version,
			RecordedFilename: filename,
			CurrentFilename:  file.filename,
			RecordedChecksum: checksum,
			CurrentChecksum:  currentChecksum,
		})
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("iterate ledger: %w", err)
	}

	if !opts.DryRun && len(report.Rows) > 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, fmt.Errorf("begin ledger rehash: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck

		for i := range report.Rows {
			row := &report.Rows[i]
			res, err := tx.ExecContext(ctx, `
				UPDATE applied_migrations
				   SET filename = ?, checksum = ?
				 WHERE version = ?
				   AND checksum = ?`,
				row.CurrentFilename,
				row.CurrentChecksum,
				row.Version,
				row.RecordedChecksum,
			)
			if err != nil {
				return report, fmt.Errorf("rehash ledger v=%d: %w", row.Version, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return report, fmt.Errorf("rehash ledger v=%d rows affected: %w", row.Version, err)
			}
			if affected != 1 {
				return report, fmt.Errorf(
					"rehash ledger v=%d: expected to update 1 row, updated %d",
					row.Version,
					affected,
				)
			}
			row.Updated = true
		}
		if err := tx.Commit(); err != nil {
			return report, fmt.Errorf("commit ledger rehash: %w", err)
		}
	}

	issues, err := verifyLedger(ctx, db)
	if err != nil {
		return report, fmt.Errorf("verify ledger: %w", err)
	}
	report.VerifyIssues = issues
	return report, nil
}

// detectCollisions returns an error if any two entries share a
// version number. The on-disk migration file set must be
// one-file-per-version; duplicates are a developer / release
// packaging bug that the daemon must refuse to start against.
func detectCollisions(files []migrationFile) error {
	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})
	seen := make(map[int]string, len(files))
	for _, f := range files {
		if prev, ok := seen[f.version]; ok {
			return fmt.Errorf(
				"migration collision: version %d is claimed by both %q and %q",
				f.version, prev, f.filename,
			)
		}
		seen[f.version] = f.filename
	}
	return nil
}

// verifyLedger inspects the applied_migrations table and the on-disk
// migration files for integrity issues. Returns a slice of
// LedgerIssue (empty when healthy) and any transport-level error.
//
// Checks performed:
//   - "orphan_row"  (error)   ledger has a row for V but no on-disk
//     file claims V. Partial restore / file
//     deletion / version rename.
//   - "skipped_migration" (error)
//     on-disk file with version V exists, the
//     ledger has no row for V, and
//     schema_version's MAX >= V. The 072
//     outage pattern — the migration was
//     skipped, but the watermark says we're
//     past it. Schema invariants provide
//     best-effort healing for known cases;
//     this guard surfaces the class.
//   - "checksum_mismatch" (error)
//     ledger has a row for V with checksum
//     X, the on-disk file's current SHA256
//     is Y != X. File modified after apply.
//   - "checksum_error" (error)
//     the on-disk file referenced by the
//     ledger could not be read at all.
//   - "future_migration" (warning)
//     on-disk file with version V has no
//     ledger row and schema_version's MAX <
//     V. Not an error — just notes the file
//     hasn't been applied yet.
//
// verifyLedger is read-only: it never mutates the database. Callers
// (boot, CI) decide whether to fail, log, or alert on each issue.
func verifyLedger(ctx context.Context, db *sql.DB) ([]LedgerIssue, error) {
	files, err := listMigrations()
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	if err := detectCollisions(files); err != nil {
		return nil, err
	}
	byVersion := make(map[int]migrationFile, len(files))
	for _, f := range files {
		byVersion[f.version] = f
	}

	// Read the full ledger. version is the PK so this is
	// naturally ordered; we don't depend on the order here.
	rows, err := db.QueryContext(ctx,
		`SELECT version, filename, checksum FROM applied_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied_migrations: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	type ledgerRow struct {
		filename string
		checksum string
	}
	ledger := make(map[int]ledgerRow)
	for rows.Next() {
		var v int
		var fn, cs string
		if err := rows.Scan(&v, &fn, &cs); err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		ledger[v] = ledgerRow{filename: fn, checksum: cs}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ledger: %w", err)
	}

	var maxSchema int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`,
	).Scan(&maxSchema); err != nil {
		return nil, fmt.Errorf("read schema_version max: %w", err)
	}

	var issues []LedgerIssue

	// Check 1: orphan rows — ledger has V, no on-disk file.
	for v, r := range ledger {
		if _, ok := byVersion[v]; ok {
			continue
		}
		issues = append(issues, LedgerIssue{
			Code:     "orphan_row",
			Severity: "error",
			Version:  v,
			Filename: r.filename,
			Message:  "ledger row references a version with no on-disk migration file",
		})
	}

	// Check 2: skipped / future migrations.
	for v, f := range byVersion {
		if _, ok := ledger[v]; ok {
			continue
		}
		if v <= maxSchema {
			issues = append(issues, LedgerIssue{
				Code:     "skipped_migration",
				Severity: "error",
				Version:  v,
				Filename: f.filename,
				Message: fmt.Sprintf(
					"migration file %s exists but the ledger has no row for it "+
						"(schema_version watermark is %d)",
					f.filename, maxSchema,
				),
			})
		} else {
			issues = append(issues, LedgerIssue{
				Code:     "future_migration",
				Severity: "warning",
				Version:  v,
				Filename: f.filename,
				Message:  "migration file is ahead of the schema_version watermark",
			})
		}
	}

	// Check 3: checksum mismatches and read errors.
	for v, r := range ledger {
		f, ok := byVersion[v]
		if !ok {
			continue // already covered as orphan_row
		}
		sum, err := computeMigrationChecksum(f.filename)
		if err != nil {
			issues = append(issues, LedgerIssue{
				Code:     "checksum_error",
				Severity: "error",
				Version:  v,
				Filename: f.filename,
				Message:  fmt.Sprintf("could not read file to recompute checksum: %v", err),
			})
			continue
		}
		if !strings.EqualFold(sum, r.checksum) {
			issues = append(issues, LedgerIssue{
				Code:     "checksum_mismatch",
				Severity: "error",
				Version:  v,
				Filename: f.filename,
				Message: fmt.Sprintf(
					"on-disk SHA256 %s does not match recorded %s — "+
						"the file was modified after apply",
					sum, r.checksum,
				),
			})
		}
	}

	return issues, nil
}
