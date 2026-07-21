package lmstudio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/sandbox"
)

func TestRunLMSRequiresExplicitOptInBeforeBinaryResolution(t *testing.T) {
	m := &Manager{enabled: false, lmsPath: "/nonexistent/lms"}
	_, err := m.runLMS(context.Background(), time.Second, "ls")
	if err == nil || !strings.Contains(err.Error(), AllowEnvVar+"=1") {
		t.Fatalf("runLMS disabled error = %v, want %s opt-in guidance", err, AllowEnvVar)
	}
	if strings.Contains(err.Error(), "/nonexistent/lms") {
		t.Fatalf("runLMS resolved binary before opt-in check: %v", err)
	}
}

func TestResolveLMSBinaryCandidateRequiresExecutableFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveLMSBinaryCandidate(dir, "test"); err == nil || !strings.Contains(err.Error(), "executable regular file") {
		t.Fatalf("directory candidate error = %v", err)
	}
	file := filepath.Join(dir, "lms")
	if err := os.WriteFile(file, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLMSBinaryCandidate(file, "test"); err == nil || !strings.Contains(err.Error(), "executable regular file") {
		t.Fatalf("non-executable candidate error = %v", err)
	}
}

func TestLMSSandboxConfigIsScopedAndOptInNetworked(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	lmRoot := filepath.Join(home, ".lmstudio")
	externalModels := filepath.Join(root, "model-library")
	for _, dir := range []string{
		filepath.Join(lmRoot, ".internal"),
		filepath.Join(lmRoot, "extensions"),
		filepath.Join(lmRoot, "server-logs"),
		filepath.Join(home, "Library", "Application Support", "LM Studio"),
		externalModels,
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(externalModels, filepath.Join(lmRoot, "models")); err != nil {
		t.Fatal(err)
	}
	canonicalExternalModels, err := filepath.EvalSymlinks(externalModels)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(lmRoot, "settings.json"),
		filepath.Join(lmRoot, ".internal", "lms-key-2"),
	} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin", "lms")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}

	deniedNetwork := lmsSandboxConfig(bin, scratch, false)
	if deniedNetwork.Network != sandbox.NetworkDeny {
		t.Fatalf("disabled network = %q, want deny", deniedNetwork.Network)
	}
	cfg := lmsSandboxConfig(bin, scratch, true)
	if cfg.Network != sandbox.NetworkHost {
		t.Fatalf("enabled network = %q, want host", cfg.Network)
	}
	if cfg.WorkingDir != scratch {
		t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, scratch)
	}
	for _, path := range []string{
		filepath.Join(lmRoot, "extensions"),
		filepath.Join(lmRoot, "server-logs"),
		canonicalExternalModels,
		scratch,
	} {
		if !sliceContains(cfg.ReadWritePaths, path) {
			t.Errorf("ReadWritePaths missing %q: %v", path, cfg.ReadWritePaths)
		}
	}
	if sliceContains(cfg.ReadWritePaths, lmRoot) {
		t.Fatalf("ReadWritePaths grants whole LM Studio root: %v", cfg.ReadWritePaths)
	}
	for _, path := range []string{
		filepath.Join(lmRoot, "settings.json"),
		filepath.Join(lmRoot, ".internal", "lms-key-2"),
	} {
		if !sliceContains(cfg.DenyWritePaths, path) {
			t.Errorf("DenyWritePaths missing %q: %v", path, cfg.DenyWritePaths)
		}
	}
	for _, paths := range [][]string{cfg.ReadOnlyPaths, cfg.ReadWritePaths, cfg.DenyWritePaths} {
		if sliceContains(paths, home) {
			t.Fatalf("sandbox grants blanket HOME access: %+v", cfg)
		}
	}
	denied := sandbox.MergeDenyPaths(home, cfg.DenyPaths)
	for _, path := range []string{
		filepath.Join(home, ".mcplexer"),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".config", "gh"),
	} {
		if !sliceContains(denied, path) {
			t.Errorf("DenyPaths missing %q: %v", path, denied)
		}
	}
}

