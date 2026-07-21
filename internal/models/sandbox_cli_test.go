package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/sandbox"
)

func TestModelCLIEnvironmentPoliciesAreProviderScopedAndFailClosed(t *testing.T) {
	multiProviderKeys := []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_OAUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"DEEPSEEK_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"MISTRAL_API_KEY",
		"GROQ_API_KEY",
		"CEREBRAS_API_KEY",
		"XAI_API_KEY",
		"XAI_BASE_URL",
		"FIREWORKS_API_KEY",
		"TOGETHER_API_KEY",
		"OPENROUTER_API_KEY",
		"AI_GATEWAY_API_KEY",
		"ZAI_API_KEY",
		"MINIMAX_API_KEY",
		"OPENCODE_API_KEY",
		"KIMI_API_KEY",
		"MOONSHOT_API_KEY",
		"MIMO_API_KEY",
		"MIMO_BASE_URL",
		"XIAOMI_API_KEY",
		"XIAOMI_BASE_URL",
		"XIAOMI_TOKEN_PLAN_CN_API_KEY",
		"XIAOMI_TOKEN_PLAN_AMS_API_KEY",
		"XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	}
	claudeKeys := []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
		"CLAUDE_CODE_OAUTH_SCOPES",
	}
	codexKeys := []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}
	geminiKeys := []string{
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GEMINI_MODEL",
		"CODE_ASSIST_ENDPOINT",
		"GOOGLE_GEMINI_BASE_URL",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}
	grokKeys := []string{"XAI_API_KEY", "XAI_BASE_URL"}
	xdgKeys := []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}

	base := []string{
		"PATH=/bin",
		"HOME=/home/example",
		"LANG=en_GB.UTF-8",
		"TERM=xterm-256color",
		"HTTPS_PROXY=http://proxy.example:8443",
		"no_proxy=localhost,127.0.0.1",
		"SSL_CERT_FILE=/etc/provider-ca.pem",
		"SSL_CERT_DIR=/etc/provider-certs",
		"NODE_EXTRA_CA_CERTS=/etc/node-provider-ca.pem",
		"MCPLEXER_AUTH_TOKEN=daemon-secret",
		"MCPLEXER_FUTURE_CONFIG=not-approved",
		"SLACK_BOT_TOKEN=slack-secret",
		"TELEGRAM_BOT_TOKEN=telegram-secret",
		"OPENWA_API_KEY=openwa-secret",
		"DATABASE_URL=postgres://operator-secret",
		"POSTGRES_PASSWORD=database-secret",
		"SENTRY_AUTH_TOKEN=sentry-secret",
		"FUTURE_VENDOR_SECRET=future-secret",
		"FUTURE_PROVIDER_API_KEY=future-provider-secret",
		"UNKNOWN_TOKEN=unknown-secret",
	}
	allProviderKeys := make(map[string]struct{})
	for _, keys := range [][]string{multiProviderKeys, claudeKeys, codexKeys, geminiKeys, grokKeys, xdgKeys} {
		for _, key := range keys {
			allProviderKeys[key] = struct{}{}
		}
	}
	for key := range allProviderKeys {
		base = append(base, key+"=provider-value")
	}

	tests := []struct {
		name    string
		policy  modelCLIEnvironmentPolicy
		allowed []string
	}{
		{"claude", claudeCLIEnvironmentPolicy(), claudeKeys},
		{"opencode", opencodeCLIEnvironmentPolicy(), append(append([]string(nil), multiProviderKeys...), xdgKeys...)},
		{"codex", codexCLIEnvironmentPolicy(), codexKeys},
		{"gemini", geminiCLIEnvironmentPolicy(), geminiKeys},
		{"grok", grokCLIEnvironmentPolicy(), grokKeys},
		{"mimo", mimoCLIEnvironmentPolicy(), multiProviderKeys},
		{"pi", piCLIEnvironmentPolicy(), multiProviderKeys},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelCLIEnvironment(base, "/scratch", tt.policy)
			values := environmentValues(got)
			for key, want := range map[string]string{
				"PATH":                "/bin",
				"HOME":                "/home/example",
				"LANG":                "en_GB.UTF-8",
				"TERM":                "xterm-256color",
				"HTTPS_PROXY":         "http://proxy.example:8443",
				"no_proxy":            "localhost,127.0.0.1",
				"SSL_CERT_FILE":       "/etc/provider-ca.pem",
				"SSL_CERT_DIR":        "/etc/provider-certs",
				"NODE_EXTRA_CA_CERTS": "/etc/node-provider-ca.pem",
				"TMPDIR":              "/scratch",
				"TMP":                 "/scratch",
				"TEMP":                "/scratch",
			} {
				if gotValues := values[key]; len(gotValues) != 1 || gotValues[0] != want {
					t.Errorf("%s = %v, want [%q]", key, gotValues, want)
				}
			}

			permitted := map[string]struct{}{
				"PATH": {}, "HOME": {}, "LANG": {}, "TERM": {}, "HTTPS_PROXY": {}, "no_proxy": {},
				"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {}, "NODE_EXTRA_CA_CERTS": {},
				"TMPDIR": {}, "TMP": {}, "TEMP": {},
			}
			for _, key := range tt.allowed {
				permitted[key] = struct{}{}
				if gotValues := values[key]; len(gotValues) != 1 || gotValues[0] != "provider-value" {
					t.Errorf("allowed provider variable %s = %v, want [provider-value]", key, gotValues)
				}
			}
			for key := range values {
				if _, ok := permitted[key]; !ok {
					t.Errorf("unexpected environment variable %s leaked into %s runner", key, tt.name)
				}
			}
		})
	}
}

