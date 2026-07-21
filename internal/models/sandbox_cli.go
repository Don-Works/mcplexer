package models

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/sandbox"
)

type modelCLISandboxPolicy struct {
	readOnly  []string
	readWrite []string
	denyWrite []string
	deny      []string
}

type modelCLIEnvironmentPolicy struct {
	allow []string
}

type modelCLISandboxConfigBuilder func(
	binary, workspacePath, scratchPath string, extraReadOnly ...string,
) sandbox.Config

// runSandboxedModelCLI prepares a fresh scratch directory and a strict,
// per-invocation profile. The CLI receives only its executable/runtime,
// provider config, explicit workspace, and scratch directory. HOME remains in
// the environment for standard provider config discovery, but the filesystem
// profile never grants the HOME root. The child environment is rebuilt from a
// closed OS/runtime and provider-specific allowlist.
func runSandboxedModelCLI(
	ctx context.Context,
	binary string,
	args []string,
	stdin string,
	workspacePath string,
	configBuilder modelCLISandboxConfigBuilder,
	environmentPolicy modelCLIEnvironmentPolicy,
	extraReadOnly ...string,
) ([]byte, []byte, error) {
	if workspacePath != "" {
		absoluteWorkspace, err := filepath.Abs(workspacePath)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve model CLI workspace: %w", err)
		}
		workspacePath = absoluteWorkspace
	}
	scratchPath, err := os.MkdirTemp("", "mcplexer-model-cli-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(scratchPath) //nolint:errcheck

	environmentBase := os.Environ()
	extraReadOnly = append(extraReadOnly, sandbox.CommandEnvironmentReadPaths(environmentBase)...)
	cfg := configBuilder(binary, workspacePath, scratchPath, extraReadOnly...)
	wrapper := sandbox.NewCommandWrapper(cfg)
	program, wrappedArgs, cleanupSandbox := wrapper.Wrap(binary, args)
	defer cleanupSandbox()

	cmd := newSandboxedCLICmd(ctx, program, wrappedArgs)
	cmd.Env = modelCLIEnvironment(environmentBase, scratchPath, environmentPolicy)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Dir = workspacePath
	if cmd.Dir == "" {
		cmd.Dir = scratchPath
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func modelCLIEnvironment(base []string, scratchPath string, policy modelCLIEnvironmentPolicy) []string {
	return sandbox.AllowlistedCommandEnvironment(base, scratchPath, policy.allow...)
}

func claudeCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{
		allow: []string{
			"ANTHROPIC_API_KEY",
			"ANTHROPIC_AUTH_TOKEN",
			"ANTHROPIC_BASE_URL",
			"CLAUDE_CODE_OAUTH_TOKEN",
			"CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
			"CLAUDE_CODE_OAUTH_SCOPES",
		},
	}
}

func codexCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
	}}
}

func geminiCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: []string{
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GEMINI_MODEL",
		"CODE_ASSIST_ENDPOINT",
		"GOOGLE_GEMINI_BASE_URL",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}}
}

func grokCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: []string{
		"XAI_API_KEY",
		"XAI_BASE_URL",
	}}
}

func opencodeCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: append(directModelProviderEnvironmentKeys(),
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
		"XDG_CACHE_HOME",
	)}
}

func mimoCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: directModelProviderEnvironmentKeys()}
}

func piCLIEnvironmentPolicy() modelCLIEnvironmentPolicy {
	return modelCLIEnvironmentPolicy{allow: directModelProviderEnvironmentKeys()}
}

// directModelProviderEnvironmentKeys is deliberately explicit. OpenCode,
// MiMo, and Pi support many direct model providers, but arbitrary environment
// interpolation would turn any daemon variable into a credential channel.
// Custom providers must use their sandbox-visible provider config/auth files;
// adding a new inherited variable requires a reviewed entry here.
func directModelProviderEnvironmentKeys() []string {
	return []string{
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
}

func modelCLISandboxConfig(
	binary, workspacePath, scratchPath string,
	policy modelCLISandboxPolicy,
	extraReadOnly ...string,
) sandbox.Config {
	cfg := sandbox.Config{
		Network:        sandbox.NetworkHost,
		ReadOnlyPaths:  existingPaths(policy.readOnly),
		ReadWritePaths: existingPaths(policy.readWrite),
		// Keep write-denies even when a protected file does not exist yet.
		// This prevents a CLI with a writable provider root from creating a
		// new hook/config/credential file after the profile is assembled.
		DenyWritePaths: nonEmptyPaths(policy.denyWrite),
		DenyPaths: append(modelCLIHardDenyPaths(),
			existingPaths(policy.deny)...),
	}
	workingDir := workspacePath
	if workingDir == "" {
		workingDir = scratchPath
	}
	return sandbox.PrepareCommandConfig(cfg, binary, workingDir, scratchPath, extraReadOnly...)
}

// modelCLIHardDenyPaths supplements sandbox.DefaultDenyPaths with cloud,
// package-manager, GPG, and GitHub credentials. These paths remain invisible
// even if a future provider policy grants a broader parent directory.
func modelCLIHardDenyPaths() []string {
	return []string{
		homeRelative(".ssh"),
		homeRelative(".aws"),
		homeRelative(".mcplexer"),
		homeRelative(".docker/config.json"),
		homeRelative(".gnupg"),
		homeRelative(".kube"),
		homeRelative(".config/gh"),
		homeRelative(".config/gcloud"),
		homeRelative(".config/oci"),
		homeRelative(".azure"),
		homeRelative(".terraform.d"),
		homeRelative(".npmrc"),
		homeRelative(".pypirc"),
		homeRelative(".netrc"),
	}
}

func claudeCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readWrite: []string{
			// claude_cli writes an IPC/lock dir at a fixed /tmp/claude-<uid>
			// (ignoring TMPDIR). Under the sandbox a denied stat makes it
			// believe the dir is absent, then its non-recursive mkdir fails
			// with EEXIST. Grant that exact dir read-write.
			claudeCLITempDir(),
		},
		denyWrite: []string{
			homeRelative(".claude"),
			homeRelative(".claude.json"),
		},
	}, extraReadOnly...)
}

