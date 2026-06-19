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
	"regexp"
	"strings"
	"time"
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
		if _, err := os.Stat(m.lmsPath); err != nil {
			return "", fmt.Errorf("%s=%s: %w", PathEnvVar, m.lmsPath, err)
		}
		return m.lmsPath, nil
	}
	p, err := exec.LookPath("lms")
	if err != nil {
		return "", fmt.Errorf("lms CLI not found on PATH (install LM Studio, then run lms bootstrap): %w", err)
	}
	return p, nil
}

func (m *Manager) runLMS(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	bin, err := m.resolveBinary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	text := string(out)
	if len(text) > maxCLIOutput {
		text = text[:maxCLIOutput] + "\n... (output truncated)"
	}
	if err != nil {
		return text, fmt.Errorf("lms %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
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
