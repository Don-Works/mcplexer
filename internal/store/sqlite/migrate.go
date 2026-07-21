package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func migrate(ctx context.Context, db *sql.DB) error {
	if err := ensureSchemaTable(ctx, db); err != nil {
		return fmt.Errorf("ensure schema table: %w", err)
	}
	if err := ensureLedgerTable(ctx, db); err != nil {
		return fmt.Errorf("ensure ledger table: %w", err)
	}

	if err := backfillAppliedMigrationsOnce(ctx, db); err != nil {
		return fmt.Errorf("backfill applied migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("get applied versions: %w", err)
	}

	// Backfill the ledger from schema_version BEFORE running the
	// migration loop. On the first boot after the ledger table is
	// introduced, this populates rows for every previously applied
	// migration so verifyLedger() doesn't false-positive a
	// skipped-migration storm. Idempotent: re-running on a healthy
	// DB is a no-op.
	if err := backfillLedger(ctx, db); err != nil {
		return fmt.Errorf("backfill ledger: %w", err)
	}

	files, err := listMigrations()
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	if err := detectCollisions(files); err != nil {
		return fmt.Errorf("migration collisions: %w", err)
	}

	for _, f := range files {
		if applied[f.version] {
			continue
		}
		if err := applyMigration(ctx, db, f); err != nil {
			return fmt.Errorf("apply migration %d: %w", f.version, err)
		}
		applied[f.version] = true
		if hook, ok := postMigrationHooks[f.version]; ok {
			if err := hook(ctx, db); err != nil {
				return fmt.Errorf("post-hook for migration %d: %w", f.version, err)
			}
		}
	}

	// Watchdog: surface integrity issues from the ledger after the
	// migration loop. Non-fatal by design — the schema invariants
	// already handle best-effort healing for specific known cases,
	// and we don't want to crash a working daemon on a legacy
	// install where the ledger didn't exist when its rows were
	// created. Operators should monitor the audit log for these
	// messages; CI / tests can call verifyLedger() directly to
	// assert on the issues.
	if issues, err := verifyLedger(ctx, db); err != nil {
		return fmt.Errorf("verify ledger: %w", err)
	} else if len(issues) > 0 {
		for _, i := range issues {
			fmt.Fprintf(os.Stderr,
				"mcplexer migration ledger: %s\n", i)
		}
	}

	// Self-heal invariants — run after every boot regardless of whether
	// migrations applied. Covers the case where schema_version was bumped
	// past a migration that never actually applied (branch swaps, manual
	// edits, partially-restored backups). Each invariant is an idempotent
	// "ensure X column exists" check.
	for _, fn := range schemaInvariants {
		if err := fn(ctx, db); err != nil {
			return fmt.Errorf("schema invariant: %w", err)
		}
	}

	if err := verifyMigrationLedger(ctx, db, files); err != nil {
		return fmt.Errorf("migration ledger guard: %w", err)
	}
	return nil
}

// schemaInvariants are idempotent Go-side schema checks that run on
// EVERY boot. Use only for columns/indexes that a migration may have
// failed to apply on installs with corrupted schema_version state.
var schemaInvariants = []func(context.Context, *sql.DB) error{
	ensureWorkerRunsWorkspaceID,
	ensureWorkerRunGitResultColumns,
	ensureTaskStatusVocabKind,
	ensureTaskHLC,
	ensureTasksMetaJSONSchema,
	ensureSkillManifestExtra,
	ensureSkillRuns,
	ensureSkillRefinementProposals,
	ensureDownstreamCallTimeout,
	ensureWorkerCapabilityProfile,
	ensureWorkerExecuteScripts,
	ensureWorkerArchiveColumns,
	ensureWorkspaceLinkColumns,
	ensureWorkerMeshTriggerStatusCols,
	ensureCrmPerson,
	ensureWorkerWorkspaceAccess,
	ensureCompressionStats,
}

// ensureWorkerRunGitResultColumns heals branch-swapped or partially restored
// databases whose schema ledger advanced past the trusted-snapshot migration.
// It runs on every boot and is deliberately idempotent.
func ensureWorkerRunGitResultColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(worker_runs)`)
	if err != nil {
		return fmt.Errorf("pragma table_info worker_runs git result: %w", err)
	}
	tableExists := false
	have := map[string]bool{}
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan worker_runs git result pragma row: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate worker_runs git result pragma rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close worker_runs git result pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}
	adds := []struct{ col, ddl string }{
		{"result_branch", `ALTER TABLE worker_runs ADD COLUMN result_branch TEXT NOT NULL DEFAULT ''`},
		{"result_commit", `ALTER TABLE worker_runs ADD COLUMN result_commit TEXT NOT NULL DEFAULT ''`},
		{"result_changed", `ALTER TABLE worker_runs ADD COLUMN result_changed INTEGER NOT NULL DEFAULT 0`},
	}
	for _, add := range adds {
		if have[add.col] {
			continue
		}
		if _, err := db.ExecContext(ctx, add.ddl); err != nil {
			return fmt.Errorf("add worker_runs.%s column: %w", add.col, err)
		}
	}
	return nil
}

// postMigrationHooks runs extra Go-side fixups after a numbered migration is
// applied. Use sparingly — most schema work belongs in plain SQL files. The
// hook executes outside the migration's transaction so it can perform
// operations SQLite cannot express atomically (e.g. an ADD COLUMN that may
// be a no-op when the column already exists from an earlier branch).
var postMigrationHooks = map[int]func(context.Context, *sql.DB) error{
	24: ensureP2PConnectionMode,
	33: ensureP2PLastKnownAddrs,
	72: backfillTasksMetaJSON,
}

// ensureWorkspaceLinkColumns adds the link columns to
// workspace_peer_bindings when migration 088 either didn't apply or
// applied on a schema whose version was bumped past it (branch swaps,
// partially-restored backups). Idempotent — mirrors
// ensureP2PConnectionMode. Each column is added only when missing, so
// re-running on a healthy schema is a no-op.
func ensureWorkspaceLinkColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(workspace_peer_bindings)`)
	if err != nil {
		return fmt.Errorf("pragma table_info workspace_peer_bindings: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	have := map[string]bool{}
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil // table is created by migration 061; nothing to heal yet.
	}
	adds := []struct{ col, ddl string }{
		{"linked", `ALTER TABLE workspace_peer_bindings ADD COLUMN linked INTEGER NOT NULL DEFAULT 0`},
		{"link_established_by", `ALTER TABLE workspace_peer_bindings ADD COLUMN link_established_by TEXT NOT NULL DEFAULT ''`},
		{"link_established_at", `ALTER TABLE workspace_peer_bindings ADD COLUMN link_established_at INTEGER`},
	}
	for _, a := range adds {
		if have[a.col] {
			continue
		}
		if _, err := db.ExecContext(ctx, a.ddl); err != nil {
			return fmt.Errorf("add workspace_peer_bindings.%s column: %w", a.col, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_workspace_peer_bindings_linked
		   ON workspace_peer_bindings(local_workspace_id) WHERE linked = 1`,
	); err != nil {
		return fmt.Errorf("create workspace_peer_bindings linked index: %w", err)
	}
	return nil
}

// ensureWorkerMeshTriggerStatusCols is the SOLE adder of the
// status_from_match / status_to_match columns on worker_mesh_triggers.
// Migration 089 is a deliberate no-op marker (SQLite ADD COLUMN has no
// IF NOT EXISTS, so a bare ALTER there would crash on a DB that already
// carries the columns at version < 89). This invariant runs on every
// boot and adds each column only when missing, so it's safe on a fresh
// install, an upgrade, a branch swap, or a partially-restored backup.
// Mirrors ensureP2PConnectionMode (migration 024 + Go-side column add).
func ensureWorkerMeshTriggerStatusCols(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(worker_mesh_triggers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info worker_mesh_triggers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	have := map[string]bool{}
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil // table is created by migration 055; nothing to heal yet.
	}
	adds := []struct{ col, ddl string }{
		{"status_from_match", `ALTER TABLE worker_mesh_triggers ADD COLUMN status_from_match TEXT NOT NULL DEFAULT ''`},
		{"status_to_match", `ALTER TABLE worker_mesh_triggers ADD COLUMN status_to_match TEXT NOT NULL DEFAULT ''`},
	}
	for _, a := range adds {
		if have[a.col] {
			continue
		}
		if _, err := db.ExecContext(ctx, a.ddl); err != nil {
			return fmt.Errorf("add worker_mesh_triggers.%s column: %w", a.col, err)
		}
	}
	return nil
}

func ensureWorkerWorkspaceAccess(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS worker_workspace_access (
			worker_id    TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			access       TEXT NOT NULL CHECK(access IN ('read', 'write')),
			created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (worker_id, workspace_id)
		)`); err != nil {
		return fmt.Errorf("create worker_workspace_access: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_worker_workspace_access_workspace
			ON worker_workspace_access(workspace_id, access)`); err != nil {
		return fmt.Errorf("create worker_workspace_access workspace index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO worker_workspace_access (
			worker_id, workspace_id, access, created_at, updated_at
		)
		SELECT id, workspace_id, 'write', created_at, updated_at
		FROM workers
		WHERE workspace_id <> ''`); err != nil {
		return fmt.Errorf("backfill worker_workspace_access: %w", err)
	}
	return nil
}

// ensureP2PConnectionMode adds the connection_mode column to p2p_peers if
// (a) the table exists and (b) the column is missing. Idempotent.
//
// This is defensive about migration 024 ordering: when M1.2 has already
// created p2p_peers via its own earlier migration, our CREATE TABLE IF NOT
// EXISTS becomes a no-op and the new column would be missing without this
// hook. When migration 024 was the first thing to create the table, the
// column was already added inline and this hook is a no-op.
func ensureP2PConnectionMode(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(p2p_peers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info p2p_peers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	hasColumn := false
	tableExists := false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "connection_mode" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}
	if !hasColumn {
		_, err = db.ExecContext(ctx,
			`ALTER TABLE p2p_peers ADD COLUMN connection_mode TEXT`,
		)
		if err != nil {
			return fmt.Errorf("add connection_mode column: %w", err)
		}
	}
	_, err = db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_p2p_peers_connection_mode
		   ON p2p_peers(connection_mode)`,
	)
	if err != nil {
		return fmt.Errorf("create connection_mode index: %w", err)
	}
	return nil
}

// ensureP2PLastKnownAddrs adds the last_known_addrs column to p2p_peers if
// (a) the table exists and (b) the column is missing. Idempotent.
//
// Mirrors ensureP2PConnectionMode: the column has to be added by Go because
// `ALTER TABLE p2p_peers ADD COLUMN` would fail when M1.2/M1.3 already
// created the table on an upgrade path that skipped 033's CREATE.
func ensureP2PLastKnownAddrs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(p2p_peers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info p2p_peers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	hasColumn := false
	tableExists := false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "last_known_addrs" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	_, err = db.ExecContext(ctx,
		`ALTER TABLE p2p_peers ADD COLUMN last_known_addrs TEXT NOT NULL DEFAULT '[]'`,
	)
	if err != nil {
		return fmt.Errorf("add last_known_addrs column: %w", err)
	}
	return nil
}

// ensureWorkerRunsWorkspaceID adds workspace_id to worker_runs when the
// column is missing — guards against upgrades that bumped schema_version
// past migration 063 without actually applying its ALTER TABLE. Hits the
// list runs / get worker endpoints which reference workspace_id in
// SELECT (workerRunCols). Idempotent.
func ensureWorkerRunsWorkspaceID(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(worker_runs)`)
	if err != nil {
		return fmt.Errorf("pragma table_info worker_runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "workspace_id" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE worker_runs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return fmt.Errorf("add worker_runs.workspace_id column: %w", err)
	}
	// Backfill from the parent workers table by JOIN.
	if _, err := db.ExecContext(ctx, `
		UPDATE worker_runs SET workspace_id = (
			SELECT workers.workspace_id
			FROM workers
			WHERE workers.id = worker_runs.worker_id
		)
		WHERE workspace_id = '' AND EXISTS (
			SELECT 1 FROM workers WHERE workers.id = worker_runs.worker_id
		)`); err != nil {
		return fmt.Errorf("backfill worker_runs.workspace_id: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_worker_runs_workspace_started
			ON worker_runs(workspace_id, started_at DESC)`,
	); err != nil {
		return fmt.Errorf("create worker_runs workspace index: %w", err)
	}
	return nil
}

// ensureTaskStatusVocabKind adds the `kind` column to
// task_status_vocabulary when migration 070 either didn't apply or
// applied on a fresh database that never ran 061/062 (so the table
// doesn't exist yet). Idempotent. Mirrors ensureWorkerRunsWorkspaceID's
// pattern: detect-then-add via PRAGMA + ALTER, then re-seed the six
// suggested defaults so a self-healed install lands in the same state
// as a freshly-migrated one.
func ensureTaskStatusVocabKind(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(task_status_vocabulary)`)
	if err != nil {
		return fmt.Errorf("pragma table_info task_status_vocabulary: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "kind" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE task_status_vocabulary ADD COLUMN kind TEXT NOT NULL DEFAULT 'open'`,
	); err != nil {
		return fmt.Errorf("add task_status_vocabulary.kind column: %w", err)
	}
	// Seed the six suggested defaults so the self-heal lands in the
	// same state migration 070 would have produced.
	seeds := []struct{ status, kind string }{
		{"open", "open"},
		{"doing", "working"},
		{"blocked", "blocked"},
		// review is its own kind since migration 099 — awaiting
		// verification, NOT working (no lease) and NOT terminal.
		{"review", "review"},
		{"done", "done"},
		{"cancelled", "cancelled"},
	}
	for _, s := range seeds {
		if _, err := db.ExecContext(ctx,
			`UPDATE task_status_vocabulary SET kind = ? WHERE status_text = ?`,
			s.kind, s.status,
		); err != nil {
			return fmt.Errorf("seed task_status_vocabulary kind: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_task_status_vocab_kind
			ON task_status_vocabulary(workspace_id, kind)`,
	); err != nil {
		return fmt.Errorf("create task_status_vocab kind index: %w", err)
	}
	return nil
}

// ensureSkillManifestExtra adds the `manifest_extra` TEXT column to
// skill_registry_entries when migration 073 either didn't apply or
// applied on a fresh database that never ran 037 first (so the table
// doesn't exist yet). Idempotent. Mirrors ensureTaskStatusVocabKind:
// detect-then-add via PRAGMA + ALTER.
func ensureSkillManifestExtra(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(skill_registry_entries)`)
	if err != nil {
		return fmt.Errorf("pragma table_info skill_registry_entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "manifest_extra" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE skill_registry_entries ADD COLUMN manifest_extra TEXT NOT NULL DEFAULT '{}'`,
	); err != nil {
		return fmt.Errorf("add skill_registry_entries.manifest_extra column: %w", err)
	}
	return nil
}

// ensureSkillRuns creates the skill_runs table + its indexes when
// migration 074 either didn't apply or the file is missing on a
// partially-restored install. Idempotent CREATE-IF-NOT-EXISTS pattern;
// no PRAGMA detection needed because the entire table is the unit of
// concern, not a column ADD.
func ensureSkillRuns(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS skill_runs (
			id                TEXT PRIMARY KEY,
			skill_name        TEXT NOT NULL,
			skill_version     INTEGER NOT NULL,
			workspace_id      TEXT NOT NULL,
			started_at        TEXT NOT NULL,
			completed_at      TEXT,
			outcome           TEXT NOT NULL DEFAULT 'running',
			phases_json       TEXT NOT NULL DEFAULT '[]',
			tools_used_json   TEXT NOT NULL DEFAULT '[]',
			task_epic_id      TEXT,
			agent_session_id  TEXT,
			metadata_json     TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
		return fmt.Errorf("create skill_runs: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_skill_runs_skill_started
			ON skill_runs(skill_name, started_at DESC)`,
	); err != nil {
		return fmt.Errorf("create skill_runs skill index: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_skill_runs_workspace_started
			ON skill_runs(workspace_id, started_at DESC)`,
	); err != nil {
		return fmt.Errorf("create skill_runs workspace index: %w", err)
	}
	return nil
}

// ensureSkillRefinementProposals creates the skill_refinement_proposals
// table + its indexes when migration 075 either didn't apply or the
// file is missing on a partially-restored install. Idempotent CREATE-
// IF-NOT-EXISTS pattern; mirrors ensureSkillRuns since the entire
// table is the unit of concern (W3 doesn't ALTER existing tables).
func ensureSkillRefinementProposals(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS skill_refinement_proposals (
			id                     TEXT PRIMARY KEY,
			skill_name             TEXT NOT NULL,
			skill_version          INTEGER NOT NULL,
			friction               TEXT NOT NULL,
			suggested_change       TEXT NOT NULL,
			rationale              TEXT NOT NULL,
			proposed_by_session_id TEXT NOT NULL,
			proposed_by_peer_id    TEXT,
			workspace_id           TEXT NOT NULL,
			created_at             TEXT NOT NULL,
			status                 TEXT NOT NULL DEFAULT 'pending',
			candidate_at           TEXT,
			resolved_at            TEXT,
			resolved_by_session_id TEXT,
			resolution_note        TEXT,
			metadata_json          TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
		return fmt.Errorf("create skill_refinement_proposals: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_refinement_skill_status
			ON skill_refinement_proposals(skill_name, status, created_at DESC)`,
	); err != nil {
		return fmt.Errorf("create refinement skill/status index: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_refinement_workspace
			ON skill_refinement_proposals(workspace_id, created_at DESC)`,
	); err != nil {
		return fmt.Errorf("create refinement workspace index: %w", err)
	}
	return nil
}

// ensureDownstreamCallTimeout adds the call_timeout_sec column to
// downstream_servers when migration 085 either didn't apply or applied
// against a schema that was branched-past it. Mirrors the other
// ensure-column patterns: detect via PRAGMA, ALTER if missing.
// Idempotent.
func ensureDownstreamCallTimeout(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(downstream_servers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info downstream_servers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "call_timeout_sec" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE downstream_servers ADD COLUMN call_timeout_sec INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("add downstream_servers.call_timeout_sec: %w", err)
	}
	return nil
}

// ensureWorkerCapabilityProfile adds the capability_profile_json column to
// the workers table when migration 112 either didn't apply or schema_version
// raced past it (branch swaps, partially-restored backups). Idempotent —
// adds the column only when missing. Mirrors ensureDownstreamCallTimeout.
func ensureWorkerCapabilityProfile(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(workers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info workers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists, hasColumn := false, false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "capability_profile_json" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists || hasColumn {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE workers ADD COLUMN capability_profile_json TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return fmt.Errorf("add workers.capability_profile_json: %w", err)
	}
	return nil
}

// ensureCompressionStats creates the token-compression savings ledger table
// idempotently on every boot, covering installs whose numbered migration (126)
// failed to apply. See migrations/126_compression_stats.sql.
func ensureCompressionStats(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS compression_stats (
			workspace_id        TEXT    NOT NULL DEFAULT '',
			transform           TEXT    NOT NULL,
			day                 TEXT    NOT NULL,
			lossless            INTEGER NOT NULL DEFAULT 0,
			samples             INTEGER NOT NULL DEFAULT 0,
			changed             INTEGER NOT NULL DEFAULT 0,
			orig_bytes          INTEGER NOT NULL DEFAULT 0,
			would_save_bytes    INTEGER NOT NULL DEFAULT 0,
			would_save_tokens   INTEGER NOT NULL DEFAULT 0,
			applied             INTEGER NOT NULL DEFAULT 0,
			applied_save_bytes  INTEGER NOT NULL DEFAULT 0,
			applied_save_tokens INTEGER NOT NULL DEFAULT 0,
			updated_at          TEXT    NOT NULL,
			PRIMARY KEY (workspace_id, transform, day)
		)`); err != nil {
		return fmt.Errorf("ensure compression_stats table: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_compression_stats_day ON compression_stats(day)`,
	); err != nil {
		return fmt.Errorf("ensure compression_stats index: %w", err)
	}
	return nil
}

// ensureWorkerExecuteScripts adds the pre_execute_script / post_execute_script
// columns to the workers table when migration 125 either didn't apply or
// schema_version raced past it (branch swaps, partially-restored backups).
// Idempotent — adds each column only when missing. Mirrors
// ensureWorkerCapabilityProfile.
func ensureWorkerExecuteScripts(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(workers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info workers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	cols := map[string]bool{}
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}
	if !cols["pre_execute_script"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE workers ADD COLUMN pre_execute_script TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add workers.pre_execute_script: %w", err)
		}
	}
	if !cols["post_execute_script"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE workers ADD COLUMN post_execute_script TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add workers.post_execute_script: %w", err)
		}
	}
	return nil
}

// ensureWorkerArchiveColumns adds the archived_at / archived_reason columns
// and live-row partial unique index when migration 123 was skipped by a
// corrupted schema_version or branch swap. Idempotent.
func ensureWorkerArchiveColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(workers)`)
	if err != nil {
		return fmt.Errorf("pragma table_info workers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	cols := map[string]bool{}
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}
	if !cols["archived_at"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE workers ADD COLUMN archived_at DATETIME NULL`); err != nil {
			return fmt.Errorf("add workers.archived_at: %w", err)
		}
	}
	if !cols["archived_reason"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE workers ADD COLUMN archived_reason TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add workers.archived_reason: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_workers_workspace_name`); err != nil {
		return fmt.Errorf("drop old worker name index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_workers_workspace_name_live
		ON workers(workspace_id, name)
		WHERE archived_at IS NULL`); err != nil {
		return fmt.Errorf("create live worker name index: %w", err)
	}
	return nil
}

// ensureCrmPerson creates the crm_person table + its FTS5 mirror, triggers,
// and the person_entities companion when migration 094 either didn't apply or
// the file is missing on a partially-restored install / branch swap.
// Idempotent CREATE-IF-NOT-EXISTS pattern; mirrors ensureSkillRuns since the
// whole table family is the unit of concern (094 doesn't ALTER existing
// tables). FTS5 virtual tables + triggers also support IF NOT EXISTS.
func ensureCrmPerson(ctx context.Context, db *sql.DB) error {
	if err := ensureDefaultPersonWorkspace(ctx, db); err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS crm_person (
			id                   TEXT PRIMARY KEY,
			workspace_id         TEXT NOT NULL REFERENCES workspaces(id),
			name                 TEXT NOT NULL,
			email                TEXT NOT NULL DEFAULT '',
			phone                TEXT NOT NULL DEFAULT '',
			company              TEXT NOT NULL DEFAULT '',
			role                 TEXT NOT NULL DEFAULT '',
			tags_json            TEXT NOT NULL DEFAULT '[]',
			notes                TEXT NOT NULL DEFAULT '',
			source_kind          TEXT NOT NULL DEFAULT 'agent',
			source_session_id    TEXT,
			source_tool_call_id  TEXT,
			pinned               INTEGER NOT NULL DEFAULT 0,
			created_at           INTEGER NOT NULL,
			updated_at           INTEGER NOT NULL,
			deleted_at           INTEGER,
			UNIQUE(workspace_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crm_person_updated
			ON crm_person(updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_crm_person_workspace_updated
			ON crm_person(workspace_id, updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_crm_person_source_session
			ON crm_person(source_session_id)
			WHERE deleted_at IS NULL AND source_session_id IS NOT NULL`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS crm_person_fts USING fts5(
			name, email, phone, company, role, tags, notes,
			id UNINDEXED,
			tokenize='porter unicode61 remove_diacritics 2'
		)`,
		`CREATE TRIGGER IF NOT EXISTS crm_person_ai AFTER INSERT ON crm_person BEGIN
			INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
			VALUES (new.rowid, new.name, new.email, new.phone, new.company, new.role, new.tags_json, new.notes, new.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS crm_person_au AFTER UPDATE ON crm_person BEGIN
			DELETE FROM crm_person_fts WHERE rowid = old.rowid;
			INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
			VALUES (new.rowid, new.name, new.email, new.phone, new.company, new.role, new.tags_json, new.notes, new.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS crm_person_ad AFTER DELETE ON crm_person BEGIN
			DELETE FROM crm_person_fts WHERE rowid = old.rowid;
		END`,
		`CREATE TABLE IF NOT EXISTS person_entities (
			id              TEXT PRIMARY KEY,
			person_id       TEXT NOT NULL,
			entity_kind     TEXT NOT NULL,
			entity_id       TEXT NOT NULL,
			role            TEXT NOT NULL DEFAULT 'subject',
			created_at      INTEGER NOT NULL,
			created_by      TEXT,
			FOREIGN KEY (person_id) REFERENCES crm_person(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_person_entities_link
			ON person_entities(person_id, entity_kind, entity_id, role)`,
		`CREATE INDEX IF NOT EXISTS idx_person_entities_lookup
			ON person_entities(entity_kind, entity_id, person_id)`,
		`CREATE INDEX IF NOT EXISTS idx_person_entities_person
			ON person_entities(person_id)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("ensure crm_person: %w", err)
		}
	}
	if err := ensureCrmPersonWorkspaceScope(ctx, db); err != nil {
		return err
	}
	return nil
}

func ensureDefaultPersonWorkspace(ctx context.Context, db *sql.DB) error {
	withParent := `INSERT INTO workspaces (
			id, name, root_path, parent_id, tags, default_policy, source, created_at, updated_at
		)
		SELECT ?, ?, '', NULL, '["crm"]', 'allow', 'system', datetime('now'), datetime('now')
		WHERE NOT EXISTS (
			SELECT 1 FROM workspaces WHERE id = ? OR name = ?
		)`
	_, err := db.ExecContext(ctx, withParent,
		store.PersonDefaultWorkspaceID, store.PersonDefaultWorkspaceID,
		store.PersonDefaultWorkspaceID, store.PersonDefaultWorkspaceID,
	)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "parent_id") {
		return fmt.Errorf("ensure crm workspace: %w", err)
	}
	withoutParent := `INSERT INTO workspaces (
			id, name, root_path, tags, default_policy, source, created_at, updated_at
		)
		SELECT ?, ?, '', '["crm"]', 'allow', 'system', datetime('now'), datetime('now')
		WHERE NOT EXISTS (
			SELECT 1 FROM workspaces WHERE id = ? OR name = ?
		)`
	if _, err := db.ExecContext(ctx, withoutParent,
		store.PersonDefaultWorkspaceID, store.PersonDefaultWorkspaceID,
		store.PersonDefaultWorkspaceID, store.PersonDefaultWorkspaceID,
	); err != nil {
		return fmt.Errorf("ensure crm workspace: %w", err)
	}
	return nil
}

func ensureCrmPersonWorkspaceScope(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(crm_person)`)
	if err != nil {
		return fmt.Errorf("pragma table_info crm_person: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	tableExists := false
	hasWorkspaceID := false
	for rows.Next() {
		tableExists = true
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		if name == "workspace_id" {
			hasWorkspaceID = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma rows: %w", err)
	}
	if !tableExists {
		return nil
	}
	if hasWorkspaceID {
		if _, err := db.ExecContext(ctx, `
			UPDATE crm_person
			SET workspace_id = COALESCE(
				(SELECT id FROM workspaces WHERE name = 'crm' LIMIT 1),
				(SELECT id FROM workspaces WHERE id = 'crm' LIMIT 1),
				?
			)
			WHERE TRIM(COALESCE(workspace_id, '')) = '' OR workspace_id = 'global'`,
			store.PersonDefaultWorkspaceID,
		); err != nil {
			return fmt.Errorf("backfill crm_person.workspace_id: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_crm_person_workspace_updated
			ON crm_person(workspace_id, updated_at DESC) WHERE deleted_at IS NULL`); err != nil {
			return fmt.Errorf("ensure crm_person workspace index: %w", err)
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin crm_person workspace rebuild: %w", err)
	}
	for _, stmt := range crmPersonWorkspaceRebuildSQL {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("rebuild crm_person workspace scope: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit crm_person workspace rebuild: %w", err)
	}
	return nil
}

var crmPersonWorkspaceRebuildSQL = []string{
	`DROP TRIGGER IF EXISTS crm_person_ai`,
	`DROP TRIGGER IF EXISTS crm_person_au`,
	`DROP TRIGGER IF EXISTS crm_person_ad`,
	`DROP TABLE IF EXISTS crm_person_fts`,
	`DROP TABLE IF EXISTS person_entities_new`,
	`DROP TABLE IF EXISTS crm_person_new`,
	`CREATE TABLE crm_person_new (
		id                   TEXT PRIMARY KEY,
		workspace_id         TEXT NOT NULL REFERENCES workspaces(id),
		name                 TEXT NOT NULL,
		email                TEXT NOT NULL DEFAULT '',
		phone                TEXT NOT NULL DEFAULT '',
		company              TEXT NOT NULL DEFAULT '',
		role                 TEXT NOT NULL DEFAULT '',
		tags_json            TEXT NOT NULL DEFAULT '[]',
		notes                TEXT NOT NULL DEFAULT '',
		source_kind          TEXT NOT NULL DEFAULT 'agent',
		source_session_id    TEXT,
		source_tool_call_id  TEXT,
		pinned               INTEGER NOT NULL DEFAULT 0,
		created_at           INTEGER NOT NULL,
		updated_at           INTEGER NOT NULL,
		deleted_at           INTEGER,
		UNIQUE(workspace_id, name)
	)`,
	`INSERT INTO crm_person_new (
		id, workspace_id, name, email, phone, company, role,
		tags_json, notes, source_kind, source_session_id, source_tool_call_id,
		pinned, created_at, updated_at, deleted_at
	)
	SELECT
		id,
		COALESCE(
			(SELECT id FROM workspaces WHERE name = 'crm' LIMIT 1),
			(SELECT id FROM workspaces WHERE id = 'crm' LIMIT 1),
			'crm'
		),
		name, email, phone, company, role,
		tags_json, notes, source_kind, source_session_id, source_tool_call_id,
		pinned, created_at, updated_at, deleted_at
	FROM crm_person`,
	`CREATE TABLE person_entities_new (
		id              TEXT PRIMARY KEY,
		person_id       TEXT NOT NULL,
		entity_kind     TEXT NOT NULL,
		entity_id       TEXT NOT NULL,
		role            TEXT NOT NULL DEFAULT 'subject',
		created_at      INTEGER NOT NULL,
		created_by      TEXT,
		FOREIGN KEY (person_id) REFERENCES crm_person_new(id) ON DELETE CASCADE
	)`,
	`INSERT INTO person_entities_new (
		id, person_id, entity_kind, entity_id, role, created_at, created_by
	)
	SELECT id, person_id, entity_kind, entity_id, role, created_at, created_by
	FROM person_entities`,
	`DROP TABLE person_entities`,
	`DROP TABLE crm_person`,
	`ALTER TABLE crm_person_new RENAME TO crm_person`,
	`ALTER TABLE person_entities_new RENAME TO person_entities`,
	`CREATE INDEX idx_crm_person_updated
		ON crm_person(updated_at DESC) WHERE deleted_at IS NULL`,
	`CREATE INDEX idx_crm_person_workspace_updated
		ON crm_person(workspace_id, updated_at DESC) WHERE deleted_at IS NULL`,
	`CREATE INDEX idx_crm_person_source_session
		ON crm_person(source_session_id)
		WHERE deleted_at IS NULL AND source_session_id IS NOT NULL`,
	`CREATE VIRTUAL TABLE crm_person_fts USING fts5(
		name, email, phone, company, role, tags, notes,
		id UNINDEXED,
		tokenize='porter unicode61 remove_diacritics 2'
	)`,
	`CREATE TRIGGER crm_person_ai AFTER INSERT ON crm_person BEGIN
		INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
		VALUES (new.rowid, new.name, new.email, new.phone, new.company, new.role, new.tags_json, new.notes, new.id);
	END`,
	`CREATE TRIGGER crm_person_au AFTER UPDATE ON crm_person BEGIN
		DELETE FROM crm_person_fts WHERE rowid = old.rowid;
		INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
		VALUES (new.rowid, new.name, new.email, new.phone, new.company, new.role, new.tags_json, new.notes, new.id);
	END`,
	`CREATE TRIGGER crm_person_ad AFTER DELETE ON crm_person BEGIN
		DELETE FROM crm_person_fts WHERE rowid = old.rowid;
	END`,
	`INSERT INTO crm_person_fts(rowid, name, email, phone, company, role, tags, notes, id)
	SELECT rowid, name, email, phone, company, role, tags_json, notes, id
	FROM crm_person`,
	`CREATE UNIQUE INDEX uniq_person_entities_link
		ON person_entities(person_id, entity_kind, entity_id, role)`,
	`CREATE INDEX idx_person_entities_lookup
		ON person_entities(entity_kind, entity_id, person_id)`,
	`CREATE INDEX idx_person_entities_person
		ON person_entities(person_id)`,
}

func ensureSchemaTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS applied_migrations (
			version    INTEGER NOT NULL,
			filename   TEXT    NOT NULL,
			checksum   TEXT    NOT NULL DEFAULT '',
			applied_at TEXT    NOT NULL,
			PRIMARY KEY (version)
		)`)
	return err
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM applied_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	set := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		set[v] = true
	}
	return set, rows.Err()
}

// backfillAppliedMigrationsOnce copies every row from schema_version
// into applied_migrations with filename + checksum resolved from the
// embedded filesystem. Runs once on an existing install and is a no-op
// thereafter. On a clean install applied_migrations starts empty —
// each applyMigration fills it in-band.
func backfillAppliedMigrationsOnce(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_migrations`).Scan(&count); err != nil {
		return fmt.Errorf("count applied_migrations: %w", err)
	}
	if count > 0 {
		return nil
	}

	rows, err := db.QueryContext(ctx, `SELECT version, applied_at FROM schema_version ORDER BY version`)
	if err != nil {
		return fmt.Errorf("scan schema_version: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	files, err := listMigrations()
	if err != nil {
		return err
	}
	fileByVersion := map[int]migrationFile{}
	for _, f := range files {
		fileByVersion[f.version] = f
	}

	for rows.Next() {
		var ver int
		var at string
		if err := rows.Scan(&ver, &at); err != nil {
			return fmt.Errorf("scan schema_version row: %w", err)
		}
		fname := ""
		csum := ""
		if f, ok := fileByVersion[ver]; ok {
			fname = f.filename
			data, rerr := migrationsFS.ReadFile("migrations/" + f.filename)
			if rerr == nil {
				h := sha256.Sum256(data)
				csum = hex.EncodeToString(h[:])
			}
		}
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO applied_migrations (version, filename, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			ver, fname, csum, at,
		); err != nil {
			return fmt.Errorf("backfill applied_migrations %d: %w", ver, err)
		}
	}
	return rows.Err()
}

// verifyMigrationLedger guards against the silent-skip failure mode:
// a migration version that exists in schema_version (bumped the max
// watermark) but never actually ran. It panics at daemon startup
// rather than limping along with a half-migrated schema.
func verifyMigrationLedger(ctx context.Context, db *sql.DB, files []migrationFile) error {
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("read applied set: %w", err)
	}
	maxApplied := 0
	for v := range applied {
		if v > maxApplied {
			maxApplied = v
		}
	}
	for _, f := range files {
		if f.version <= maxApplied && !applied[f.version] {
			return fmt.Errorf(
				"migration %d (%s) was skipped — version <= max applied (%d) but not in applied_migrations ledger",
				f.version, f.filename, maxApplied,
			)
		}
	}
	return nil
}

type migrationFile struct {
	version  int
	filename string
}

func listMigrations() ([]migrationFile, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}

	var files []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var ver int
		if _, err := fmt.Sscanf(e.Name(), "%03d_", &ver); err != nil {
			continue
		}
		files = append(files, migrationFile{version: ver, filename: e.Name()})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})
	return files, nil
}

func applyMigration(ctx context.Context, db *sql.DB, f migrationFile) error {
	data, err := migrationsFS.ReadFile("migrations/" + f.filename)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.filename, err)
	}
	// Hash the migration file as it sits on disk right now. The
	// ledger row stores this SHA256 so verifyLedger() can detect
	// tampering / post-apply edits.
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, string(data)); err != nil {
		return fmt.Errorf("exec %s: %w", f.filename, err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
		f.version,
	)
	if err != nil {
		return fmt.Errorf("record schema_version: %w", err)
	}

	// Mirror the apply record into the per-migration ledger so
	// verifyLedger() can answer "was this migration actually
	// applied, and is its file unchanged?". Same transaction so
	// the two rows can never disagree.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO applied_migrations (version, filename, checksum, applied_at)
		 VALUES (?, ?, ?, datetime('now'))`,
		f.version, f.filename, checksum,
	)
	if err != nil {
		return fmt.Errorf("record ledger: %w", err)
	}

	return tx.Commit()
}
