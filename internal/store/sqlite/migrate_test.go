package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrate024CleanInstall verifies migration 024 creates p2p_peers with
// the connection_mode column when the table doesn't pre-exist.
func TestMigrate024CleanInstall(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !columnExists(t, db, "p2p_peers", "connection_mode") {
		t.Fatalf("connection_mode column missing after clean install")
	}
}

// TestMigrate024PreExistingTable simulates the case where M1.2 already
// created p2p_peers (without the connection_mode column) before our
// migration runs. The post-migration hook must add the missing column
// without failing the daemon startup.
func TestMigrate024PreExistingTable(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Apply migrations 1..23 first.
	if err := ensureSchemaTable(context.Background(), db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(context.Background(), db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	for _, f := range files {
		if f.version >= 24 {
			break
		}
		if err := applyMigration(context.Background(), db, f); err != nil {
			t.Fatalf("apply %d: %v", f.version, err)
		}
	}

	// Pre-create p2p_peers WITHOUT connection_mode — matches M1.2's shape
	// in case migration 027 (post-migration hook) runs against a DB where
	// 024 has already executed but 027's column-add hasn't yet.
	_, err = db.Exec(`CREATE TABLE p2p_peers (
		peer_id      TEXT PRIMARY KEY,
		display_name TEXT NOT NULL DEFAULT '',
		paired_at    TEXT NOT NULL,
		last_seen    TEXT,
		trust_level  INTEGER NOT NULL DEFAULT 0,
		scopes       TEXT NOT NULL DEFAULT '[]',
		revoked_at   TEXT
	)`)
	if err != nil {
		t.Fatalf("pre-create p2p_peers: %v", err)
	}

	// Run remaining migrations (24+).
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate after pre-existing: %v", err)
	}
	if !columnExists(t, db, "p2p_peers", "connection_mode") {
		t.Fatalf("connection_mode column missing after pre-existing-table case")
	}
}

// TestMigrate028UsersCleanInstall verifies the M7.1 migration creates
// users + peer_users (with the partial unique index on is_self) on a
// fresh DB.
func TestMigrate028UsersCleanInstall(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// users table exists with the expected columns.
	for _, col := range []string{"user_id", "display_name", "created_at", "is_self"} {
		if !columnExists(t, db, "users", col) {
			t.Fatalf("users.%s missing", col)
		}
	}
	for _, col := range []string{"peer_id", "user_id"} {
		if !columnExists(t, db, "peer_users", col) {
			t.Fatalf("peer_users.%s missing", col)
		}
	}
	if !indexExists(t, db, "idx_peer_users_one_owner") {
		t.Fatal("peer_users one-owner unique index missing")
	}

	// Insert two is_self=1 rows must fail thanks to the partial unique
	// index — guards the bootstrap invariant.
	now := "2025-04-30T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO users VALUES ('a','A',?,1)`, now); err != nil {
		t.Fatalf("insert first self: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES ('b','B',?,1)`, now); err == nil {
		t.Fatal("second is_self=1 row should violate partial unique index")
	}
	// Two is_self=0 rows are fine.
	if _, err := db.Exec(`INSERT INTO users VALUES ('c','C',?,0)`, now); err != nil {
		t.Fatalf("insert non-self c: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES ('d','D',?,0)`, now); err != nil {
		t.Fatalf("insert non-self d: %v", err)
	}
}

// TestMigrate028UpgradeFromPriorSchema simulates a daemon already at
// schema_version 27 (pre-M7.1) and confirms applying 028 lands us with
// the new tables intact.
func TestMigrate028UpgradeFromPriorSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	if err := ensureSchemaTable(context.Background(), db); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(context.Background(), db); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	for _, f := range files {
		if f.version >= 28 {
			break
		}
		if err := applyMigration(context.Background(), db, f); err != nil {
			t.Fatalf("apply %d: %v", f.version, err)
		}
		if hook, ok := postMigrationHooks[f.version]; ok {
			if err := hook(context.Background(), db); err != nil {
				t.Fatalf("hook %d: %v", f.version, err)
			}
		}
	}

	// Now run migrate() to advance to the latest — should apply 028 atop
	// the prior schema.
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate up to latest: %v", err)
	}
	if !columnExists(t, db, "users", "is_self") {
		t.Fatal("users table missing after upgrade")
	}
	if !columnExists(t, db, "peer_users", "user_id") {
		t.Fatal("peer_users table missing after upgrade")
	}
}

