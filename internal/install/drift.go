package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// ClaudeSettingsPath returns the absolute path to Claude Code's settings.json
// for this installer's home — the file the PreToolUse curl hook is written
// into by InstallClaudeCodeHooks. Exposed (vs. the unexported sibling
// `claudeSettingsPath`) so the dashboard's drift reconciler can re-check
// the same file without re-deriving the path.
func (h *HookInstaller) ClaudeSettingsPath() string {
	return h.claudeSettingsPath()
}

// Endpoint returns the URL substring that should appear inside any
// installed PreToolUse hook command. Used by the drift reconciler to
// decide whether settings.json still references mcplexer at all.
func (h *HookInstaller) Endpoint() string {
	return h.endpoint
}

// SessionEndpoint returns the URL substring that should appear inside any
// installed session-lifecycle (SessionStart/SessionEnd) hook command. Used
// by the drift reconciler alongside Endpoint so stripped session hooks are
// detected even when the PreToolUse hook is intact.
func (h *HookInstaller) SessionEndpoint() string {
	return h.sessionEndpoint()
}

// ClaudeSettingsReferencesEndpoint reports whether settingsPath is a
// Claude Code settings.json that still references the mcplexer hook
// endpoint inside at least one PreToolUse matcher entry.
//
// Return contract:
//   - (true, nil):   file exists, parses as a JSON object, and at least
//     one hooks.PreToolUse[*].hooks[*].command string
//     contains the endpoint substring.
//   - (false, nil):  file is missing, empty, or parsed cleanly but no
//     PreToolUse entry references the endpoint. Treated
//     by callers as drifted when the DB says installed=true.
//   - (false, err):  the file exists but read/parse failed (e.g.
//     permissions, corrupt JSON, non-object root).
//     Callers surface this as "drifted (unknown — parse
//     error)" rather than guessing.
//
// Pure: no filesystem mutation, no DB calls. Safe to call on every
// /api/v1/guards/shell GET if the caller wants — though guards_handler
// throttles in practice.
func ClaudeSettingsReferencesEndpoint(settingsPath, endpoint string) (bool, error) {
	if settingsPath == "" {
		return false, fmt.Errorf("settings path required")
	}
	if endpoint == "" {
		return false, fmt.Errorf("endpoint required")
	}
	cfg, existed, err := readJSONObject(settingsPath)
	if err != nil {
		return false, err
	}
	if !existed || len(cfg) == 0 {
		// Missing file = no reference. Caller treats this as drifted
		// when DB says hooks_installed=true.
		return false, nil
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	preList, _ := hooks["PreToolUse"].([]any)
	for _, entry := range preList {
		if hookEntryReferences(entry, endpoint) {
			return true, nil
		}
	}
	return false, nil
}

// ClaudeSettingsReferencesSessionEndpoint is the session-hook analogue of
// ClaudeSettingsReferencesEndpoint: it reports whether settingsPath still
// references sessionEndpoint under EVERY registered session event key
// (claudeSessionEvents — SessionStart + SessionEnd). The PreToolUse-only
// check above can't see stripped session hooks, so a user who removed the
// SessionStart/SessionEnd entries (but kept PreToolUse) would otherwise read
// as not-drifted. This closes that gap.
//
// Return contract mirrors ClaudeSettingsReferencesEndpoint:
//   - (true, nil):  file parses and EVERY session event key holds an entry
//     whose command references sessionEndpoint.
//   - (false, nil): file missing/empty, or at least one session event key is
//     missing or doesn't reference the endpoint (drifted).
//   - (false, err): the file exists but read/parse failed.
//
// Pure: no filesystem mutation, no DB calls.
func ClaudeSettingsReferencesSessionEndpoint(settingsPath, sessionEndpoint string) (bool, error) {
	if settingsPath == "" {
		return false, fmt.Errorf("settings path required")
	}
	if sessionEndpoint == "" {
		return false, fmt.Errorf("session endpoint required")
	}
	cfg, existed, err := readJSONObject(settingsPath)
	if err != nil {
		return false, err
	}
	if !existed || len(cfg) == 0 {
		return false, nil
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	// Every session event key must carry a referencing entry; a single
	// missing/stripped key counts as drift.
	for _, event := range claudeSessionEvents {
		list, _ := hooks[event].([]any)
		found := false
		for _, entry := range list {
			if hookEntryReferences(entry, sessionEndpoint) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// DefaultClaudeSettingsPath returns the same path layout that
// HookInstaller would use for `home`, without requiring a constructed
// installer. Useful for callers (rules sync, tests, diagnostics) that
// only want to point at the file.
func DefaultClaudeSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// fileExists is a small convenience for tests; not used by the helper
// above but co-located so the drift package stays self-contained.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
