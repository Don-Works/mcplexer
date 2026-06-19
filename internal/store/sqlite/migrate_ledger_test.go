package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// openLedgerTestDB opens a fresh sqlite DB and runs the full
// migrate() pipeline so every test starts from a healthy state.
func openLedgerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// openEmptySQLiteDB opens a sqlite file at a temp path with FKs
// enabled, no migrations run. Used by tests that want to control
// the boot sequence themselves (e.g. to simulate a pre-ledger
// install where only schema_version exists).
func openEmptySQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	return db
}

// countLedgerRows returns the number of rows in applied_migrations
// for the given version. Used to assert backfill / apply semantics.
func countLedgerRows(t *testing.T, db *sql.DB, version int) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM applied_migrations WHERE version = ?`, version,
	).Scan(&n); err != nil {
		t.Fatalf("count ledger v=%d: %v", version, err)
	}
	return n
}

// TestLedgerHealthyDB — running migrate() on a fresh DB must leave
// the ledger in a state where verifyLedger() reports no issues.
func TestLedgerHealthyDB(t *testing.T) {
	t.Parallel()
	db := openLedgerTestDB(t)

	issues, err := verifyLedger(context.Background(), db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	if len(issues) != 0 {
		for _, i := range issues {
			t.Logf("unexpected issue: %s", i)
		}
		t.Fatalf("expected zero issues on a healthy DB, got %d", len(issues))
	}
}

// TestLedgerBackfillOldDB — a DB that already has schema_version
// rows for every applied migration but no applied_migrations table
// must upgrade cleanly via backfillLedger(), without raising any
// verifyLedger() errors. This is the upgrade path for a healthy
// pre-100 install: every on-disk migration has a schema_version
// row, the ledger table doesn't exist, the first boot after the
// daemon ships migration 100 must populate the ledger without
// tripping any skipped-migration warnings.
func TestLedgerBackfillOldDB(t *testing.T) {
	t.Parallel()
	db := openEmptySQLiteDB(t)
	ctx := context.Background()

	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}

	// Populate schema_version with EVERY on-disk migration
	// version. This is exactly what a healthy pre-100 install
	// looks like at the moment the new daemon ships.
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	// Exclude migration 100 itself — it doesn't exist yet on
	// a pre-100 install. Backfill is for the historical rows;
	// migration 100 gets applied through the normal loop.
	preLedger := make([]migrationFile, 0, len(files))
	for _, f := range files {
		if f.version >= 100 {
			continue
		}
		preLedger = append(preLedger, f)
	}
	if len(preLedger) == 0 {
		t.Fatal("no pre-ledger migrations on disk — test is meaningless")
	}
	for _, f := range preLedger {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
			f.version,
		); err != nil {
			t.Fatalf("seed schema_version v=%d: %v", f.version, err)
		}
	}

	// Bring the ledger table online + run backfill. This is
	// what migrate() does between ensureLedgerTable and the
	// migration loop.
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	if err := backfillLedger(ctx, db); err != nil {
		t.Fatalf("backfillLedger: %v", err)
	}

	// Every pre-ledger version that has an on-disk file must
	// now be in the ledger. Spot-check a few well-known
	// milestones rather than asserting an exact count (the
	// on-disk list can shift between releases).
	for _, v := range []int{1, 24, 53, 70, 72, 99} {
		if n := countLedgerRows(t, db, v); n != 1 {
			t.Errorf("expected 1 ledger row for v=%d after backfill, got %d", v, n)
		}
	}

	// Verify must be silent on errors — the only issue should
	// be a future_migration WARNING for v=100, which is the
	// very file we just introduced and haven't applied yet
	// (this test stops at backfill, before the migration
	// loop). Warnings are not errors; we explicitly filter
	// for severity="error" so the future-file warning
	// doesn't false-positive the test.
	issues, err := verifyLedger(ctx, db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	for _, i := range issues {
		if i.Severity == "error" {
			t.Errorf("unexpected post-backfill error: %s", i)
		}
	}
}

// TestLedgerBackfillIdempotent — running backfillLedger() twice on
// the same DB must be a no-op the second time. This is what
// guarantees a healthy install doesn't accumulate duplicate ledger
// rows on every boot.
func TestLedgerBackfillIdempotent(t *testing.T) {
	t.Parallel()
	db := openEmptySQLiteDB(t)
	ctx := context.Background()

	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	for _, v := range []int{1, 2, 5, 27, 99} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`, v,
		); err != nil {
			t.Fatalf("seed v=%d: %v", v, err)
		}
	}
	if err := backfillLedger(ctx, db); err != nil {
		t.Fatalf("backfill 1: %v", err)
	}
	// Snapshot the count.
	var n1 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM applied_migrations`).Scan(&n1); err != nil {
		t.Fatalf("count 1: %v", err)
	}
	if err := backfillLedger(ctx, db); err != nil {
		t.Fatalf("backfill 2: %v", err)
	}
	var n2 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM applied_migrations`).Scan(&n2); err != nil {
		t.Fatalf("count 2: %v", err)
	}
	if n1 != n2 {
		t.Fatalf("backfill not idempotent: first=%d second=%d", n1, n2)
	}
	if n1 != 5 {
		t.Fatalf("expected 5 rows after backfill, got %d", n1)
	}
}