func TestMigrate133PurgesLegacyWorkspaceCodeIndex(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range files {
		if migration.version >= 133 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply %d: %v", migration.version, err)
		}
		if hook, ok := postMigrationHooks[migration.version]; ok {
			if err := hook(ctx, db); err != nil {
				t.Fatalf("hook %d: %v", migration.version, err)
			}
		}
	}
	now := "2026-07-14T12:00:00Z"
	statements := []string{
		`INSERT INTO code_index_builds(workspace_id, root_path, built_at) VALUES ('legacy-ws','/repo','` + now + `')`,
		`INSERT INTO code_index_files(id,workspace_id,path,indexed_at,chunk_version) VALUES (1,'legacy-ws','a.go','` + now + `',1)`,
		`INSERT INTO code_index_symbols(workspace_id,file_id,name,kind,start_line) VALUES ('legacy-ws',1,'Alpha','func',1)`,
		`INSERT INTO code_index_edges(workspace_id,from_file_id,kind) VALUES ('legacy-ws',1,'import')`,
		`INSERT INTO code_index_chunks(workspace_id,file_id,path,ordinal,indexed_at) VALUES ('legacy-ws',1,'a.go',0,'` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed legacy code index: %v", err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("apply migration 133: %v", err)
	}
	for _, table := range []string{
		"code_index_builds", "code_index_files", "code_index_symbols", "code_index_edges",
		"code_index_chunks", "code_index_files_fts", "code_index_symbols_fts", "code_index_chunks_fts",
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("migration 133 left %d rows in %s", count, table)
		}
	}
}

