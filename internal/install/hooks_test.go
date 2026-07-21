package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallClaudeCodeHooks_EmptyHome(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil receipt on first install")
	}
	if r.BackupPath != "" {
		t.Errorf("expected empty BackupPath on empty-home install, got %q", r.BackupPath)
	}
	if r.TargetPath != settings {
		t.Errorf("TargetPath=%q want %q", r.TargetPath, settings)
	}
	if r.Action != "write_file" {
		t.Errorf("Action=%q want write_file", r.Action)
	}

	cfg := readSettings(t, settings)
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks key missing after install")
	}
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse len=%d want 1", len(pre))
	}

	r2, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if r2 != nil {
		t.Errorf("expected nil receipt on idempotent re-install, got %+v", r2)
	}
	if got := len(fs.receipts); got != 1 {
		t.Errorf("receipts after idempotent install: %d want 1", got)
	}
}

func TestInstallClaudeCodeHooks_PreservesExistingSettings(t *testing.T) {
	inst, _, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	writeJSON(t, settings, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Edit",
					"hooks": []any{
						map[string]any{"type": "command", "command": "echo unrelated"},
					},
				},
			},
		},
	})

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r == nil || r.BackupPath == "" {
		t.Fatalf("expected receipt with backup path, got %+v", r)
	}
	if _, err := os.Stat(r.BackupPath); err != nil {
		t.Errorf("backup file should exist: %v", err)
	}

	cfg := readSettings(t, settings)
	if cfg["theme"] != "dark" {
		t.Errorf("theme key lost; got %v", cfg["theme"])
	}
	hooks := cfg["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("PostToolUse stanza lost")
	}
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse len=%d want 1", len(pre))
	}
	entry := pre[0].(map[string]any)
	if entry["matcher"] != "Bash" {
		t.Errorf("matcher=%v want Bash", entry["matcher"])
	}
	inner := entry["hooks"].([]any)[0].(map[string]any)
	if !strings.Contains(inner["command"].(string), DefaultHookEndpoint) {
		t.Errorf("command does not reference endpoint: %v", inner["command"])
	}
}

func TestInstallClaudeCodeHooks_AlreadyPresent(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	// Use the installer's *current* hookCommand verbatim so this exercises
	// the true idempotent path (endpoint matches AND command matches). The
	// session-lifecycle hooks must ALSO be present-and-current for the
	// install to be a no-op, so seed all of them.
	// Drift detection is covered in TestInstallClaudeCodeHooks_RewritesStaleCommand.
	hooks := map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "Bash",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": inst.hookCommand(),
					},
				},
			},
		},
	}
	for _, event := range claudeSessionEvents {
		hooks[event] = []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": inst.sessionHookCommand(),
					},
				},
			},
		}
	}
	writeJSON(t, settings, map[string]any{"hooks": hooks})

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil receipt when hook already present, got %+v", r)
	}
	if len(fs.receipts) != 0 {
		t.Errorf("no receipts should be recorded; got %d", len(fs.receipts))
	}
}

// TestInstallClaudeCodeHooks_RewritesStaleCommand verifies that an existing
// settings.json which has our endpoint but a stale command shape (e.g. the
// pre-fix `$CLAUDE_HOOK_INPUT` form that never actually delivered a body)
// gets rewritten in place on re-install. This is the path that lets users
// upgrade by re-running setup rather than hand-editing settings.json.
func TestInstallClaudeCodeHooks_RewritesStaleCommand(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	staleCommand := `curl -s -X POST -H 'Content-Type: application/json' -d "$CLAUDE_HOOK_INPUT" ` + DefaultHookEndpoint
	writeJSON(t, settings, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": staleCommand,
						},
					},
				},
			},
		},
	})

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil receipt when stale command was rewritten")
	}
	if r.BackupPath == "" {
		t.Error("expected backup when overwriting existing settings.json")
	}
	if len(fs.receipts) != 1 {
		t.Errorf("receipts after stale-rewrite: got %d want 1", len(fs.receipts))
	}

	cfg := readSettings(t, settings)
	if cfg["theme"] != "dark" {
		t.Errorf("unrelated theme key lost; got %v", cfg["theme"])
	}
	hooks := cfg["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse len=%d want 1 (rewrite-in-place, not append)", len(pre))
	}
	got := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if got != inst.hookCommand() {
		t.Errorf("command not rewritten:\n got:  %q\n want: %q", got, inst.hookCommand())
	}
	if strings.Contains(got, "CLAUDE_HOOK_INPUT") {
		t.Errorf("rewritten command still references CLAUDE_HOOK_INPUT: %q", got)
	}
}

func TestUninstallClaudeCodeHooks_RestoresBackup(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	original := map[string]any{"theme": "dark"}
	writeJSON(t, settings, original)

	if _, err := inst.InstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := inst.UninstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	cfg := readSettings(t, settings)
	if _, has := cfg["hooks"]; has {
		t.Errorf("hooks should have been removed; got %v", cfg)
	}
	if cfg["theme"] != "dark" {
		t.Errorf("theme key not restored: %v", cfg["theme"])
	}
	if fs.receipts[0].ReversedAt == nil {
		t.Error("receipt should be marked reversed")
	}
	if fs.receipts[0].ReverseError != "" {
		t.Errorf("unexpected reverse error: %q", fs.receipts[0].ReverseError)
	}
}

func TestUninstallClaudeCodeHooks_NoReceipts(t *testing.T) {
	inst, _, _ := newTestInstaller(t)
	if err := inst.UninstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("uninstall on empty store should be no-op: %v", err)
	}
}

func TestUninstallClaudeCodeHooks_DeletesWhenNoBackup(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r.BackupPath != "" {
		t.Fatalf("test invariant: empty-home install should produce no backup, got %q", r.BackupPath)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("settings should exist after install: %v", err)
	}

	if err := inst.UninstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("settings should be deleted after empty-home uninstall; got err=%v", err)
	}
	if fs.receipts[0].ReversedAt == nil {
		t.Error("receipt should be marked reversed")
	}
}

func TestNewHookInstaller_Validation(t *testing.T) {
	if _, err := NewHookInstaller("", newFakeStore(), ""); err == nil {
		t.Error("expected error for empty home")
	}
	if _, err := NewHookInstaller(t.TempDir(), nil, ""); err == nil {
		t.Error("expected error for nil store")
	}
	inst, err := NewHookInstaller(t.TempDir(), newFakeStore(), "")
	if err != nil {
		t.Fatalf("default endpoint constructor: %v", err)
	}
	if inst.endpoint != DefaultHookEndpoint {
		t.Errorf("endpoint=%q want %q", inst.endpoint, DefaultHookEndpoint)
	}
}
