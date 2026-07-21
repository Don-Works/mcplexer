package main

import "testing"

func TestNormalizeServerProfile(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty defaults full", raw: "", want: serverProfileFull},
		{name: "full", raw: "full", want: serverProfileFull},
		{name: "skills", raw: "skills", want: serverProfileSkills},
		{name: "tasks", raw: "tasks", want: serverProfileTasks},
		{name: "skills tasks plus", raw: "skills+tasks", want: serverProfileSkillsTasks},
		{name: "skills tasks comma", raw: "skills,tasks", want: serverProfileSkillsTasks},
		{name: "tasks skills normalized", raw: "tasks+skills", want: serverProfileSkillsTasks},
		{name: "invalid", raw: "workers", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeServerProfile(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeServerProfile(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLoadConfigReadsServerProfile(t *testing.T) {
	t.Setenv("MCPLEXER_SERVER_PROFILE", "skills,tasks")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ServerProfile != serverProfileSkillsTasks {
		t.Fatalf("ServerProfile = %q, want %q", cfg.ServerProfile, serverProfileSkillsTasks)
	}
}

func TestParseTrustedHostsNormalizesBrowserOrigins(t *testing.T) {
	got := parseTrustedHosts("https://My-Mac.Tailnet-Name.ts.net:3333/app, other-host.local:4444, plain.example.")
	want := []string{"my-mac.tailnet-name.ts.net", "other-host.local", "plain.example"}
	if len(got) != len(want) {
		t.Fatalf("parseTrustedHosts length = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseTrustedHosts[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestApplyFlagsReadsServerProfile(t *testing.T) {
	cfg := &Config{ServerProfile: serverProfileFull}
	if err := applyFlags(cfg, []string{"--server-profile=skills"}); err != nil {
		t.Fatalf("applyFlags: %v", err)
	}
	if cfg.ServerProfile != serverProfileSkills {
		t.Fatalf("ServerProfile = %q, want %q", cfg.ServerProfile, serverProfileSkills)
	}
}

func TestServerCapabilities(t *testing.T) {
	caps := serverCapabilities(serverProfileSkillsTasks)
	for _, key := range []string{"skills", "tasks", "signals", "server_settings"} {
		if !caps[key] {
			t.Fatalf("capability %q = false, want true", key)
		}
	}
	for _, key := range []string{"local_setup", "workers", "memory"} {
		if caps[key] {
			t.Fatalf("capability %q = true, want false", key)
		}
	}
}
