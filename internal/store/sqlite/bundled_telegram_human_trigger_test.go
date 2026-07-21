package sqlite

import (
	"context"
	"testing"
)

func TestMigration110ScopesTelegramResponderTriggerToHuman(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := "2026-06-12T00:00:00Z"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workspaces (id, name, root_path, tags, default_policy, source, created_at, updated_at)
		VALUES ('ws-telegram', 'Telegram', '', '[]', 'allow', 'test', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO auth_scopes (id, name, type, redaction_hints, source, created_at, updated_at)
		VALUES ('scope-telegram', 'telegram-model-key', 'env', '[]', 'test', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert auth scope: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers (id, name, model_provider, model_id, secret_scope_id,
			prompt_template, schedule_spec, workspace_id, created_at, updated_at)
		VALUES ('wkr-telegram', 'telegram-responder', 'opencode_cli', 'm', 'scope-telegram',
			'p', 'manual', 'ws-telegram', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert worker: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO worker_mesh_triggers (
			id, worker_id, tag_match, enabled, throttle_seconds, max_chain_depth,
			created_at, updated_at
		) VALUES ('trig-telegram', 'wkr-telegram', 'telegram', 1, 2, 3, ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert trigger: %v", err)
	}

	body, err := migrationsFS.ReadFile("migrations/110_telegram_responder_human_trigger.sql")
	if err != nil {
		t.Fatalf("read migration 110: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("reapply migration 110: %v", err)
	}

	var tagMatch string
	err = db.QueryRowContext(ctx, `
		SELECT tag_match FROM worker_mesh_triggers WHERE id = 'trig-telegram'`).Scan(&tagMatch)
	if err != nil {
		t.Fatalf("query trigger: %v", err)
	}
	if tagMatch != "human" {
		t.Fatalf("tag_match = %q, want human", tagMatch)
	}
}
