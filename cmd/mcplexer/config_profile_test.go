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
		{name: "core", raw: " CORE ", want: serverProfileCore},
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
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "core", want: serverProfileCore},
		{raw: "skills,tasks", want: serverProfileSkillsTasks},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Setenv("MCPLEXER_SERVER_PROFILE", tt.raw)
			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.ServerProfile != tt.want {
				t.Fatalf("ServerProfile = %q, want %q", cfg.ServerProfile, tt.want)
			}
		})
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
	tests := []struct {
		flag string
		want string
	}{
		{flag: "--server-profile=core", want: serverProfileCore},
		{flag: "--profile=skills", want: serverProfileSkills},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			cfg := &Config{ServerProfile: serverProfileFull}
			if err := applyFlags(cfg, []string{tt.flag}); err != nil {
				t.Fatalf("applyFlags: %v", err)
			}
			if cfg.ServerProfile != tt.want {
				t.Fatalf("ServerProfile = %q, want %q", cfg.ServerProfile, tt.want)
			}
		})
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

func TestCoreServerCapabilities(t *testing.T) {
	caps := serverCapabilities(serverProfileCore)
	for _, key := range []string{
		"approvals", "audit", "downstreams", "guards",
		"local_setup", "signals", "server_settings",
	} {
		if !caps[key] {
			t.Fatalf("core capability %q = false, want true", key)
		}
	}
	for _, key := range []string{
		"brain", "delegations", "memory", "model_routing",
		"skills", "tasks", "workers",
	} {
		if caps[key] {
			t.Fatalf("core capability %q = true, want false", key)
		}
	}
}

func TestRuntimeModulesForProfile(t *testing.T) {
	all := runtimeModulePlan{
		Core:          true,
		Agent:         true,
		Automation:    true,
		Collaboration: true,
		Ops:           true,
		Experimental:  true,
	}
	tests := []struct {
		profile string
		want    runtimeModulePlan
	}{
		{profile: serverProfileCore, want: runtimeModulePlan{Core: true}},
		{profile: serverProfileFull, want: all},
		{profile: serverProfileSkills, want: all},
		{profile: serverProfileTasks, want: all},
		{profile: serverProfileSkillsTasks, want: all},
		{profile: "not-a-profile", want: all},
	}
	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			if got := runtimeModulesForProfile(tt.profile); got != tt.want {
				t.Fatalf("runtimeModulesForProfile(%q) = %+v, want %+v", tt.profile, got, tt.want)
			}
		})
	}
}
