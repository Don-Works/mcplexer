package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestToolsHandlerListEmpty confirms the endpoint returns [] rather
// than null when no downstreams are configured (the workers UI uses
// [].length and would crash on null).
func TestToolsHandlerListEmpty(t *testing.T) {
	srv := newToolsTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/api/v1/tools")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var rows []toolListItem
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty list, got %d rows", len(rows))
	}
}

// TestToolsHandlerListFromCache exercises the happy path: two
// downstreams, each with a capabilities cache containing a couple of
// tools — the response carries every tool with the namespace
// prepended where the raw name lacks one, and the write_class flag
// matches the heuristic.
func TestToolsHandlerListFromCache(t *testing.T) {
	githubCache := []byte(`{"tools":[
		{"name":"list_issues","description":"list"},
		{"name":"create_issue","description":"create new issue"}
	]}`)
	meshCache := []byte(`{"tools":[
		{"name":"send","description":"send"},
		{"name":"receive","description":"receive"}
	]}`)
	srv := newToolsTestServer(t, []store.DownstreamServer{
		{
			ID: "ds-github", Name: "github", Transport: "stdio",
			ToolNamespace: "github", CapabilitiesCache: githubCache,
		},
		{
			ID: "ds-mesh", Name: "mesh", Transport: "internal",
			ToolNamespace: "mesh", CapabilitiesCache: meshCache,
		},
	})

	resp, err := http.Get(srv.URL + "/api/v1/tools")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var rows []toolListItem
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 tools, got %d: %+v", len(rows), rows)
	}
	byName := map[string]toolListItem{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r, ok := byName["github__list_issues"]; !ok || r.WriteClass {
		t.Errorf("github__list_issues: missing or wrongly write-class: %+v", r)
	}
	if r, ok := byName["github__create_issue"]; !ok || !r.WriteClass {
		t.Errorf("github__create_issue: missing or not flagged write-class: %+v", r)
	}
	if r, ok := byName["mesh__send"]; !ok || !r.WriteClass {
		t.Errorf("mesh__send: missing or not flagged write-class: %+v", r)
	}
	if r, ok := byName["mesh__receive"]; !ok || r.WriteClass {
		t.Errorf("mesh__receive: missing or wrongly write-class: %+v", r)
	}
}

// TestIsWriteClassTool keeps the heuristic in lockstep with the
// runner's dispatcher classification — adding a new prefix here MUST
// be reflected in cmd/mcplexer/workers_dispatcher.go's
// classifyWriteTool to keep the UI label honest at dispatch time.
func TestIsWriteClassTool(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"github__list_issues", false},
		{"github__create_issue", true},
		{"github__delete_issue", true},
		{"mesh__send", true},
		{"mesh__receive", false},
		{"linear__update_status", true},
		{"linear__get_issue", false},
		{"set", true},
		{"setup_thing", false}, // "setup_" is not a write prefix
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsWriteClassTool(tc.name); got != tc.want {
				t.Errorf("IsWriteClassTool(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func newToolsTestServer(t *testing.T, servers []store.DownstreamServer) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := range servers {
		if err := db.CreateDownstreamServer(ctx, &servers[i]); err != nil {
			t.Fatalf("seed downstream: %v", err)
		}
	}
	r := NewRouter(RouterDeps{
		APIToken: "",
		Store:    db,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}