// TestLedgerSkippedMigration — the 072 outage pattern. A migration
// file exists on disk, the ledger has no row for it, but
// schema_version's MAX is past it. verifyLedger() must flag this
// as a "skipped_migration" error so the operator (or CI) sees it.
func TestLedgerSkippedMigration(t *testing.T) {
	t.Parallel()
	db := openLedgerTestDB(t)
	ctx := context.Background()

	// Pick the highest applied version, then delete its row
	// from the ledger while leaving schema_version at MAX.
	// This produces the classic "watermark is past me but I'm
	// not in the ledger" failure.
	var maxV int
	if err := db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_version`,
	).Scan(&maxV); err != nil {
		t.Fatalf("read max version: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM applied_migrations WHERE version = ?`, maxV,
	); err != nil {
		t.Fatalf("tamper ledger: %v", err)
	}

	issues, err := verifyLedger(ctx, db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	found := false
	for _, i := range issues {
		if i.Code == "skipped_migration" && i.Version == maxV {
			found = true
			if i.Severity != "error" {
				t.Errorf("skipped_migration severity = %q, want error", i.Severity)
			}
			if !strings.Contains(i.Message, strconv.Itoa(maxV)) {
				t.Errorf("skipped_migration message %q does not mention version", i.Message)
			}
		}
	}
	if !found {
		t.Fatalf("expected skipped_migration issue for v=%d, got %+v", maxV, issues)
	}
}

// TestLedgerChecksumMismatch — the ledger's stored checksum for a
// given migration must match the SHA256 of the file currently on
// disk. Tampering with the stored checksum must be detected.
func TestLedgerChecksumMismatch(t *testing.T) {
	t.Parallel()
	db := openLedgerTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`UPDATE applied_migrations
		    SET checksum = 'deadbeef'
		  WHERE version = (
		      SELECT MIN(version) FROM applied_migrations
		  )`,
	); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}

	issues, err := verifyLedger(ctx, db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	found := false
	for _, i := range issues {
		if i.Code == "checksum_mismatch" {
			found = true
			if i.Severity != "error" {
				t.Errorf("checksum_mismatch severity = %q, want error", i.Severity)
			}
			// Message should show the stored (tampered)
			// checksum so the operator can tell at a
			// glance which direction the drift went.
			if !strings.Contains(i.Message, "deadbeef") {
				t.Errorf("checksum_mismatch message %q does not mention stored checksum", i.Message)
			}
		}
	}
	if !found {
		t.Fatalf("expected checksum_mismatch issue, got %+v", issues)
	}
}

// TestLedgerComputeChecksumStable — SHA256 of the same migration
// file must be stable across calls. Backfill relies on this; if
// Go's hash function ever changed under us, the backfilled rows
// would silently disagree with applyMigration's rows.
func TestLedgerComputeChecksumStable(t *testing.T) {
	t.Parallel()
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files on disk — test is meaningless")
	}
	f := files[len(files)/2] // arbitrary mid-range file
	a, err := computeMigrationChecksum(f.filename)
	if err != nil {
		t.Fatalf("first compute: %v", err)
	}
	b, err := computeMigrationChecksum(f.filename)
	if err != nil {
		t.Fatalf("second compute: %v", err)
	}
	if a != b {
		t.Fatalf("checksum not stable: %s vs %s", a, b)
	}
	// SHA256 is 32 bytes -> 64 hex chars.
	if len(a) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars: %q", len(a), a)
	}
}

