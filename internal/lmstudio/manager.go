// Package lmstudio exposes opt-in tools for managing a local LM Studio
// instance. Inference is not proxied here; once the server is up, workers can
// use the normal openai_compat provider against http://localhost:1234/v1.
package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/sandbox"
)

const (
	// AllowEnvVar gates the integration because the lms CLI can spawn host
	// processes and download models over the network.
	AllowEnvVar = "MCPLEXER_ALLOW_LMSTUDIO"
	// PathEnvVar overrides the lms binary location.
	PathEnvVar = "MCPLEXER_LMS_PATH"
	// EndpointEnvVar overrides the LM Studio server base URL.
	EndpointEnvVar = "MCPLEXER_LMSTUDIO_ENDPOINT"

	defaultEndpoint = "http://localhost:1234"
	cliTimeout      = 60 * time.Second
	downloadTimeout = 30 * time.Minute
	maxCLIOutput    = 16 * 1024
)

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)

// Manager shells out to the lms CLI and probes the local LM Studio server.
type Manager struct {
	enabled  bool
	lmsPath  string
	endpoint string
	client   *http.Client
}

// NewManagerFromEnv constructs a Manager from daemon environment.
func NewManagerFromEnv() *Manager {
	endpoint := strings.TrimRight(os.Getenv(EndpointEnvVar), "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Manager{
		enabled:  os.Getenv(AllowEnvVar) == "1",
		lmsPath:  os.Getenv(PathEnvVar),
		endpoint: endpoint,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reports whether the operator opted in via MCPLEXER_ALLOW_LMSTUDIO=1.
func (m *Manager) Enabled() bool { return m.enabled }

// Endpoint returns the base URL workers should use for openai_compat.
func (m *Manager) Endpoint() string { return m.endpoint }

func (m *Manager) resolveBinary() (string, error) {
	if m.lmsPath != "" {
		return resolveLMSBinaryCandidate(m.lmsPath, PathEnvVar+"="+m.lmsPath)
	}
	if p, err := exec.LookPath("lms"); err == nil {
		return resolveLMSBinaryCandidate(p, "lms")
	}
	for _, candidate := range lmsStandardPaths() {
		if _, err := os.Stat(candidate); err == nil {
			return resolveLMSBinaryCandidate(candidate, candidate)
		}
	}
	return "", fmt.Errorf("lms CLI not found (install LM Studio, then run lms bootstrap)")
}

func (m *Manager) runLMS(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if !m.enabled {
		return "", fmt.Errorf("LM Studio integration disabled; set %s=1 to opt in", AllowEnvVar)
	}
	bin, err := m.resolveBinary()
	if err != nil {
		return "", err
	}
	scratchPath, err := os.MkdirTemp("", "mcplexer-lms-*")
	if err != nil {
		return "", fmt.Errorf("create lms scratch directory: %w", err)
	}
	defer os.RemoveAll(scratchPath) //nolint:errcheck

	environmentBase := os.Environ()
	cfg := lmsSandboxConfig(
		bin, scratchPath, m.enabled,
		sandbox.CommandEnvironmentReadPaths(environmentBase)...,
	)
	wrapper := sandbox.NewCommandWrapper(cfg)
	program, wrappedArgs, cleanupSandbox := wrapper.Wrap(bin, args)
	defer cleanupSandbox()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, wrappedArgs...)
	cmd.Dir = scratchPath
	cmd.Env = sandbox.AllowlistedCommandEnvironment(environmentBase, scratchPath)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > maxCLIOutput {
		text = text[:maxCLIOutput] + "\n... (output truncated)"
	}
	if err != nil {
		return text, fmt.Errorf("lms %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func resolveLMSBinaryCandidate(candidate, label string) (string, error) {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("%s: resolve absolute path: %w", label, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%s: not an executable regular file", label)
	}
	return abs, nil
}

func lmsStandardPaths() []string {
	paths := []string{
		"/Applications/LM Studio.app/Contents/Resources/app/.webpack/lms",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append([]string{filepath.Join(home, ".lmstudio", "bin", "lms")}, paths...)
	}
	return paths
}

func lmsSandboxConfig(
	binary, scratchPath string, allowNetwork bool, extraReadOnly ...string,
) sandbox.Config {
	home, _ := os.UserHomeDir()
	lmRoot := lmsHomePath(home, ".lmstudio")
	appData := lmsHomePath(home, "Library", "Application Support", "LM Studio")
	modelPath := lmsRootPath(lmRoot, "models")
	readWrite := existingLMSPaths([]string{
		modelPath,
		lmsRootPath(lmRoot, "extensions"),
		lmsRootPath(lmRoot, "server-logs"),
		lmsRootPath(lmRoot, "working-directories"),
		lmsRootPath(lmRoot, "hub", "models"),
		lmsRootPath(lmRoot, "hub", "presets"),
		lmsRootPath(lmRoot, ".internal"),
	})
	if resolved, err := filepath.EvalSymlinks(modelPath); err == nil && resolved != modelPath {
		readWrite = append(readWrite, resolved)
	}
	readOnly := existingLMSPaths([]string{
		"/Applications/LM Studio.app/Contents",
	})
	// These paths are readable configuration/identity material but are
	// immutable to the manager subprocess. Keep the rules even when a file
	// is absent so a writable .internal directory cannot create it later.
	denyWrite := nonEmptyLMSPaths([]string{
		lmsRootPath(lmRoot, "settings.json"),
		lmsRootPath(lmRoot, "mcp.json"),
		lmsRootPath(lmRoot, "config-presets"),
		lmsRootPath(lmRoot, "credentials"),
		lmsRootPath(lmRoot, ".internal", "lms-key-2"),
		lmsRootPath(lmRoot, ".internal", "user-profile.json"),
		lmsRootPath(lmRoot, ".internal", "local-identity.json"),
		lmsRootPath(lmRoot, ".internal", "lm-link-config.json"),
		lmsRootPath(lmRoot, ".internal", "lm-link-account-status-cache.json"),
		lmsRootPath(lmRoot, ".internal", "last-synced-mcp-state.json"),
		lmsRootPath(lmRoot, ".internal", "artifact-permissions-list.json"),
		lmsRootPath(lmRoot, ".internal", "http-server-config.json"),
		lmsRootPath(lmRoot, ".internal", "app-install-location.json"),
		lmsRootPath(lmRoot, ".internal", "cli-pref.json"),
		lmsRootPath(lmRoot, ".internal", "conversation-config.json"),
		lmsRootPath(lmRoot, ".internal", "global-hotkeys"),
		lmsRootPath(lmRoot, ".internal", "user-concrete-model-default-config"),
		lmsRootPath(appData, "settings.json"),
		lmsRootPath(appData, "config.json"),
		lmsRootPath(appData, "Preferences"),
		lmsRootPath(appData, "Cookies"),
	})
	network := sandbox.NetworkDeny
	if allowNetwork {
		network = sandbox.NetworkHost
	}
	cfg := sandbox.Config{
		Network:        network,
		ReadOnlyPaths:  readOnly,
		ReadWritePaths: readWrite,
		DenyWritePaths: denyWrite,
		DenyPaths: []string{
			lmsHomePath(home, ".ssh"),
			lmsHomePath(home, ".aws"),
			lmsHomePath(home, ".mcplexer"),
			lmsHomePath(home, ".docker", "config.json"),
			lmsHomePath(home, ".gnupg"),
			lmsHomePath(home, ".kube"),
			lmsHomePath(home, ".config", "gh"),
			lmsHomePath(home, ".config", "gcloud"),
			lmsHomePath(home, ".config", "oci"),
			lmsHomePath(home, ".azure"),
			lmsHomePath(home, ".terraform.d"),
			lmsHomePath(home, ".npmrc"),
			lmsHomePath(home, ".pypirc"),
			lmsHomePath(home, ".netrc"),
		},
	}
	return sandbox.PrepareCommandConfig(cfg, binary, scratchPath, scratchPath, extraReadOnly...)
}

func lmsHomePath(home string, elems ...string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, elems...)...)
}

