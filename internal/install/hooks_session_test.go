package install

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// sessionEventEntries returns the [{hooks:[{type,command}]}] list stored
// under one Claude Code session event key, failing the test if the shape
// is not the expected matcher-less session-hook layout.
func sessionEventEntries(t *testing.T, cfg map[string]any, event string) []any {
	t.Helper()
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks key missing or not an object: %v", cfg["hooks"])
	}
	list, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("%s key missing or not a list: %v", event, hooks[event])
	}
	return list
}

// firstSessionCommand returns the command string of the first hook under a
// session event key. Session entries carry no "matcher", so this also
// asserts that invariant.
func firstSessionCommand(t *testing.T, cfg map[string]any, event string) string {
	t.Helper()
	list := sessionEventEntries(t, cfg, event)
	if len(list) == 0 {
		t.Fatalf("%s has no entries", event)
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("%s[0] not an object: %v", event, list[0])
	}
	if _, hasMatcher := entry["matcher"]; hasMatcher {
		t.Errorf("%s entry should not carry a matcher key: %v", event, entry)
	}
	inner, ok := entry["hooks"].([]any)
	if !ok || len(inner) == 0 {
		t.Fatalf("%s[0].hooks missing: %v", event, entry["hooks"])
	}
	hookObj, ok := inner[0].(map[string]any)
	if !ok {
		t.Fatalf("%s[0].hooks[0] not an object: %v", event, inner[0])
	}
	cmd, _ := hookObj["command"].(string)
	return cmd
}

// TestInstallClaudeCodeHooks_WritesSessionHooks verifies the install pass
// writes a SessionStart, SessionEnd, and Stop hook, each pointing at
// /v1/hooks/session, alongside the PreToolUse hook.
func TestInstallClaudeCodeHooks_WritesSessionHooks(t *testing.T) {
	inst, _, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	if _, err := inst.InstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	wantEndpoint := inst.sessionEndpoint()
	if !strings.HasSuffix(wantEndpoint, sessionPathSuffix) {
		t.Fatalf("session endpoint %q does not end in %q", wantEndpoint, sessionPathSuffix)
	}
	if strings.Contains(wantEndpoint, pretoolPathSuffix) {
		t.Errorf("session endpoint must not still reference the pretool path: %q", wantEndpoint)
	}

	cfg := readSettings(t, settings)
	for _, event := range claudeSessionEvents {
		list := sessionEventEntries(t, cfg, event)
		if len(list) != 1 {
			t.Fatalf("%s entries=%d want 1", event, len(list))
		}
		cmd := firstSessionCommand(t, cfg, event)
		if !strings.Contains(cmd, wantEndpoint) {
			t.Errorf("%s command %q does not reference session endpoint %q", event, cmd, wantEndpoint)
		}
		if strings.Contains(cmd, pretoolPathSuffix) {
			t.Errorf("%s command %q wrongly references the pretool endpoint", event, cmd)
		}
	}
}

// TestInstallClaudeCodeHooks_SessionHooksIdempotent verifies a re-install
// neither duplicates session entries nor records a second receipt once the
// PreToolUse and all session hooks are already current.
func TestInstallClaudeCodeHooks_SessionHooksIdempotent(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	if _, err := inst.InstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(fs.receipts) != 1 {
		t.Fatalf("receipts after first install=%d want 1", len(fs.receipts))
	}

	r2, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if r2 != nil {
		t.Errorf("expected nil receipt on idempotent re-install, got %+v", r2)
	}
	if len(fs.receipts) != 1 {
		t.Errorf("idempotent re-install recorded extra receipt: got %d want 1", len(fs.receipts))
	}

	cfg := readSettings(t, settings)
	for _, event := range claudeSessionEvents {
		if list := sessionEventEntries(t, cfg, event); len(list) != 1 {
			t.Errorf("%s entries=%d after re-install want 1 (no duplication)", event, len(list))
		}
	}
}