func TestMigrate136DefaultsLegacyP2PRelationshipsToNoAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range files {
		if migration.version >= 136 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply %d: %v", migration.version, err)
		}
		if hook, ok := postMigrationHooks[migration.version]; ok {
			if err := hook(ctx, db); err != nil {
				t.Fatalf("hook %d: %v", migration.version, err)
			}
		}
	}
	now := "2026-07-16T12:00:00Z"
	statements := []string{
		`INSERT INTO users(user_id, display_name, created_at, is_self) VALUES ('legacy-local', 'Local operator', '` + now + `', 1)`,
		`INSERT INTO users(user_id, display_name, created_at, is_self) VALUES ('legacy-remote', 'Remote label', '` + now + `', 0)`,
		`INSERT INTO p2p_peers(peer_id, display_name, paired_at, trust_level, scopes) VALUES ('peer-local', 'Local device', '` + now + `', 99, '["admin"]')`,
		`INSERT INTO p2p_peers(peer_id, display_name, paired_at, trust_level, scopes) VALUES ('peer-remote', 'Remote device', '` + now + `', 99, '["admin","tasks.read"]')`,
		`INSERT INTO peer_users(peer_id, user_id) VALUES ('peer-local', 'legacy-local')`,
		`INSERT INTO peer_users(peer_id, user_id) VALUES ('peer-remote', 'legacy-remote')`,
		`INSERT INTO workspaces(id, name, root_path, tags, default_policy, source, created_at, updated_at) VALUES ('legacy-workspace', 'Legacy workspace', '', '[]', 'allow', 'api', '` + now + `', '` + now + `')`,
		`INSERT INTO workspace_peer_bindings(peer_id, remote_workspace_id, local_workspace_id, established_at, linked, link_established_by) VALUES ('peer-remote', 'remote-workspace', 'legacy-workspace', 1, 1, 'local')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed pre-136 state: %v\n%s", err, statement)
		}
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("apply migration 136: %v", err)
	}
	assertPrincipal := func(id, status string, owner int) {
		t.Helper()
		var gotStatus string
		var gotOwner int
		if err := db.QueryRow(`SELECT status, is_local_owner FROM p2p_principals WHERE id = ?`, id).Scan(&gotStatus, &gotOwner); err != nil {
			t.Fatalf("query principal %s: %v", id, err)
		}
		if gotStatus != status || gotOwner != owner {
			t.Fatalf("principal %s = (%s,%d), want (%s,%d)", id, gotStatus, gotOwner, status, owner)
		}
	}
	assertPrincipal("legacy-local", "active", 1)
	assertPrincipal("legacy-remote", "legacy_unverified", 0)
	for _, peerID := range []string{"peer-local", "peer-remote"} {
		var status string
		var keyID sql.NullString
		if err := db.QueryRow(`SELECT status, identity_key_id FROM p2p_principal_devices WHERE peer_id = ?`, peerID).Scan(&status, &keyID); err != nil {
			t.Fatalf("query migrated device %s: %v", peerID, err)
		}
		if status != "legacy_unverified" || keyID.Valid {
			t.Fatalf("device %s = status %s, key %#v; want unverified without key", peerID, status, keyID)
		}
	}
	for _, table := range []string{"p2p_workspace_shares", "p2p_workspace_grants", "p2p_principal_invitations"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("migration implicitly authorized %d rows in %s", count, table)
		}
	}
}

// openTestDB opens a fresh sqlite database and registers cleanup.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	return db
}

// columnExists reports whether the named column is present on the table.
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var (
			cid          int
			name, ctype  string
			notnull, pk  int
			defaultValue sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// TestAppliedMigrationsLedgerCleanInstall verifies that a fresh DB
// records every migration in the applied_migrations ledger with a
// non-empty filename and checksum.
func TestAppliedMigrationsLedgerCleanInstall(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}

	rows, err := db.Query(`SELECT version, filename, checksum FROM applied_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query applied_migrations: %v", err)
	}
	defer rows.Close() //nolint:errcheck

	var count int
	applied := map[int]bool{}
	for rows.Next() {
		var ver int
		var fname, csum string
		if err := rows.Scan(&ver, &fname, &csum); err != nil {
			t.Fatalf("scan applied_migrations: %v", err)
		}
		count++
		applied[ver] = true
		if fname == "" {
			t.Errorf("version %d has empty filename", ver)
		}
		if csum == "" {
			t.Errorf("version %d has empty checksum", ver)
		}
		if len(csum) != 64 {
			t.Errorf("version %d: checksum length %d, want 64", ver, len(csum))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate applied_migrations: %v", err)
	}

	if count != len(files) {
		t.Errorf("applied_migrations row count %d != migration file count %d", count, len(files))
	}

	for _, f := range files {
		if !applied[f.version] {
			t.Errorf("migration %d (%s) is missing from applied_migrations", f.version, f.filename)
		}
	}
}

// TestMigrationLedgerDetectsSkippedMigration simulates the failure mode
// where a migration version was recorded but the SQL never ran. After a
// clean migrate, we delete one row from applied_migrations (simulating
// that the SQL was never actually executed despite the version counting
// toward the max). The ledger guard must fail with a clear "skipped" error.
func TestMigrationLedgerDetectsSkippedMigration(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	if len(files) < 10 {
		t.Fatalf("need at least 10 migration files, got %d", len(files))
	}

	// Pick a mid-range migration and delete it from applied_migrations
	// as if it was recorded in schema_version but never actually ran.
	skipVer := files[5].version
	if _, err := db.ExecContext(ctx,
		`DELETE FROM applied_migrations WHERE version = ?`, skipVer,
	); err != nil {
		t.Fatalf("delete applied_migrations %d: %v", skipVer, err)
	}

	if err := verifyMigrationLedger(ctx, db, files); err == nil {
		t.Fatal("expected ledger guard error, got nil")
	} else {
		if !strings.Contains(err.Error(), "skipped") {
			t.Errorf("expected 'skipped' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%d", skipVer)) {
			t.Errorf("expected version %d in error, got: %v", skipVer, err)
		}
	}
}

// TestMigrationLedgerGuardBypass verifies that the ledger guard passes
// (no error) when the skipVer migration was actually applied correctly
// — it's in the applied_migrations set. Regression test for the
// skipped-migration detection not being a false positive.
func TestMigrationLedgerGuardBypass(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	if err := verifyMigrationLedger(context.Background(), db, files); err != nil {
		t.Fatalf("ledger guard unexpectedly failed on clean install: %v", err)
	}
}