func TestModelCLISandboxConfigsAreInvocationScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))

	paths := []string{
		".claude", ".claude.json",
		".codex/auth.json", ".codex/config.toml", ".codex/rules",
		".gemini/oauth_creds.json", ".gemini/tmp", ".gemini/history",
		".grok/auth.json", ".grok/logs", ".grok/projects",
		".mimo", ".cache/mimo", ".local/share/mimo",
		".pi/agent/auth.json", ".pi/agent/logs",
		"xdg-config/opencode", "xdg-data/opencode/auth.json", "xdg-cache/opencode",
	}
	for _, rel := range paths {
		createSandboxPolicyFixture(t, filepath.Join(home, rel))
	}

	bin := filepath.Join(home, "bin", "provider")
	createSandboxPolicyFixture(t, bin)
	workspace := filepath.Join(home, "workspace")
	scratch := filepath.Join(home, "scratch")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		builder    modelCLISandboxConfigBuilder
		protected  string
		protection string
	}{
		{"claude", claudeCLISandboxConfig, filepath.Join(home, ".claude"), "deny-write"},
		{"opencode", opencodeCLISandboxConfig, filepath.Join(home, "xdg-data/opencode/auth.json"), "deny-write"},
		{"codex", codexCLISandboxConfig, filepath.Join(home, ".codex/auth.json"), "deny-write"},
		{"gemini", geminiCLISandboxConfig, filepath.Join(home, ".gemini/oauth_creds.json"), "deny-write"},
		{"grok", grokCLISandboxConfig, filepath.Join(home, ".grok/auth.json"), "deny-write"},
		{"mimo", mimoCLISandboxConfig, filepath.Join(home, ".mimo"), "read-only"},
		{"pi", piCLISandboxConfig, filepath.Join(home, ".pi/agent/auth.json"), "deny-write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.builder(bin, workspace, scratch)
			if cfg.Network != sandbox.NetworkHost {
				t.Fatalf("Network = %q, want host", cfg.Network)
			}
			if cfg.WorkingDir != workspace {
				t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, workspace)
			}
			for _, path := range []string{workspace, scratch} {
				if !stringSliceContains(cfg.ReadWritePaths, path) {
					t.Errorf("ReadWritePaths missing %q: %v", path, cfg.ReadWritePaths)
				}
			}
			if !stringSliceContains(cfg.ReadOnlyPaths, bin) {
				t.Errorf("ReadOnlyPaths missing binary %q: %v", bin, cfg.ReadOnlyPaths)
			}
			for _, paths := range [][]string{cfg.ReadOnlyPaths, cfg.ReadWritePaths, cfg.DenyWritePaths} {
				if stringSliceContains(paths, home) {
					t.Fatalf("sandbox grants blanket HOME access: %+v", cfg)
				}
			}
			denied := sandbox.MergeDenyPaths(home, cfg.DenyPaths)
			for _, path := range []string{
				filepath.Join(home, ".mcplexer"),
				filepath.Join(home, ".ssh"),
				filepath.Join(home, ".aws"),
				filepath.Join(home, ".gnupg"),
				filepath.Join(home, ".config/gh"),
				filepath.Join(home, ".config/gcloud"),
			} {
				if !stringSliceContains(denied, path) {
					t.Errorf("DenyPaths missing %q: %v", path, denied)
				}
			}
			switch tt.protection {
			case "deny-write":
				if !stringSliceContains(cfg.DenyWritePaths, tt.protected) {
					t.Errorf("DenyWritePaths missing %q: %v", tt.protected, cfg.DenyWritePaths)
				}
			case "read-only":
				if !stringSliceContains(cfg.ReadOnlyPaths, tt.protected) {
					t.Errorf("ReadOnlyPaths missing %q: %v", tt.protected, cfg.ReadOnlyPaths)
				}
			}
		})
	}
}