// TestInstallClaudeCodeHooks_SessionHooksPreserveUserEntries verifies that
// pre-existing user-authored session hooks (and unrelated settings) survive
// the install — we merge our entry, never clobber the list.
func TestInstallClaudeCodeHooks_SessionHooksPreserveUserEntries(t *testing.T) {
	inst, _, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	userCmd := "echo user-session-start"
	writeJSON(t, settings, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": userCmd},
					},
				},
			},
		},
	})

	if _, err := inst.InstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	cfg := readSettings(t, settings)
	if cfg["theme"] != "dark" {
		t.Errorf("unrelated theme key lost: %v", cfg["theme"])
	}

	start := sessionEventEntries(t, cfg, "SessionStart")
	if len(start) != 2 {
		t.Fatalf("SessionStart entries=%d want 2 (user + mcplexer)", len(start))
	}
	var sawUser, sawMcplexer bool
	for _, e := range start {
		entry := e.(map[string]any)
		inner := entry["hooks"].([]any)[0].(map[string]any)
		cmd := inner["command"].(string)
		switch {
		case cmd == userCmd:
			sawUser = true
		case strings.Contains(cmd, inst.sessionEndpoint()):
			sawMcplexer = true
		}
	}
	if !sawUser {
		t.Error("user-authored SessionStart hook was dropped")
	}
	if !sawMcplexer {
		t.Error("mcplexer SessionStart hook was not added")
	}

	// The events the user did NOT pre-populate still get our hook.
	for _, event := range []string{"SessionEnd"} {
		if list := sessionEventEntries(t, cfg, event); len(list) != 1 {
			t.Errorf("%s entries=%d want 1", event, len(list))
		}
	}
	// HIGH-2 regression: "Stop" is a per-turn event and must NOT be installed
	// (it would flood the capture nudge every turn). We never register it.
	if list, ok := cfg["hooks"].(map[string]any)["Stop"]; ok && list != nil {
		t.Errorf("Stop must NOT be installed (per-turn flood), got %v", list)
	}
}

// TestInstallClaudeCodeHooks_RewritesStaleSessionCommand verifies that a
// settings.json which already references the session endpoint but with a
// stale command shape gets rewritten in place on re-install (not appended),
// matching the PreToolUse drift-rewrite contract.
func TestInstallClaudeCodeHooks_RewritesStaleSessionCommand(t *testing.T) {
	inst, fs, home := newTestInstaller(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	sessionEP := inst.sessionEndpoint()
	staleCmd := `curl -s -X POST -H 'Content-Type: application/json' -d "$CLAUDE_HOOK_INPUT" ` + sessionEP

	// Seed a fully-current PreToolUse hook so the only drift is in the
	// session command — this isolates the session rewrite path.
	hooks := map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "Bash",
				"hooks": []any{
					map[string]any{"type": "command", "command": inst.hookCommand()},
				},
			},
		},
	}
	for _, event := range claudeSessionEvents {
		hooks[event] = []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "command": staleCmd},
				},
			},
		}
	}
	writeJSON(t, settings, map[string]any{"hooks": hooks})

	r, err := inst.InstallClaudeCodeHooks(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil receipt when stale session command was rewritten")
	}
	if len(fs.receipts) != 1 {
		t.Errorf("receipts after stale-session rewrite=%d want 1", len(fs.receipts))
	}

	cfg := readSettings(t, settings)
	for _, event := range claudeSessionEvents {
		list := sessionEventEntries(t, cfg, event)
		if len(list) != 1 {
			t.Fatalf("%s entries=%d want 1 (rewrite-in-place, not append)", event, len(list))
		}
		got := firstSessionCommand(t, cfg, event)
		if got != inst.sessionHookCommand() {
			t.Errorf("%s command not rewritten:\n got:  %q\n want: %q", event, got, inst.sessionHookCommand())
		}
		if strings.Contains(got, "CLAUDE_HOOK_INPUT") {
			t.Errorf("%s rewritten command still references CLAUDE_HOOK_INPUT: %q", event, got)
		}
	}
}

// TestSessionEndpointDerivation pins how the session URL is derived from a
// custom pretool endpoint so a non-default daemon host:port (passed by
// serve.go) is preserved for both hooks.
func TestSessionEndpointDerivation(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{
			name:     "default endpoint",
			endpoint: DefaultHookEndpoint,
			want:     "http://127.0.0.1:3333/v1/hooks/session",
		},
		{
			name:     "custom port",
			endpoint: "http://127.0.0.1:4555/v1/hooks/pretool",
			want:     "http://127.0.0.1:4555/v1/hooks/session",
		},
		{
			name:     "custom host and port",
			endpoint: "http://localhost:9090/v1/hooks/pretool",
			want:     "http://localhost:9090/v1/hooks/session",
		},
		{
			// Non-standard endpoint (no /v1/hooks/pretool suffix): net/url
			// path-swap must replace only the path, never double-slash.
			name:     "non-suffix endpoint path swapped",
			endpoint: "http://127.0.0.1:8080/custom/hook",
			want:     "http://127.0.0.1:8080/v1/hooks/session",
		},
		{
			// Trailing slash + query must not leak into the session URL.
			name:     "endpoint with trailing slash and query",
			endpoint: "http://127.0.0.1:8080/some/path/?token=abc",
			want:     "http://127.0.0.1:8080/v1/hooks/session",
		},
		{
			// Bare host with no path falls to the parse branch cleanly.
			name:     "bare host no path",
			endpoint: "http://example.com",
			want:     "http://example.com/v1/hooks/session",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewHookInstaller(t.TempDir(), newFakeStore(), tc.endpoint)
			if err != nil {
				t.Fatalf("NewHookInstaller: %v", err)
			}
			if got := inst.sessionEndpoint(); got != tc.want {
				t.Errorf("sessionEndpoint()=%q want %q", got, tc.want)
			}
		})
	}
}
