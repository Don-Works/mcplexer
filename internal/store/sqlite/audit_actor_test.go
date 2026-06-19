package sqlite

import (
	"context"
	"testing"
)

// TestMigrate053BackfillsActorColumns simulates a daemon at schema 52
// holding rows that already used the worker:/scope: SessionID
// conventions, then advances to 053 and asserts the backfill produced
// the expected actor_kind / actor_id values.
func TestMigrate053BackfillsActorColumns(t *testing.T) {
	t.Parallel()
	rawDB := openTestDB(t)

	if err := ensureSchemaTable(context.Background(), rawDB); err != nil {
		t.Fatalf("ensureSchemaTable: %v", err)
	}
	if err := ensureLedgerTable(context.Background(), rawDB); err != nil {
		t.Fatalf("ensureLedgerTable: %v", err)
	}
	files, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	for _, f := range files {
		if f.version >= 53 {
			break
		}
		if err := applyMigration(context.Background(), rawDB, f); err != nil {
			t.Fatalf("apply %d: %v", f.version, err)
		}
		if hook, ok := postMigrationHooks[f.version]; ok {
			if err := hook(context.Background(), rawDB); err != nil {
				t.Fatalf("hook %d: %v", f.version, err)
			}
		}
	}

	// Seed audit rows that look like they were written by the existing
	// emit sites BEFORE the new columns existed.
	seedSQL := `INSERT INTO audit_records
		(id, timestamp, session_id, client_type, tool_name,
		 params_redacted, status, created_at)
		VALUES (?, ?, ?, ?, ?, '{}', 'ok', ?)`
	now := "2026-01-01T00:00:00Z"
	cases := []struct {
		id, sid, ct                  string
		wantKind, wantID, wantCorrel string
	}{
		{"a", "worker:wrk-1", "worker", "worker", "wrk-1", ""},
		// case b — the greedy-LIKE bug. 053's CASE branch order MUST
		// match the literal 'worker_admin' before the LIKE 'worker%'
		// fallback, otherwise worker_admin rows get bucketed as
		// 'worker' and forensics joins on actor_kind miss them.
		{"b", "worker:wrk-2", "worker_admin", "worker_admin", "wrk-2", ""},
		{"c", "scope:s-xyz", "secrets", "secrets", "s-xyz", ""},
		{"d", "sess-claude", "claude-code", "user", "sess-claude", ""},
		{"e", "sess-other", "scheduler", "scheduler", "sess-other", ""},
	}
	for _, tc := range cases {
		if _, err := rawDB.Exec(seedSQL, tc.id, now, tc.sid, tc.ct, "evt", now); err != nil {
			t.Fatalf("seed %s: %v", tc.id, err)
		}
	}

	// Apply 053+ and confirm backfill ran.
	if err := migrate(context.Background(), rawDB); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}
	for _, col := range []string{"actor_kind", "actor_id", "correlation_id"} {
		if !columnExists(t, rawDB, "audit_records", col) {
			t.Fatalf("audit_records.%s missing after migration", col)
		}
	}

	for _, tc := range cases {
		var gotKind, gotID, gotCorrel string
		err := rawDB.QueryRow(
			`SELECT actor_kind, actor_id, correlation_id
			 FROM audit_records WHERE id = ?`, tc.id).
			Scan(&gotKind, &gotID, &gotCorrel)
		if err != nil {
			t.Fatalf("scan %s: %v", tc.id, err)
		}
		if gotKind != tc.wantKind {
			t.Errorf("%s actor_kind = %q, want %q", tc.id, gotKind, tc.wantKind)
		}
		if gotID != tc.wantID {
			t.Errorf("%s actor_id = %q, want %q", tc.id, gotID, tc.wantID)
		}
		if gotCorrel != tc.wantCorrel {
			t.Errorf("%s correlation_id = %q, want empty", tc.id, gotCorrel)
		}
	}
}

// TestMigrate054RecategorizesLegacyWorkerAdminRows simulates a daemon
// that ran the *original* (greedy-LIKE) version of 053, so historical
// worker_admin rows got actor_kind='worker'. After 054 lands the rows
// MUST be recategorized to actor_kind='worker_admin'. Critically, rows
// that were legitimately classified as 'worker' (i.e. client_type !=
// 'worker_admin') MUST be left alone — 054's WHERE is narrow.
func TestMigrate054RecategorizesLegacyWorkerAdminRows(t *testing.T) {
	t.Parallel()
	rawDB := openTestDB(t)
	if err := migrate(context.Background(), rawDB); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}

	// Plant the post-buggy-053 state: one worker_admin row mis-bucketed
	// as actor_kind='worker', plus one legitimately worker-class row
	// that 054 must NOT touch.
	const seed = `INSERT INTO audit_records
		(id, timestamp, session_id, client_type, tool_name,
		 params_redacted, status, created_at,
		 actor_kind, actor_id, correlation_id)
		VALUES (?, ?, ?, ?, ?, '{}', 'ok', ?, ?, ?, '')`
	now := "2026-01-01T00:00:00Z"
	if _, err := rawDB.Exec(seed,
		"buggy-admin", now, "worker:wrk-a", "worker_admin", "worker_admin.create",
		now, "worker", "wrk-a"); err != nil {
		t.Fatalf("seed buggy admin row: %v", err)
	}
	if _, err := rawDB.Exec(seed,
		"legit-runner", now, "worker:wrk-b", "worker", "worker.tool_call",
		now, "worker", "wrk-b"); err != nil {
		t.Fatalf("seed legit worker row: %v", err)
	}

	// Re-execute 054's UPDATE so the recategorisation runs on our
	// planted rows (the live migration pass above ran on an empty
	// table, so it had nothing to fix — that's the upgrade-path
	// scenario this test models).
	if _, err := rawDB.Exec(`
		UPDATE audit_records
		SET actor_kind = 'worker_admin'
		WHERE actor_kind = 'worker'
		  AND client_type = 'worker_admin'`); err != nil {
		t.Fatalf("re-run 054 update: %v", err)
	}

	cases := []struct {
		id            string
		wantActorKind string
	}{
		{"buggy-admin", "worker_admin"},
		{"legit-runner", "worker"},
	}
	for _, tc := range cases {
		var got string
		if err := rawDB.QueryRow(
			`SELECT actor_kind FROM audit_records WHERE id = ?`, tc.id,
		).Scan(&got); err != nil {
			t.Fatalf("scan %s: %v", tc.id, err)
		}
		if got != tc.wantActorKind {
			t.Errorf("%s actor_kind = %q, want %q", tc.id, got, tc.wantActorKind)
		}
	}
}
