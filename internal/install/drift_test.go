package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeSettingsReferencesEndpoint walks the three return shapes of
// the drift helper: (a) endpoint present → true,nil; (b) endpoint
// absent → false,nil; (c) settings.json corrupt → false,err.
// Missing file is also (false, nil) because the dashboard treats
// "installed=true but file gone" identically to "installed=true but
// entry stripped" — both render as drifted.
func TestClaudeSettingsReferencesEndpoint(t *testing.T) {
	const endpoint = "http://127.0.0.1:3333/v1/hooks/pretool"

	tests := []struct {
		name        string
		writeBody   string // empty => don't write the file
		wantRef     bool
		wantErr     bool
		errContains string
	}{
		{
			name: "endpoint referenced",
			writeBody: `{
				"hooks": {
					"PreToolUse": [
						{"matcher":"Bash","hooks":[
							{"type":"command","command":"curl -s ` + endpoint + `"}
						]}
					]
				}
			}`,
			wantRef: true,
		},
		{
			name: "no hooks block",
			writeBody: `{
				"theme": "dark"
			}`,
			wantRef: false,
		},
		{
			name: "PreToolUse exists but unrelated",
			writeBody: `{
				"hooks": {
					"PreToolUse": [
						{"matcher":"Bash","hooks":[
							{"type":"command","command":"curl http://other.example/hook"}
						]}
					]
				}
			}`,
			wantRef: false,
		},
		{
			name:      "missing file",
			writeBody: "",
			wantRef:   false,
		},
		{
			name:        "corrupt json",
			writeBody:   "{not valid json",
			wantErr:     true,
			errContains: "parse",
		},
		{
			name:      "empty file",
			writeBody: "",
			wantRef:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if tc.writeBody != "" {
				if err := os.WriteFile(path, []byte(tc.writeBody), 0600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			got, err := ClaudeSettingsReferencesEndpoint(path, endpoint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (ref=%v)", got)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error: want substring %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantRef {
				t.Fatalf("ref: want %v, got %v", tc.wantRef, got)
			}
		})
	}
}

// TestClaudeSettingsReferencesEndpoint_BadArgs covers the input
// validation: empty path or empty endpoint is a programmer error,
// surface it as a hard error rather than silently returning false.
func TestClaudeSettingsReferencesEndpoint_BadArgs(t *testing.T) {
	if _, err := ClaudeSettingsReferencesEndpoint("", "x"); err == nil {
		t.Fatalf("empty path: want error, got nil")
	}
	if _, err := ClaudeSettingsReferencesEndpoint("/tmp/foo", ""); err == nil {
		t.Fatalf("empty endpoint: want error, got nil")
	}
}

// TestClaudeSettingsReferencesSessionEndpoint covers the session-hook drift
// helper: it must report referenced=true ONLY when every session event key
// (claudeSessionEvents) holds an entry referencing the session endpoint, and
// false when any one of them is missing/stripped — which is exactly the
// stripped-session-hook case the PreToolUse-only check could not see.
func TestClaudeSettingsReferencesSessionEndpoint(t *testing.T) {
	const sessionEP = "http://127.0.0.1:3333/v1/hooks/session"
	entry := func() string {
		return `{"hooks":[{"type":"command","command":"curl -s ` + sessionEP + `"}]}`
	}
	allPresent := `{
		"hooks": {
			"SessionStart": [` + entry() + `],
			"SessionEnd": [` + entry() + `]
		}
	}`
	// SessionEnd stripped: PreToolUse-only drift detection would miss this.
	startOnly := `{
		"hooks": {
			"SessionStart": [` + entry() + `]
		}
	}`
	unrelated := `{
		"hooks": {
			"SessionStart": [{"hooks":[{"type":"command","command":"curl http://other/x"}]}],
			"SessionEnd": [{"hooks":[{"type":"command","command":"curl http://other/x"}]}]
		}
	}`

	tests := []struct {
		name      string
		writeBody string
		wantRef   bool
		wantErr   bool
	}{
		{"all session events present", allPresent, true, false},
		{"one session event stripped", startOnly, false, false},
		{"session events unrelated", unrelated, false, false},
		{"missing file", "", false, false},
		{"corrupt json", "{not valid", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if tc.writeBody != "" {
				if err := os.WriteFile(path, []byte(tc.writeBody), 0600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			got, err := ClaudeSettingsReferencesSessionEndpoint(path, sessionEP)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (ref=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantRef {
				t.Fatalf("ref: want %v, got %v", tc.wantRef, got)
			}
		})
	}
	// Bad-args validation parity with the PreToolUse helper.
	if _, err := ClaudeSettingsReferencesSessionEndpoint("", "x"); err == nil {
		t.Fatalf("empty path: want error, got nil")
	}
	if _, err := ClaudeSettingsReferencesSessionEndpoint("/tmp/foo", ""); err == nil {
		t.Fatalf("empty session endpoint: want error, got nil")
	}
}

// TestClaudeSettingsReferencesSessionEndpoint_RealInstall proves the helper
// agrees with what InstallClaudeCodeHooks actually writes: a fresh install
// must read as referenced=true under the installer's own session endpoint.
func TestClaudeSettingsReferencesSessionEndpoint_RealInstall(t *testing.T) {
	home := t.TempDir()
	inst, err := NewHookInstaller(home, newFakeStore(), "")
	if err != nil {
		t.Fatalf("NewHookInstaller: %v", err)
	}
	if _, err := inst.InstallClaudeCodeHooks(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	ref, err := ClaudeSettingsReferencesSessionEndpoint(
		inst.ClaudeSettingsPath(), inst.SessionEndpoint())
	if err != nil {
		t.Fatalf("session drift check: %v", err)
	}
	if !ref {
		t.Fatal("fresh install must read as session-referenced=true")
	}
}

// TestHookInstallerSettingsPathAccessor pins the exported accessors so
// the drift reconciler always sees the same file the installer writes.
// A divergence here would mean the dashboard reads a different file
// than the writer touches — exactly the bug the reconciler exists to
// catch.
func TestHookInstallerSettingsPathAccessor(t *testing.T) {
	home := t.TempDir()
	inst, err := NewHookInstaller(home, newFakeStore(), "")
	if err != nil {
		t.Fatalf("NewHookInstaller: %v", err)
	}
	want := filepath.Join(home, ".claude", "settings.json")
	if got := inst.ClaudeSettingsPath(); got != want {
		t.Fatalf("ClaudeSettingsPath: want %s, got %s", want, got)
	}
	if got := DefaultClaudeSettingsPath(home); got != want {
		t.Fatalf("DefaultClaudeSettingsPath: want %s, got %s", want, got)
	}
	if got := inst.Endpoint(); got != DefaultHookEndpoint {
		t.Fatalf("Endpoint: want %s, got %s", DefaultHookEndpoint, got)
	}
}

// Sanity: fileExists is a tiny helper, but the test guards against a
// subtle regression (e.g. someone changing it to os.Open which would
// leak fds and behave differently on broken symlinks).
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "hit")
	if err := os.WriteFile(hit, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(hit) {
		t.Errorf("want true for existing file")
	}
	if fileExists(filepath.Join(dir, "miss")) {
		t.Errorf("want false for missing file")
	}
}
