package lmstudio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