func TestModelCLISandboxConfigDoesNotInventWorkspaceAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "provider")
	createSandboxPolicyFixture(t, bin)
	scratch := filepath.Join(home, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := claudeCLISandboxConfig(bin, "", scratch)
	if cfg.WorkingDir != scratch {
		t.Fatalf("WorkingDir = %q, want scratch %q", cfg.WorkingDir, scratch)
	}
	if stringSliceContains(cfg.ReadWritePaths, home) {
		t.Fatalf("empty workspace granted HOME: %v", cfg.ReadWritePaths)
	}
	if !stringSliceContains(cfg.ReadWritePaths, scratch) {
		t.Fatalf("ReadWritePaths missing scratch: %v", cfg.ReadWritePaths)
	}
	// The only writable paths are scratch and the fixed claude IPC temp
	// dir — never the empty workspace or anything under HOME.
	for _, p := range cfg.ReadWritePaths {
		if p != scratch && p != claudeCLITempDir() {
			t.Fatalf("unexpected writable path %q: %v", p, cfg.ReadWritePaths)
		}
	}
}

func createSandboxPolicyFixture(t *testing.T, path string) {
	t.Helper()
	if filepath.Ext(path) == "" && filepath.Base(path) != "provider" {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

func stringSliceContains(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func environmentValues(environment []string) map[string][]string {
	values := make(map[string][]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = append(values[key], value)
		}
	}
	return values
}

// TestGrokCLISandboxGrantsSessionWrite is the regression test for the
// live "Couldn't create session: FS_PERMISSION_DENIED" failure: grok
// headless refuses to start unless it can WRITE its own per-run session
// and project state under ~/.grok. The earlier policy hard-denied both
// dirs, breaking every grok delegation the moment CLI sandboxing went
// live. auth/config must still be write-protected.
func TestGrokCLISandboxGrantsSessionWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin", "grok")
	createSandboxPolicyFixture(t, bin)
	for _, rel := range []string{".grok/sessions", ".grok/projects", ".grok/auth.json"} {
		createSandboxPolicyFixture(t, filepath.Join(home, rel))
	}
	workspace := filepath.Join(home, "workspace")
	scratch := filepath.Join(home, "scratch")

	cfg := grokCLISandboxConfig(bin, workspace, scratch)
	for _, want := range []string{
		filepath.Join(home, ".grok", "sessions"),
		filepath.Join(home, ".grok", "projects"),
	} {
		if !stringSliceContains(cfg.ReadWritePaths, want) {
			t.Errorf("grok session dir not writable: %q missing from %v", want, cfg.ReadWritePaths)
		}
		if stringSliceContains(cfg.DenyPaths, want) {
			t.Errorf("grok session dir still hard-denied: %q", want)
		}
	}
	// Credentials stay write-protected.
	if !stringSliceContains(cfg.DenyWritePaths, filepath.Join(home, ".grok", "auth.json")) {
		t.Error("grok auth.json must remain write-denied")
	}
}

// TestClaudeCLISandboxGrantsTempDir is the regression test for the live
// "EEXIST mkdir /tmp/claude-<uid>" failure: claude_cli writes a fixed
// per-user IPC dir under /tmp (ignoring TMPDIR); a denied stat there
// makes its non-recursive mkdir fail. The dir must be granted read-write.
func TestClaudeCLISandboxGrantsTempDir(t *testing.T) {
	tmp := claudeCLITempDir()
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Skipf("cannot create %s: %v", tmp, err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	createSandboxPolicyFixture(t, bin)
	cfg := claudeCLISandboxConfig(bin, t.TempDir(), t.TempDir())
	if !stringSliceContains(cfg.ReadWritePaths, tmp) {
		t.Fatalf("claude temp dir %q not granted read-write: %v", tmp, cfg.ReadWritePaths)
	}
}