func sliceContains(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

// TestModelIDValidationThroughLoadModel proves the identifier check is
// actually wired into the CLI entry points (not just the regex): valid
// ids must reach binary resolution, invalid ids must be rejected first.
func TestModelIDValidationThroughLoadModel(t *testing.T) {
	t.Parallel()
	m := &Manager{enabled: true, lmsPath: "/nonexistent/lms"}
	ctx := context.Background()

	valid := []string{
		"qwen/qwen3-8b",
		"llama-3.2-1b-instruct",
		"mistralai/Mistral-7B-Instruct-v0.3",
		"model@q4_k_m",
		"a:b/c.d_e-f",
	}
	for _, id := range valid {
		// Valid ids pass validation and fail later at binary resolution
		// (lmsPath points nowhere), proving the id check did not reject.
		if _, err := m.LoadModel(ctx, id); err == nil {
			t.Errorf("LoadModel(%q): expected binary-resolution error, got nil", id)
		} else if !strings.Contains(err.Error(), "/nonexistent/lms") {
			t.Errorf("LoadModel(%q): rejected before exec: %v", id, err)
		}
	}

	invalid := []string{
		"",
		"-leading-dash",
		".hidden",
		"model name with spaces",
		"model;rm -rf /",
		"model$(whoami)",
		"model`id`",
		"model|pipe",
		"../escape",
	}
	for _, id := range invalid {
		if _, err := m.LoadModel(ctx, id); err == nil || !strings.Contains(err.Error(), "invalid model identifier") {
			t.Errorf("LoadModel(%q): want invalid-identifier error, got %v", id, err)
		}
	}
}

func TestLoadedModels(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen/qwen3-8b"},{"id":"llama-3.2-1b"}]}`))
	}))
	defer srv.Close()

	m := &Manager{enabled: true, endpoint: srv.URL, client: srv.Client()}
	ids, up, err := m.LoadedModels(context.Background())
	if err != nil {
		t.Fatalf("LoadedModels: %v", err)
	}
	if !up {
		t.Fatal("LoadedModels: server should report up")
	}
	if len(ids) != 2 || ids[0] != "qwen/qwen3-8b" || ids[1] != "llama-3.2-1b" {
		t.Fatalf("LoadedModels: unexpected ids %v", ids)
	}
}

func TestLoadedModelsServerDown(t *testing.T) {
	t.Parallel()
	// Port 1 is never listening: connection refused, which must be
	// reported as down (up=false), not as an error.
	m := &Manager{
		enabled:  true,
		endpoint: "http://127.0.0.1:1",
		client:   &http.Client{Timeout: 500 * time.Millisecond},
	}
	ids, up, err := m.LoadedModels(context.Background())
	if err != nil {
		t.Fatalf("LoadedModels on down server: unexpected error %v", err)
	}
	if up || ids != nil {
		t.Fatalf("LoadedModels on down server: want down/nil, got up=%v ids=%v", up, ids)
	}
}

func TestNewManagerFromEnv(t *testing.T) {
	t.Setenv(AllowEnvVar, "")
	t.Setenv(EndpointEnvVar, "")
	if m := NewManagerFromEnv(); m.Enabled() {
		t.Error("manager enabled without opt-in env")
	} else if m.Endpoint() != defaultEndpoint {
		t.Errorf("default endpoint = %q, want %q", m.Endpoint(), defaultEndpoint)
	}

	t.Setenv(AllowEnvVar, "1")
	t.Setenv(EndpointEnvVar, "http://192.0.2.5:9999/")
	m := NewManagerFromEnv()
	if !m.Enabled() {
		t.Error("manager not enabled with opt-in env set")
	}
	if m.Endpoint() != "http://192.0.2.5:9999" {
		t.Errorf("endpoint = %q, want trailing slash trimmed", m.Endpoint())
	}
}
