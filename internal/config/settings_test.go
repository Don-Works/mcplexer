package config

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type mockSettingsStore struct {
	raw json.RawMessage
}

func (m *mockSettingsStore) GetSettings(_ context.Context) (json.RawMessage, error) {
	return m.raw, nil
}

func (m *mockSettingsStore) UpdateSettings(_ context.Context, data json.RawMessage) error {
	m.raw = data
	return nil
}

func TestDefaultSettings_ContextCaps(t *testing.T) {
	s := DefaultSettings()
	if s.CodeModeMaxOutputBytes != 24*1024 {
		t.Fatalf("CodeModeMaxOutputBytes = %d, want %d", s.CodeModeMaxOutputBytes, 24*1024)
	}
	if s.CodeModeMaxHeapGrowthMB != 2048 {
		t.Fatalf("CodeModeMaxHeapGrowthMB = %d, want 2048", s.CodeModeMaxHeapGrowthMB)
	}
	if s.MeshReceiveMaxResults != 20 {
		t.Fatalf("MeshReceiveMaxResults = %d, want 20", s.MeshReceiveMaxResults)
	}
	if s.MeshReceivePreviewBytes != 512 {
		t.Fatalf("MeshReceivePreviewBytes = %d, want 512", s.MeshReceivePreviewBytes)
	}
	if s.MeshSendMaxContentBytes != 64*1024 {
		t.Fatalf("MeshSendMaxContentBytes = %d, want %d", s.MeshSendMaxContentBytes, 64*1024)
	}
}

func TestLoadSettings_MissingContextCapsUseDefaults(t *testing.T) {
	st := &mockSettingsStore{
		raw: json.RawMessage(`{"slim_tools":false,"tools_cache_ttl_sec":30,"log_level":"warn"}`),
	}
	svc := NewSettingsService(st)

	got := svc.Load(context.Background())
	if got.CodeModeMaxOutputBytes != 24*1024 {
		t.Fatalf("CodeModeMaxOutputBytes = %d, want default", got.CodeModeMaxOutputBytes)
	}
	if got.CodeModeMaxHeapGrowthMB != 2048 {
		t.Fatalf("CodeModeMaxHeapGrowthMB = %d, want default", got.CodeModeMaxHeapGrowthMB)
	}
	if got.MeshReceiveMaxResults != 20 {
		t.Fatalf("MeshReceiveMaxResults = %d, want default", got.MeshReceiveMaxResults)
	}
	if got.MeshReceivePreviewBytes != 512 {
		t.Fatalf("MeshReceivePreviewBytes = %d, want default", got.MeshReceivePreviewBytes)
	}
	if got.MeshSendMaxContentBytes != 64*1024 {
		t.Fatalf("MeshSendMaxContentBytes = %d, want default", got.MeshSendMaxContentBytes)
	}
}