// claudeCLITempDir returns the fixed per-user IPC directory the claude
// CLI creates under the system temp root.
func claudeCLITempDir() string {
	return filepath.Join("/tmp", fmt.Sprintf("claude-%d", os.Getuid()))
}

func opencodeCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	data := xdgPath("XDG_DATA_HOME", ".local/share", "opencode")
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readOnly: []string{
			homeRelative(".opencode"),
			xdgPath("XDG_CONFIG_HOME", ".config", "opencode"),
		},
		readWrite: []string{
			data,
			xdgPath("XDG_CACHE_HOME", ".cache", "opencode"),
			// opencode (Bun) writes session/lock state here and aborts
			// with EEXIST/mkdir errors when the dir is outside the
			// sandbox — the XDG state dir is separate from data/cache.
			xdgPath("XDG_STATE_HOME", ".local/state", "opencode"),
		},
		denyWrite: []string{
			filepath.Join(data, "auth.json"),
			filepath.Join(data, "account.json"),
		},
	}, extraReadOnly...)
}

func codexCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	root := homeRelative(".codex")
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readWrite: []string{root},
		denyWrite: []string{
			filepath.Join(root, "auth.json"),
			filepath.Join(root, "config.toml"),
			filepath.Join(root, "rules"),
			filepath.Join(root, "AGENTS.md"),
		},
	}, extraReadOnly...)
}

func geminiCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	root := homeRelative(".gemini")
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readOnly: []string{root},
		readWrite: []string{
			filepath.Join(root, "tmp"),
			filepath.Join(root, "history"),
		},
		denyWrite: []string{
			filepath.Join(root, "oauth_creds.json"),
			filepath.Join(root, "google_accounts.json"),
			filepath.Join(root, "settings.json"),
			filepath.Join(root, "trustedFolders.json"),
			filepath.Join(root, "projects.json"),
			filepath.Join(root, "state.json"),
			filepath.Join(root, "installation_id"),
			filepath.Join(root, "GEMINI.md"),
		},
		deny: []string{
			filepath.Join(root, "antigravity"),
			filepath.Join(root, "antigravity-browser-profile"),
		},
	}, extraReadOnly...)
}

func grokCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	root := homeRelative(".grok")
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readOnly: []string{root},
		readWrite: []string{
			filepath.Join(root, "logs"),
			filepath.Join(root, "debug"),
			filepath.Join(root, "memtrace"),
			filepath.Join(root, "upload_queue"),
			filepath.Join(root, "active_sessions.json"),
			filepath.Join(root, "active_sessions.lock"),
			filepath.Join(root, ".config-init.lock"),
			filepath.Join(root, "managed_config.lock"),
			filepath.Join(root, "auth.json.lock"),
			filepath.Join(root, "trusted_folders.toml.lock"),
			// grok headless refuses to start without writing its own
			// per-run session + project state ("Couldn't create
			// session: Permission denied" / FS_PERMISSION_DENIED). These
			// are the worker's own transcripts under the user's home, not
			// secrets — auth/config below stay write-denied.
			filepath.Join(root, "sessions"),
			filepath.Join(root, "projects"),
		},
		denyWrite: []string{
			filepath.Join(root, "auth.json"),
			filepath.Join(root, "config.toml"),
			filepath.Join(root, "trusted_folders.toml"),
			filepath.Join(root, "hooks"),
		},
	}, extraReadOnly...)
}

func mimoCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readOnly: []string{
			homeRelative(".mimo"),
			homeRelative(".mimocode"),
			homeRelative(".config/mimo"),
			homeRelative(".config/mimocode"),
		},
		readWrite: []string{
			homeRelative(".cache/mimo"),
			homeRelative(".cache/mimocode"),
			homeRelative(".local/share/mimo"),
			homeRelative(".local/share/mimocode"),
			// mimo (Bun) writes session/lock state to the XDG state dir,
			// separate from share/cache, and aborts with EEXIST mkdir when
			// it is outside the sandbox.
			homeRelative(".local/state/mimo"),
			homeRelative(".local/state/mimocode"),
		},
	}, extraReadOnly...)
}

func piCLISandboxConfig(binary, workspacePath, scratchPath string, extraReadOnly ...string) sandbox.Config {
	root := homeRelative(".pi")
	return modelCLISandboxConfig(binary, workspacePath, scratchPath, modelCLISandboxPolicy{
		readOnly: []string{root},
		readWrite: []string{
			filepath.Join(root, "agent", "logs"),
			filepath.Join(root, "agent", "cache"),
		},
		denyWrite: []string{
			filepath.Join(root, "agent", "auth.json"),
			filepath.Join(root, "agent", "models.json"),
			filepath.Join(root, "agent", "settings.json"),
			filepath.Join(root, "agent", "trust.json"),
		},
	}, extraReadOnly...)
}

// homeRelative joins the current user's home with a slash-separated suffix.
// Empty paths are discarded by the sandbox config helpers.
func homeRelative(suffix string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(suffix))
}

func xdgPath(envVar, fallback string, elems ...string) string {
	root := os.Getenv(envVar)
	if root == "" || !filepath.IsAbs(root) {
		root = homeRelative(fallback)
	}
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elems...)...)
}

func existingPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

func nonEmptyPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}
