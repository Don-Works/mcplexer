// opencode_handlers_test.go drives the HTTP shim with a fake manager.
// We don't go through NewRouter here because router.go (per the spec)
// isn't ours to edit — the main session will wire the routes. Tests
// register the handler methods directly on a fresh ServeMux instead.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/opencode"
)

// fakeOpenCode satisfies the opencodeManager interface so handler
// tests stay decoupled from the real subprocess layer.
type fakeOpenCode struct {
	status     opencode.Status
	models     []string
	cacheAge   time.Duration
	startErr   error
	stopErr    error
	listErr    error
	startCnt   int
	stopCnt    int
	listCnt    int
	refreshCnt int
	postStart  opencode.Status // status snapshot returned after successful Start
}

func (f *fakeOpenCode) Start(_ context.Context) error {
	f.startCnt++
	if f.startErr != nil {
		return f.startErr
	}
	if (f.postStart != opencode.Status{}) {
		f.status = f.postStart
	}
	return nil
}

func (f *fakeOpenCode) Stop() error {
	f.stopCnt++
	return f.stopErr
}

func (f *fakeOpenCode) Status() opencode.Status { return f.status }
func (f *fakeOpenCode) CacheAge() time.Duration { return f.cacheAge }
func (f *fakeOpenCode) ListModels(_ context.Context) ([]string, error) {
	f.listCnt++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.models, nil
}

func (f *fakeOpenCode) RefreshModels(_ context.Context) ([]string, error) {
	f.refreshCnt++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.models, nil
}

// newOpenCodeTestServer mounts the OpenCodeHandlers on a fresh mux so
// we don't go through router.go (which the spec forbids editing).
func newOpenCodeTestServer(t *testing.T, m opencodeManager) *httptest.Server {
	t.Helper()
	h := &OpenCodeHandlers{Manager: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/opencode/status", h.Status)
	mux.HandleFunc("POST /api/v1/opencode/start", h.Start)
	mux.HandleFunc("POST /api/v1/opencode/stop", h.Stop)
	mux.HandleFunc("GET /api/v1/opencode/models", h.Models)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenCodeStatusReturnsManagerStatus(t *testing.T) {
	f := &fakeOpenCode{status: opencode.Status{
		Installed:  true,
		Running:    false,
		BinaryPath: "/usr/local/bin/opencode",
		Version:    "opencode 0.99.0",
	}}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("status code: got %d, want 200", res.StatusCode)
	}
	var got opencode.Status
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BinaryPath != "/usr/local/bin/opencode" || !got.Installed {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestOpenCodeStartNotInstalledReturns409(t *testing.T) {
	f := &fakeOpenCode{startErr: opencode.ErrNotInstalled}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Post(srv.URL+"/api/v1/opencode/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", res.StatusCode)
	}
	var body errorResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode err body: %v", err)
	}
	if body.Error == "" {
		t.Fatalf("expected non-empty error message, got %+v", body)
	}
}

func TestOpenCodeStartSuccessReturnsRunningStatus(t *testing.T) {
	f := &fakeOpenCode{
		status: opencode.Status{Installed: true, Running: false},
		postStart: opencode.Status{
			Installed:  true,
			Running:    true,
			Port:       4096,
			BinaryPath: "/fake/opencode",
		},
	}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Post(srv.URL+"/api/v1/opencode/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("status code: got %d, want 200", res.StatusCode)
	}
	var got opencode.Status
	_ = json.NewDecoder(res.Body).Decode(&got)
	if !got.Running || got.Port != 4096 {
		t.Fatalf("expected running on 4096, got %+v", got)
	}
	if f.startCnt != 1 {
		t.Fatalf("expected Start called once, got %d", f.startCnt)
	}
}

func TestOpenCodeStartGenericErrorReturns502(t *testing.T) {
	f := &fakeOpenCode{startErr: errors.New("readiness timed out after 30s")}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Post(srv.URL+"/api/v1/opencode/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status code: got %d, want 502", res.StatusCode)
	}
}

func TestOpenCodeStopIsIdempotent(t *testing.T) {
	f := &fakeOpenCode{}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Post(srv.URL+"/api/v1/opencode/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("status code: got %d, want 200", res.StatusCode)
	}
}

func TestOpenCodeModelsSuccessUncached(t *testing.T) {
	f := &fakeOpenCode{
		models:   []string{"anthropic/claude-opus-4-7", "openai/gpt-4o"},
		cacheAge: 0,
	}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/models")
	if err != nil {
		t.Fatalf("GET models: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("status code: got %d, want 200", res.StatusCode)
	}
	var got modelsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Cached {
		t.Fatalf("expected Cached=false on first call, got true")
	}
	if len(got.Models) != 2 {
		t.Fatalf("expected 2 models, got %v", got.Models)
	}
}

func TestOpenCodeModelsSuccessCached(t *testing.T) {
	f := &fakeOpenCode{
		models:   []string{"a/b"},
		cacheAge: 30 * time.Second,
	}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/models")
	if err != nil {
		t.Fatalf("GET models: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var got modelsResponse
	_ = json.NewDecoder(res.Body).Decode(&got)
	if !got.Cached {
		t.Fatalf("expected Cached=true when CacheAge>0, got %+v", got)
	}
}

func TestOpenCodeModelsErrorReturns502(t *testing.T) {
	f := &fakeOpenCode{listErr: errors.New("opencode models: exit status 1")}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/models")
	if err != nil {
		t.Fatalf("GET models: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status code: got %d, want 502", res.StatusCode)
	}
	var body errorResponse
	_ = json.NewDecoder(res.Body).Decode(&body)
	if !strings.HasPrefix(body.Details, "see server logs; request_id=") {
		t.Fatalf("expected opaque log reference, got %+v", body)
	}
	if strings.Contains(body.Details, "exit status") {
		t.Fatalf("internal command error leaked to caller: %+v", body)
	}
}

func TestOpenCodeModelsNotInstalledReturns409(t *testing.T) {
	f := &fakeOpenCode{listErr: opencode.ErrNotInstalled}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/models")
	if err != nil {
		t.Fatalf("GET models: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", res.StatusCode)
	}
}

func TestOpenCodeModelsRefreshBustsCache(t *testing.T) {
	f := &fakeOpenCode{
		models:   []string{"a/b"},
		cacheAge: 30 * time.Second, // warm cache that refresh must bypass
	}
	srv := newOpenCodeTestServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/opencode/models?refresh=1")
	if err != nil {
		t.Fatalf("GET models?refresh=1: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var got modelsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if f.refreshCnt != 1 || f.listCnt != 0 {
		t.Fatalf("refresh=1 must call RefreshModels not ListModels: refresh=%d list=%d",
			f.refreshCnt, f.listCnt)
	}
	if got.Cached {
		t.Fatalf("forced refresh must report Cached=false, got %+v", got)
	}
}
