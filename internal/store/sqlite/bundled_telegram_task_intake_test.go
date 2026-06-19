package sqlite

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestBundledTelegramTaskIntake verifies migration 109 publishes the
// create-only telegram-task-intake template with a deliberately narrow
// tool_allowlist (task__create only) and no output
// channels — the template never replies, delegates, or writes memory.
func TestBundledTelegramTaskIntake(t *testing.T) {
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
		WHERE name = 'telegram-task-intake' AND deleted_at IS NULL
		ORDER BY version DESC LIMIT 1`).Scan(&version, &body)
	if err != nil {
		t.Fatalf("query telegram-task-intake template: %v", err)
	}
	if version != 1 {
		t.Fatalf("latest telegram-task-intake version = %d, want 1", version)
	}

	var tpl struct {
		Name            string   `json:"name"`
		PromptTemplate  string   `json:"prompt_template"`
		ToolAllowlist   []string `json:"tool_allowlist"`
		ParameterSchema []struct {
			Name    string `json:"name"`
			Default string `json:"default"`
		} `json:"parameter_schema"`
		OutputChannelsHint []struct {
			Type string `json:"type"`
		} `json:"output_channels_hint"`
		ExecModeHint string `json:"exec_mode_hint"`
	}
	if err := json.Unmarshal([]byte(body), &tpl); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if tpl.Name != "telegram-task-intake" {
		t.Errorf("body name = %q, want telegram-task-intake", tpl.Name)
	}

	// NARROW ALLOWLIST: task__create only.
	// This is the core safety property — the intake must never be able
	// to reply, delegate, write memory, or touch any other surface.
	allowed := []string{"task__create"}
	if len(tpl.ToolAllowlist) != len(allowed) {
		t.Fatalf("tool_allowlist has %d entries, want %d: %v",
			len(tpl.ToolAllowlist), len(allowed), tpl.ToolAllowlist)
	}
	for _, tool := range allowed {
		if !slices.Contains(tpl.ToolAllowlist, tool) {
			t.Errorf("tool_allowlist missing required %q", tool)
		}
	}
	// Explicit deny-list: tools that must NEVER appear in intake allowlist.
	denied := []string{
		"telegram__send_message",
		"telegram__list_chats",
		"telegram__broadcast",
		"mcpx__delegate_worker",
		"mcpx__search_tools",
		"mcpx__list_delegations",
		"mcpx__review_delegation",
		"memory__save",
		"memory__list",
		"memory__recall",
		"mesh__send",
		"task__update",
		"task__append_note",
		"task__list",
		"task__get",
	}
	for _, tool := range denied {
		if slices.Contains(tpl.ToolAllowlist, tool) {
			t.Errorf("tool_allowlist contains denied tool %q — intake must not have this", tool)
		}
	}

	// No output channels: the template never sends replies.
	if len(tpl.OutputChannelsHint) != 0 {
		t.Errorf("output_channels_hint has %d entries, want 0 (intake never replies)",
			len(tpl.OutputChannelsHint))
	}

	// Prompt must instruct task creation only, not reply/delegate.
	if !strings.Contains(tpl.PromptTemplate, "task__create") {
		t.Error("prompt_template should reference task__create")
	}
	if !strings.Contains(tpl.PromptTemplate, "{trigger_content}") {
		t.Error("prompt_template should include fenced trigger_content")
	}
	if !strings.Contains(tpl.PromptTemplate, "{trigger_content_raw}") {
		t.Error("prompt_template should include trigger_content_raw for verbatim task text")
	}
	if !strings.Contains(tpl.PromptTemplate, "{target_workspace_id}") {
		t.Error("prompt_template should include target_workspace_id parameter")
	}
	if strings.Contains(tpl.PromptTemplate, "discover via mcpx__search_tools") {
		t.Error("prompt_template must not suggest workspace discovery via mcpx__search_tools")
	}
	// Check for positive call instructions for denied tools. The prompt
	// may mention denied tools in "DO NOT" constraints (which is fine),
	// but must never contain "Call <denied_tool>" as a positive action.
	positiveCallPatterns := []string{
		"Call telegram__send_message",
		"Call mesh__send",
		"Call memory__save",
		"Call mcpx__delegate_worker",
		"Call mcpx__search_tools",
		"Call task__update",
		"Call task__append_note",
	}
	for _, pattern := range positiveCallPatterns {
		if strings.Contains(tpl.PromptTemplate, pattern) {
			t.Errorf("prompt_template contains positive call %q — intake must never instruct this", pattern)
		}
	}
	if len(tpl.ParameterSchema) != 1 {
		t.Fatalf("parameter_schema has %d entries, want target_workspace_id only", len(tpl.ParameterSchema))
	}
	if tpl.ParameterSchema[0].Name != "target_workspace_id" {
		t.Fatalf("parameter_schema[0].name = %q, want target_workspace_id", tpl.ParameterSchema[0].Name)
	}
	if tpl.ParameterSchema[0].Default != "" {
		t.Errorf("target_workspace_id default = %q, want blank current-workspace default", tpl.ParameterSchema[0].Default)
	}

	// Not autonomous — should only fire on mesh trigger, not on a cron.
	if tpl.ExecModeHint != "" && tpl.ExecModeHint != "propose" {
		t.Errorf("exec_mode_hint = %q, want empty or propose (not autonomous)", tpl.ExecModeHint)
	}
}

// TestBundledTelegramResponderUnchangedAfter109 confirms that the
// existing telegram-responder template (v2) is not mutated by migration
// 109. This guards against accidental cross-template contamination.
func TestBundledTelegramResponderUnchangedAfter109(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Snapshot the responder's state after all migrations (including 109).
	var version int
	var body string
	var contentHash string
	err := db.QueryRowContext(ctx, `
		SELECT version, body, content_hash FROM worker_templates
		WHERE name = 'telegram-responder' AND deleted_at IS NULL
		ORDER BY version DESC LIMIT 1`).Scan(&version, &body, &contentHash)
	if err != nil {
		t.Fatalf("query telegram-responder: %v", err)
	}
	if version != 2 {
		t.Fatalf("telegram-responder version = %d, want 2 (unchanged)", version)
	}
	if !strings.HasPrefix(contentHash, "bundled-builtin-telegram-responder-v2") {
		t.Errorf("content_hash = %q, expected bundled-builtin-telegram-responder-v2-*", contentHash)
	}

	// Verify the responder still has its delegation tools.
	var tpl struct {
		ToolAllowlist []string `json:"tool_allowlist"`
	}
	if err := json.Unmarshal([]byte(body), &tpl); err != nil {
		t.Fatalf("responder body JSON parse: %v", err)
	}
	mustHave := []string{
		"telegram__send_message",
		"mcpx__delegate_worker",
		"mcpx__search_tools",
	}
	for _, tool := range mustHave {
		if !slices.Contains(tpl.ToolAllowlist, tool) {
			t.Errorf("telegram-responder v2 lost tool %q after migration 109", tool)
		}
	}
}