// TestValidateDisplayName pins the validation surface for user-facing
// device labels. NOT auth-bearing — but we still reject obvious garbage so
// the UI doesn't render exotic Unicode that confuses users about which
// device sent a mesh message.
func TestValidateDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple alpha", "my-mbp", false},
		{"with dot", "max.air", false},
		{"with underscore", "max_air", false},
		{"alnum mix", "Box1", false},
		{"single char", "a", false},
		{"max len", strings.Repeat("a", 50), false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 51), true},
		{"space", "max air", true},
		{"slash", "max/air", true},
		{"unicode", "máx", true},
		{"emoji", "max🚀", true},
		{"newline", "max\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDisplayName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateDisplayName(%q) err = %v, wantErr %v",
					tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestSaveSettings_RejectsInvalidDisplayName ensures the API surface
// (PUT /api/v1/settings) refuses bad names instead of writing them.
func TestSaveSettings_RejectsInvalidDisplayName(t *testing.T) {
	st := &mockSettingsStore{}
	svc := NewSettingsService(st)
	bad := DefaultSettings()
	bad.DisplayName = "hello world" // contains space
	err := svc.Save(context.Background(), bad)
	if err == nil {
		t.Fatal("Save should reject invalid display_name")
	}
}

func TestSaveSettings_RejectsInvalidCodeModeHeapCap(t *testing.T) {
	st := &mockSettingsStore{}
	svc := NewSettingsService(st)
	bad := DefaultSettings()
	bad.CodeModeMaxHeapGrowthMB = 4096
	err := svc.Save(context.Background(), bad)
	if err == nil {
		t.Fatal("Save should reject invalid code_mode_max_heap_growth_mb")
	}
}

// TestDefaultSettings_DisplayNameNotEmpty ensures we always derive a
// non-empty default so the wire field is never blank on first run.
func TestDefaultSettings_DisplayNameNotEmpty(t *testing.T) {
	s := DefaultSettings()
	if s.DisplayName == "" {
		t.Fatal("DefaultSettings().DisplayName should not be empty")
	}
	if err := ValidateDisplayName(s.DisplayName); err != nil {
		t.Fatalf("default display_name failed validation: %v", err)
	}
}

// TestLoadSettings_FillsLegacyDisplayName checks the legacy-row path:
// a settings JSON written before display_name existed must come back with
// a default value, not "" — that's what stops "peer-Ymq…" leaking into
// the UI for users on older binaries.
func TestLoadSettings_FillsLegacyDisplayName(t *testing.T) {
	st := &mockSettingsStore{
		raw: json.RawMessage(`{"slim_tools":true,"log_level":"info"}`),
	}
	svc := NewSettingsService(st)
	got := svc.Load(context.Background())
	if got.DisplayName == "" {
		t.Fatal("legacy load returned empty display_name")
	}
}

func TestNormalizeRemoteSkillServerURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty", "", "", false},
		{"bare dns", "skills.example", "http://skills.example", false},
		{"bare host port", "skills.example:13333", "http://skills.example:13333", false},
		{"http url", "http://skills.example:3333/", "http://skills.example:3333", false},
		{"https url", "https://skills.example.com", "https://skills.example.com", false},
		{"bad scheme", "ftp://skills.example", "", true},
		{"credentials", "https://user:pass@skills.example.com", "", true},
		{"query", "https://skills.example.com?token=x", "", true},
		{"bare path", "skills.example/api", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRemoteSkillServerURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSaveSettings_NormalizesRemoteSkillServerURL(t *testing.T) {
	st := &mockSettingsStore{}
	svc := NewSettingsService(st)
	settings := DefaultSettings()
	settings.RemoteSkillServerURL = "skills.example"
	if err := svc.Save(context.Background(), settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var got Settings
	if err := json.Unmarshal(st.raw, &got); err != nil {
		t.Fatalf("unmarshal stored settings: %v", err)
	}
	if got.RemoteSkillServerURL != "http://skills.example" {
		t.Fatalf("RemoteSkillServerURL = %q", got.RemoteSkillServerURL)
	}
}

func TestLoadSettings_NormalizesRemoteSkillServerURL(t *testing.T) {
	st := &mockSettingsStore{
		raw: json.RawMessage(`{"remote_skill_server_url":"skills.example"}`),
	}
	svc := NewSettingsService(st)
	got := svc.Load(context.Background())
	if got.RemoteSkillServerURL != "http://skills.example" {
		t.Fatalf("RemoteSkillServerURL = %q", got.RemoteSkillServerURL)
	}
}

// TestDefaultSettings_ToolHints_NoDeploymentLeak guards the repo/local
// boundary for tool hints. Hints in DefaultSettings ship in every binary;
// they MUST be keyed by tool names from generic public servers, never by
// names that identify a specific deployment (e.g. `<vendor>_production__…`,
// `<vendor>_staging__…`). Private hints belong in the user's settings row in
// ~/.mcplexer/mcplexer.db, set via dashboard or `mcplexer__update_settings`.
//
// Heuristic: the namespace segment (before `__`) of a public MCP server is
// always a single token (postgres, github, linear, …). An underscore in the
// namespace strongly suggests `<service>_<env>` — i.e. a private deployment.
func TestDefaultSettings_ToolHints_NoDeploymentLeak(t *testing.T) {
	for tool := range DefaultSettings().ToolHints {
		ns, _, ok := strings.Cut(tool, "__")
		if !ok {
			t.Errorf("tool hint key %q is not namespaced (expected `<ns>__<tool>`)", tool)
			continue
		}
		if strings.Contains(ns, "_") {
			t.Errorf("tool hint key %q has underscore in namespace %q — looks deployment-specific; "+
				"move private hints to the user's settings row, not DefaultSettings", tool, ns)
		}
	}
}

func TestApplyEnvOverrides_ContextCaps(t *testing.T) {
	t.Setenv("MCPLEXER_CODE_MODE_MAX_OUTPUT_BYTES", "4096")
	t.Setenv("MCPLEXER_CODE_MODE_MAX_HEAP_GROWTH_MB", "768")
	t.Setenv("MCPLEXER_MESH_RECEIVE_MAX_RESULTS", "7")
	t.Setenv("MCPLEXER_MESH_RECEIVE_PREVIEW_BYTES", "128")
	t.Setenv("MCPLEXER_MESH_SEND_MAX_CONTENT_BYTES", "8192")

	got := applyEnvOverrides(DefaultSettings())
	if got.CodeModeMaxOutputBytes != 4096 {
		t.Fatalf("CodeModeMaxOutputBytes = %d, want 4096", got.CodeModeMaxOutputBytes)
	}
	if got.CodeModeMaxHeapGrowthMB != 768 {
		t.Fatalf("CodeModeMaxHeapGrowthMB = %d, want 768", got.CodeModeMaxHeapGrowthMB)
	}
	if got.MeshReceiveMaxResults != 7 {
		t.Fatalf("MeshReceiveMaxResults = %d, want 7", got.MeshReceiveMaxResults)
	}
	if got.MeshReceivePreviewBytes != 128 {
		t.Fatalf("MeshReceivePreviewBytes = %d, want 128", got.MeshReceivePreviewBytes)
	}
	if got.MeshSendMaxContentBytes != 8192 {
		t.Fatalf("MeshSendMaxContentBytes = %d, want 8192", got.MeshSendMaxContentBytes)
	}
}

func TestApplyEnvOverrides_RemoteSkillServerURL(t *testing.T) {
	t.Setenv("MCPLEXER_REMOTE_SKILL_SERVER_URL", "skills.example")
	got := applyEnvOverrides(DefaultSettings())
	if got.RemoteSkillServerURL != "http://skills.example" {
		t.Fatalf("RemoteSkillServerURL = %q", got.RemoteSkillServerURL)
	}
}

// TestDefaultSettings_ShellGuardAllowChainingDefaultsOn pins the new
// default: chaining metachars are allowed (cheap-block lifted) unless the
// operator opts back into hard-block.
func TestDefaultSettings_ShellGuardAllowChainingDefaultsOn(t *testing.T) {
	if !DefaultSettings().ShellGuardAllowChaining {
		t.Fatal("ShellGuardAllowChaining should default to true (allow chaining)")
	}
}

// TestLoadSettings_LegacyRowBackfillsShellGuardAllowChaining covers the
// upgrade path: a settings row persisted before this field existed has no
// shell_guard_allow_chaining key. A plain unmarshal would leave it false
// (hard-block), silently regressing the new allow-chaining default on every
// existing install. Load must backfill missing keys to true.
func TestLoadSettings_LegacyRowBackfillsShellGuardAllowChaining(t *testing.T) {
	st := &mockSettingsStore{
		raw: json.RawMessage(`{"slim_tools":true,"log_level":"info"}`),
	}
	svc := NewSettingsService(st)
	got := svc.Load(context.Background())
	if !got.ShellGuardAllowChaining {
		t.Fatal("legacy row (no key) should backfill ShellGuardAllowChaining=true")
	}
}

// TestLoadSettings_ExplicitFalseShellGuardAllowChainingHonoured ensures an
// operator who deliberately wrote shell_guard_allow_chaining:false (the
// hard-block opt-in) is NOT overwritten by the legacy backfill — the key is
// present, so its explicit value wins.
func TestLoadSettings_ExplicitFalseShellGuardAllowChainingHonoured(t *testing.T) {
	st := &mockSettingsStore{
		raw: json.RawMessage(`{"log_level":"info","shell_guard_allow_chaining":false}`),
	}
	svc := NewSettingsService(st)
	got := svc.Load(context.Background())
	if got.ShellGuardAllowChaining {
		t.Fatal("explicit shell_guard_allow_chaining:false must be honoured, not backfilled to true")
	}
}

// TestApplyEnvOverrides_ShellGuardAllowChaining verifies the env escape
// hatch can force hard-block headlessly without a settings PUT.
func TestApplyEnvOverrides_ShellGuardAllowChaining(t *testing.T) {
	t.Setenv("MCPLEXER_SHELL_GUARD_ALLOW_CHAINING", "0")
	got := applyEnvOverrides(DefaultSettings())
	if got.ShellGuardAllowChaining {
		t.Fatal("MCPLEXER_SHELL_GUARD_ALLOW_CHAINING=0 should force allow-chaining off")
	}
}