func lmsRootPath(root string, elems ...string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elems...)...)
}

func existingLMSPaths(paths []string) []string {
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

func nonEmptyLMSPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" && path != "." {
			out = append(out, path)
		}
	}
	return out
}

// LoadedModels queries the running server's /v1/models endpoint.
func (m *Manager) LoadedModels(ctx context.Context) (ids []string, up bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint+"/v1/models", nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, true, fmt.Errorf("decode /v1/models: %w", err)
	}
	for _, d := range body.Data {
		ids = append(ids, d.ID)
	}
	return ids, true, nil
}

// StartServer launches the LM Studio server via lms.
func (m *Manager) StartServer(ctx context.Context) (string, error) {
	out, err := m.runLMS(ctx, cliTimeout, "server", "start")
	if err != nil {
		return out, err
	}
	for range 15 {
		if _, up, _ := m.LoadedModels(ctx); up {
			return fmt.Sprintf("LM Studio server is up at %s.\n%s", m.endpoint, out), nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return out, fmt.Errorf("server started but %s/v1/models not answering after 15s", m.endpoint)
}

// StopServer stops the LM Studio server via lms.
func (m *Manager) StopServer(ctx context.Context) (string, error) {
	return m.runLMS(ctx, cliTimeout, "server", "stop")
}

// ListDownloaded returns the host's downloaded models via lms.
func (m *Manager) ListDownloaded(ctx context.Context) (string, error) {
	return m.runLMS(ctx, cliTimeout, "ls")
}

// LoadModel loads a downloaded model into memory.
func (m *Manager) LoadModel(ctx context.Context, model string) (string, error) {
	if !modelIDPattern.MatchString(model) {
		return "", fmt.Errorf("invalid model identifier %q", model)
	}
	return m.runLMS(ctx, 5*time.Minute, "load", model, "--yes")
}

// UnloadModel unloads a loaded model.
func (m *Manager) UnloadModel(ctx context.Context, model string) (string, error) {
	if !modelIDPattern.MatchString(model) {
		return "", fmt.Errorf("invalid model identifier %q", model)
	}
	return m.runLMS(ctx, cliTimeout, "unload", model)
}

// DownloadModel fetches a model from the LM Studio catalog.
func (m *Manager) DownloadModel(ctx context.Context, model string) (string, error) {
	if !modelIDPattern.MatchString(model) {
		return "", fmt.Errorf("invalid model identifier %q", model)
	}
	return m.runLMS(ctx, downloadTimeout, "get", model, "--yes")
}
