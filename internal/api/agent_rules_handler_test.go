package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func newAgentRulesMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAgentRulesRoutes(mux)
	return mux
}

func testRulesPath(t *testing.T, parts ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	all := append([]string{home, ".claude", "test-agent-rules"}, parts...)
	return filepath.Join(all...)
}

func TestAgentRulesStatusMissingFile(t *testing.T) {
	path := testRulesPath(t, "status-missing", "CLAUDE.md")
	mux := newAgentRulesMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-rules/status?path="+url.QueryEscape(path), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", w.Code, w.Body.String())
	}
	var resp agentRulesStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Present || resp.UpToDate {
		t.Errorf("expected missing file to be present=false up_to_date=false; got %+v", resp)
	}
	if resp.Path != path {
		t.Errorf("path echoed wrong: %q vs %q", resp.Path, path)
	}
	if resp.LatestVersion < 1 {
		t.Errorf("latest_version should be >= 1; got %d", resp.LatestVersion)
	}
}

func TestAgentRulesSyncCreatesThenIdempotent(t *testing.T) {
	path := testRulesPath(t, "sync-e2e", "CLAUDE.md")
	mux := newAgentRulesMux(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-rules/sync?path="+url.QueryEscape(path), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", w.Code, w.Body.String())
	}
	var resp agentRulesSyncResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Changed {
		t.Errorf("first sync should report changed=true")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/agent-rules/sync?path="+url.QueryEscape(path), nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", w2.Code, w2.Body.String())
	}
	var resp2 agentRulesSyncResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode2: %v", err)
	}
	if resp2.Changed {
		t.Errorf("second sync should be no-op (changed=false); got %+v", resp2)
	}

	sreq := httptest.NewRequest(http.MethodGet, "/api/v1/agent-rules/status?path="+url.QueryEscape(path), nil)
	sw := httptest.NewRecorder()
	mux.ServeHTTP(sw, sreq)
	var status agentRulesStatusResponse
	if err := json.NewDecoder(sw.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Present || !status.UpToDate {
		t.Errorf("post-sync status should be present + up_to_date; got %+v", status)
	}
}

func TestAgentRulesSyncRejectsRelativePathOverride(t *testing.T) {
	mux := newAgentRulesMux(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-rules/sync?path=relative/path.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for relative path override; got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAgentRulesSyncRejectsArbitraryAbsPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mux := newAgentRulesMux(t)

	for _, badPath := range []string{
		"/etc/passwd",
		"/tmp/evil.md",
		filepath.Join(os.TempDir(), "evil.md"),
		"/var/log/syslog",
		"/usr/local/bin/payload",
	} {
		t.Run(badPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-rules/sync?path="+url.QueryEscape(badPath), nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for arbitrary abs path %q; got %d body=%s", badPath, w.Code, w.Body.String())
			}
		})
	}
}

func TestAgentRulesStatusRejectsArbitraryAbsPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mux := newAgentRulesMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent-rules/status?path="+url.QueryEscape("/etc/shadow"), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for /etc/shadow; got %d body=%s", w.Code, w.Body.String())
	}
}

func TestResolveRulesPathAllowsOnlyAgentRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	allowed := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".claude", "nested", "custom.md"),
		filepath.Join(home, ".mcplexer", "rules", "CLAUDE.md"),
	}
	for _, p := range allowed {
		t.Run("allow_"+filepath.Base(p), func(t *testing.T) {
			got, err := resolveRulesPath(p)
			if err != nil {
				t.Fatalf("resolveRulesPath(%q): %v", p, err)
			}
			if got != filepath.Clean(p) {
				t.Fatalf("got %q, want %q", got, filepath.Clean(p))
			}
		})
	}

	denied := []string{
		filepath.Join(home, ".claude2", "CLAUDE.md"),
		filepath.Join(home, "Documents", "CLAUDE.md"),
		filepath.Join(home, ".ssh", "authorized_keys"),
		filepath.Join(home, ".claude", "..", ".ssh", "authorized_keys"),
	}
	for _, p := range denied {
		t.Run("deny_"+filepath.Base(p), func(t *testing.T) {
			if _, err := resolveRulesPath(p); err == nil {
				t.Fatalf("resolveRulesPath(%q) succeeded, want error", p)
			}
		})
	}
}

func TestResolveRulesPathDefaultIsHomeClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := resolveRulesPath("")
	if err != nil {
		t.Fatalf("resolveRulesPath default: %v", err)
	}
	want := filepath.Join(home, ".claude", "CLAUDE.md")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
}
