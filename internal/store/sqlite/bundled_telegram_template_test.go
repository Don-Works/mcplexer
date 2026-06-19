package sqlite

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestBundledTelegramResponderV2 verifies migration 098 publishes the
// delegation-capable v2 of the telegram-responder template with a body
// the install flow can parse, and that the allowlist actually carries
// the delegation + lmstudio tools the v2 prompt instructs the worker
// to use.
func TestBundledTelegramResponderV2(t *testing.T) {
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
		WHERE name = 'telegram-responder' AND deleted_at IS NULL
		ORDER BY version DESC LIMIT 1`).Scan(&version, &body)
	if err != nil {
		t.Fatalf("query telegram-responder template: %v", err)
	}
	if version != 2 {
		t.Fatalf("latest telegram-responder version = %d, want 2", version)
	}

	var tpl struct {
		Name               string   `json:"name"`
		PromptTemplate     string   `json:"prompt_template"`
		ToolAllowlist      []string `json:"tool_allowlist"`
		OutputChannelsHint []struct {
			Type           string `json:"type"`
			Priority       string `json:"priority"`
			Tags           string `json:"tags"`
			NotifyUser     bool   `json:"notify_user"`
			ReplyToTrigger bool   `json:"reply_to_trigger"`
		} `json:"output_channels_hint"`
	}
	if err := json.Unmarshal([]byte(body), &tpl); err != nil {
		t.Fatalf("v2 body is not valid JSON: %v", err)
	}
	if tpl.Name != "telegram-responder" {
		t.Errorf("body name = %q", tpl.Name)
	}
	for _, tool := range []string{
		"telegram__send_message",
		"mcpx__delegate_worker",
		"mcpx__list_delegations",
		"mcpx__list_delegation_model_capacity",
		"lmstudio__status",
		"lmstudio__start_server",
		"lmstudio__load_model",
	} {
		if !slices.Contains(tpl.ToolAllowlist, tool) {
			t.Errorf("v2 tool_allowlist missing %q", tool)
		}
	}
	if tpl.PromptTemplate == "" {
		t.Error("v2 prompt_template is empty")
	}
	if !strings.Contains(tpl.PromptTemplate, "mcpx__execute_code") || !strings.Contains(tpl.PromptTemplate, "search_tools") {
		t.Error("v2 prompt should instruct use of mcpx__search_tools + mcpx__execute_code for mcpx__/lmstudio delegation (slim worker surface)")
	}
	if len(tpl.OutputChannelsHint) != 1 {
		t.Fatalf("output_channels_hint len = %d, want 1", len(tpl.OutputChannelsHint))
	}
	ch := tpl.OutputChannelsHint[0]
	if ch.Type != "mesh" || ch.Priority != "normal" || ch.Tags != "telegram" || !ch.NotifyUser || !ch.ReplyToTrigger {
		t.Fatalf("telegram output channel = %+v, want mesh normal tags=telegram notify_user reply_to_trigger", ch)
	}
}

func TestMigration108RepairsSimpleTelegramResponderWorker(t *testing.T) {
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
		INSERT INTO workers (
			id, name, description, model_provider, model_id, secret_scope_id,
			prompt_template, schedule_spec, output_channels_json, workspace_id,
			created_at, updated_at
		)
		VALUES (
			'wkr-telegram', 'telegram-responder', '', 'opencode_cli',
			'minimax/MiniMax-M2.7-highspeed', 'scope-telegram',
			'reply', 'manual', '[{"type":"mesh","priority":"high"}]',
			'ws-telegram', ?, ?
		)`, now, now); err != nil {
		t.Fatalf("insert worker: %v", err)
	}

	body, err := migrationsFS.ReadFile("migrations/108_telegram_responder_reply_output.sql")
	if err != nil {
		t.Fatalf("read migration 108: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("reapply migration 108: %v", err)
	}

	var priority, tags string
	var notify, reply int
	err = db.QueryRowContext(ctx, `
		SELECT
			json_extract(output_channels_json, '$[0].priority'),
			json_extract(output_channels_json, '$[0].tags'),
			json_extract(output_channels_json, '$[0].notify_user'),
			json_extract(output_channels_json, '$[0].reply_to_trigger')
		FROM workers WHERE id = 'wkr-telegram'`).Scan(&priority, &tags, &notify, &reply)
	if err != nil {
		t.Fatalf("query repaired worker: %v", err)
	}
	if priority != "high" || tags != "telegram" || notify != 1 || reply != 1 {
		t.Fatalf("repaired channel priority=%q tags=%q notify=%d reply=%d; want high telegram 1 1",
			priority, tags, notify, reply)
	}
}
