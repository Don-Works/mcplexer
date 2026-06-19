package brain

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
		wantDirEnd  string // suffix the resolved Dir must end with
	}{
		{
			name:        "unset is disabled",
			env:         map[string]string{},
			wantEnabled: false,
			wantDirEnd:  DefaultDirName,
		},
		{
			name:        "enabled=1",
			env:         map[string]string{EnvEnabled: "1"},
			wantEnabled: true,
			wantDirEnd:  DefaultDirName,
		},
		{
			name:        "enabled=true case-insensitive",
			env:         map[string]string{EnvEnabled: "TRUE"},
			wantEnabled: true,
			wantDirEnd:  DefaultDirName,
		},
		{
			name:        "enabled=0 is disabled",
			env:         map[string]string{EnvEnabled: "0"},
			wantEnabled: false,
		},
		{
			name:        "enabled=garbage is disabled",
			env:         map[string]string{EnvEnabled: "maybe"},
			wantEnabled: false,
		},
		{
			name:        "explicit dir override",
			env:         map[string]string{EnvEnabled: "yes", EnvDir: "/custom/brain"},
			wantEnabled: true,
			wantDirEnd:  filepath.Join("custom", "brain"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			c := LoadConfig(getenv)
			if c.Enabled != tc.wantEnabled {
				t.Fatalf("Enabled = %v, want %v", c.Enabled, tc.wantEnabled)
			}
			if c.Dir == "" {
				t.Fatal("Dir must never be empty")
			}
			if tc.wantDirEnd != "" && filepath.Base(c.Dir) != filepath.Base(tc.wantDirEnd) {
				t.Fatalf("Dir = %q, want suffix %q", c.Dir, tc.wantDirEnd)
			}
		})
	}
}

func TestSettingsEnabledAndMerge(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty blob", "", false},
		{"absent key", `{"other":true}`, false},
		{"explicit false", `{"brain_enabled":false}`, false},
		{"explicit true", `{"brain_enabled":true}`, true},
		{"malformed json", `{not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SettingsEnabled(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("SettingsEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}

	// MergeSettings OR-semantics: env-on stays on; env-off + settings-on
	// turns on; both off stays off.
	if !(Config{Enabled: true}).MergeSettings(json.RawMessage(`{"brain_enabled":false}`)).Enabled {
		t.Error("env-on should stay on regardless of settings")
	}
	if !(Config{Enabled: false}).MergeSettings(json.RawMessage(`{"brain_enabled":true}`)).Enabled {
		t.Error("settings-on should turn the brain on")
	}
	if (Config{Enabled: false}).MergeSettings(json.RawMessage(`{}`)).Enabled {
		t.Error("both off should stay off")
	}
}

func TestConfigDirHelpers(t *testing.T) {
	c := Config{Dir: "/root/brain"}
	got, err := c.WorkspaceDir("mcplexer")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	if got != filepath.Join("/root/brain", "workspaces", "mcplexer") {
		t.Errorf("WorkspaceDir = %q, want %q", got, filepath.Join("/root/brain", "workspaces", "mcplexer"))
	}
	got, err = c.ClientDir("acme")
	if err != nil {
		t.Fatalf("ClientDir: %v", err)
	}
	if got != filepath.Join("/root/brain", "clients", "acme") {
		t.Errorf("ClientDir = %q, want %q", got, filepath.Join("/root/brain", "clients", "acme"))
	}
	if got, want := c.GlobalDir(), filepath.Join("/root/brain", "global"); got != want {
		t.Errorf("GlobalDir = %q, want %q", got, want)
	}
}