// TestLedgerCollision — two on-disk files claiming the same
// version number is a packaging bug and the daemon must refuse to
// migrate. detectCollisions() is the unit of decision; we feed it
// a synthetic list (the on-disk FS can't be tampered with at test
// time because migrationsFS is embedded).
func TestLedgerCollision(t *testing.T) {
	t.Parallel()
	colliding := []migrationFile{
		{version: 1, filename: "001_a.sql"},
		{version: 2, filename: "002_a.sql"},
		{version: 2, filename: "002_b.sql"}, // collision
		{version: 3, filename: "003_a.sql"},
	}
	if err := detectCollisions(colliding); err == nil {
		t.Fatal("expected collision error, got nil")
	} else if !strings.Contains(err.Error(), "002") {
		t.Errorf("collision error should mention version 002, got: %v", err)
	}
}

// TestLedgerNoCollision — a clean list passes.
func TestLedgerNoCollision(t *testing.T) {
	t.Parallel()
	clean := []migrationFile{
		{version: 1, filename: "001_a.sql"},
		{version: 2, filename: "002_a.sql"},
		{version: 3, filename: "003_a.sql"},
	}
	if err := detectCollisions(clean); err != nil {
		t.Fatalf("clean list flagged as collision: %v", err)
	}
}

// TestLedgerOrphanRow — a row in the ledger whose version has no
// matching on-disk file (e.g. a partial restore deleted 072.sql
// but left the ledger row) must be reported as orphan_row.
func TestLedgerOrphanRow(t *testing.T) {
	t.Parallel()
	db := openLedgerTestDB(t)
	ctx := context.Background()

	// Inject a row for a fictional version that no on-disk
	// file claims. The on-disk FS is embedded so we can't
	// delete real files, but we can add a ledger row that
	// references a version the FS has never heard of.
	const fakeVersion = 9999
	if _, err := db.ExecContext(ctx,
		`INSERT INTO applied_migrations
			(version, filename, checksum, applied_at)
		 VALUES (?, '9999_phantom.sql', 'abc', datetime('now'))`,
		fakeVersion,
	); err != nil {
		t.Fatalf("insert orphan: %v", err)
	}

	issues, err := verifyLedger(ctx, db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	found := false
	for _, i := range issues {
		if i.Code == "orphan_row" && i.Version == fakeVersion {
			found = true
			if i.Severity != "error" {
				t.Errorf("orphan_row severity = %q, want error", i.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected orphan_row issue for v=%d, got %+v", fakeVersion, issues)
	}
}

// TestLedgerFutureMigration — a file on disk with a version
// higher than MAX(schema_version) AND no row in the ledger is
// fine; it's a migration that hasn't been applied yet.
// verifyLedger() should report it as a warning (not an error)
// so the boot doesn't false-positive on the next migration
// that's about to land.
func TestLedgerFutureMigration(t *testing.T) {
	t.Parallel()
	db := openEmptySQLiteDB(t)
	ctx := context.Background()

	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}

	// Apply only the first 30 migration files. The remaining
	// on-disk files (v=31..100) are "future": not in the
	// ledger, and ahead of the schema_version watermark.
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	const appliedThrough = 30
	for _, f := range files {
		if f.version > appliedThrough {
			break
		}
		if err := applyMigration(ctx, db, f); err != nil {
			t.Fatalf("apply %d: %v", f.version, err)
		}
	}

	// Sanity: schema_version is at appliedThrough and the
	// ledger has exactly appliedThrough rows.
	var maxV int
	if err := db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_version`,
	).Scan(&maxV); err != nil {
		t.Fatalf("read max: %v", err)
	}
	if maxV != appliedThrough {
		t.Fatalf("expected MAX(schema_version)=%d, got %d", appliedThrough, maxV)
	}

	issues, err := verifyLedger(ctx, db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	hasFuture := false
	for _, i := range issues {
		if i.Code == "future_migration" {
			hasFuture = true
			if i.Severity != "warning" {
				t.Errorf("future_migration severity = %q, want warning", i.Severity)
			}
			// Future migrations should be at or beyond
			// appliedThrough+1.
			if i.Version <= appliedThrough {
				t.Errorf("future_migration v=%d should be > %d", i.Version, appliedThrough)
			}
		}
		// The schema_version watermark is the source of
		// truth for "what's been applied", NOT the ledger
		// row count. Files 1..appliedThrough are in the
		// ledger AND <= maxSchema, so no skipped_migration
		// errors are expected.
		if i.Code == "skipped_migration" {
			t.Errorf("unexpected skipped_migration for v=%d when MAX(schema_version)=%d: %s",
				i.Version, maxV, i.Message)
		}
	}
	if !hasFuture {
		t.Fatalf("expected at least one future_migration warning, got %+v", issues)
	}
}

// TestApplyMigrationWritesLedgerRow — applyMigration() must
// insert into BOTH schema_version and applied_migrations inside
// the same transaction. A successful apply on a fresh DB
// therefore yields two rows for the version, and the recorded
// checksum must agree with what computeMigrationChecksum
// recomputes from the same file (the source-of-truth agreement
// that verifyLedger() depends on).
func TestApplyMigrationWritesLedgerRow(t *testing.T) {
	t.Parallel()
	db := openEmptySQLiteDB(t)
	ctx := context.Background()

	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files on disk")
	}
	if err := applyMigration(ctx, db, files[0]); err != nil {
		t.Fatalf("applyMigration: %v", err)
	}

	var schemaN, ledgerN int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_version WHERE version = ?`, files[0].version,
	).Scan(&schemaN); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM applied_migrations WHERE version = ?`, files[0].version,
	).Scan(&ledgerN); err != nil {
		t.Fatalf("count applied_migrations: %v", err)
	}
	if schemaN != 1 {
		t.Errorf("schema_version row count = %d, want 1", schemaN)
	}
	if ledgerN != 1 {
		t.Errorf("applied_migrations row count = %d, want 1", ledgerN)
	}
	var got string
	if err := db.QueryRow(
		`SELECT checksum FROM applied_migrations WHERE version = ?`, files[0].version,
	).Scan(&got); err != nil {
		t.Fatalf("read checksum: %v", err)
	}
	want, err := computeMigrationChecksum(files[0].filename)
	if err != nil {
		t.Fatalf("computeMigrationChecksum: %v", err)
	}
	if got != want {
		t.Fatalf("ledger checksum %q does not match recomputed %q", got, want)
	}
}

// TestLedgerStrictHealthyOnFullRun — a full migrate() of a fresh
// DB followed by verifyLedger() must be clean. Mirrors the
// production boot path end-to-end so a regression in either
// layer trips the test.
func TestLedgerStrictHealthyOnFullRun(t *testing.T) {
	t.Parallel()
	db := openLedgerTestDB(t)
	issues, err := verifyLedger(context.Background(), db)
	if err != nil {
		t.Fatalf("verifyLedger: %v", err)
	}
	for _, i := range issues {
		if i.Severity == "error" {
			t.Errorf("healthy DB has error: %s", i)
		}
	}
}

func TestHistoricalSeedMigrationChecksums(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"052_bundled_worker_templates.sql":       "ed12789a12089bef3f1697a191e925f75e65f003ace807b63a09e2d6b6a58410",
		"084_bundled_telegram_slack_workers.sql": "de05d9d338d5d9ed96a0b91271ad02ea1e16fa10cc1dcbf96d29ba56037d0db8",
		"098_telegram_responder_delegation.sql":  "09d0bc141bafa5be3278dab8fdebea23b335a7a8c837c7a4d133f3eb7634ffd7",
	}
	for filename, want := range cases {
		t.Run(filename, func(t *testing.T) {
			t.Parallel()
			got, err := computeMigrationChecksum(filename)
			if err != nil {
				t.Fatalf("computeMigrationChecksum: %v", err)
			}
			if got != want {
				t.Fatalf("checksum = %s, want %s; do not edit applied migrations in place", got, want)
			}
		})
	}
}
