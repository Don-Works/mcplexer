package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestMigration113TelegramResponderHistoryTemplate verifies migration 113
// patches the bundled telegram-responder v2 catalog row so fresh installs get
// conversation history: the prompt renders {mesh_history}, and the
// parameter_schema carries mesh_history_count / mesh_history_tags defaults that
// flow into a freshly-installed worker's parameters_json (the map the runner
// reads via historyCount/historyTags).
func TestMigration113TelegramResponderHistoryTemplate(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var version int
	var body string
	err := db.QueryRowContext(ctx, `
		SELECT version, body FROM worker_templates
		WHERE id = 'template-bundled-telegram-responder-v2'
		  AND name = 'telegram-responder' AND deleted_at IS NULL
		ORDER BY version DESC LIMIT 1`).Scan(&version, &body)
	if err != nil {
		t.Fatalf("query telegram-responder template: %v", err)
	}
	if version != 2 {
		t.Fatalf("latest telegram-responder version = %d, want 2", version)
	}

	var tpl struct {
		PromptTemplate  string `json:"prompt_template"`
		ParameterSchema []struct {
			Name    string `json:"name"`
			Default string `json:"default"`
		} `json:"parameter_schema"`
	}
	if err := json.Unmarshal([]byte(body), &tpl); err != nil {
		t.Fatalf("v2 body is not valid JSON: %v", err)
	}
	if !strings.Contains(tpl.PromptTemplate, "{mesh_history}") {
		t.Errorf("prompt_template missing {mesh_history} placeholder; got:\n%s", tpl.PromptTemplate)
	}
	defaults := map[string]string{}
	for _, p := range tpl.ParameterSchema {
		defaults[p.Name] = p.Default
	}
	if defaults["mesh_history_count"] != "12" {
		t.Errorf("mesh_history_count default = %q, want 12", defaults["mesh_history_count"])
	}
	if defaults["mesh_history_tags"] != "telegram" {
		t.Errorf("mesh_history_tags default = %q, want telegram", defaults["mesh_history_tags"])
	}

	// Idempotent re-apply must not duplicate the {mesh_history} block.
	mig, err := migrationsFS.ReadFile("migrations/113_telegram_responder_history.sql")
	if err != nil {
		t.Fatalf("read migration 113: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(mig)); err != nil {
		t.Fatalf("reapply migration 113: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT body FROM worker_templates
		WHERE id = 'template-bundled-telegram-responder-v2'`).Scan(&body); err != nil {
		t.Fatalf("re-query template: %v", err)
	}
	if err := json.Unmarshal([]byte(body), &tpl); err != nil {
		t.Fatalf("body invalid after re-apply: %v", err)
	}
	if n := strings.Count(tpl.PromptTemplate, "{mesh_history}"); n != 1 {
		t.Errorf("{mesh_history} appears %d times after re-apply, want 1 (idempotent)", n)
	}
}

// TestMigration113RepairsInstalledTelegramResponderWorker verifies migration
// 113's installed-worker UPDATE patches a live telegram-responder row: it sets
// the two runtime history params the runner reads and prepends {mesh_history}
// to the stored prompt_template.
func TestMigration113RepairsInstalledTelegramResponderWorker(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := "2026-06-17T00:00:00Z"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workspaces (id, name, root_path, tags, default_policy, source, created_at, updated_at)
		VALUES ('ws-tg-hist', 'Telegram', '', '[]', 'allow', 'test', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO auth_scopes (id, name, type, redaction_hints, source, created_at, updated_at)
		VALUES ('scope-tg-hist', 'telegram-model-key', 'env', '[]', 'test', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert auth scope: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers (
			id, name, description, model_provider, model_id, secret_scope_id,
			prompt_template, parameters_json, schedule_spec, output_channels_json,
			workspace_id, created_at, updated_at
		)
		VALUES (
			'wkr-tg-hist', 'telegram-responder', '', 'anthropic',
			'claude-haiku-4-5', 'scope-tg-hist',
			'Reply to the user.', '{"existing":"keep"}', 'manual',
			'[{"type":"mesh","priority":"normal"}]',
			'ws-tg-hist', ?, ?
		)`, now, now); err != nil {
		t.Fatalf("insert worker: %v", err)
	}

	mig, err := migrationsFS.ReadFile("migrations/113_telegram_responder_history.sql")
	if err != nil {
		t.Fatalf("read migration 113: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(mig)); err != nil {
		t.Fatalf("reapply migration 113: %v", err)
	}

	var params, prompt string
	err = db.QueryRowContext(ctx, `
		SELECT parameters_json, prompt_template FROM workers WHERE id = 'wkr-tg-hist'`).
		Scan(&params, &prompt)
	if err != nil {
		t.Fatalf("query repaired worker: %v", err)
	}

	var p map[string]any
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		t.Fatalf("parameters_json invalid: %v", err)
	}
	if p["mesh_history_count"] != "12" {
		t.Errorf("mesh_history_count = %v, want \"12\"", p["mesh_history_count"])
	}
	if p["mesh_history_tags"] != "telegram" {
		t.Errorf("mesh_history_tags = %v, want \"telegram\"", p["mesh_history_tags"])
	}
	if p["existing"] != "keep" {
		t.Errorf("pre-existing param dropped: existing = %v, want keep", p["existing"])
	}
	if !strings.Contains(prompt, "{mesh_history}") {
		t.Errorf("prompt_template missing {mesh_history}; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Reply to the user.") {
		t.Errorf("original prompt body lost; got:\n%s", prompt)
	}

	// Re-apply must be idempotent on the installed worker too.
	if _, err := db.ExecContext(ctx, string(mig)); err != nil {
		t.Fatalf("second reapply: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT prompt_template FROM workers WHERE id = 'wkr-tg-hist'`).Scan(&prompt); err != nil {
		t.Fatalf("re-query worker: %v", err)
	}
	if n := strings.Count(prompt, "{mesh_history}"); n != 1 {
		t.Errorf("{mesh_history} appears %d times after re-apply, want 1", n)
	}
}
