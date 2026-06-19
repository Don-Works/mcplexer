// skill_registry_handler_test.go (W4) — verifies that the dashboard's
// REST surface surfaces the W4 manifest_extra fields as a first-class
// top-level key on every relevant response shape.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

const w4SkillBody = `---
name: w4-api-demo
description: Use when verifying that the API surfaces W4 frontmatter fields.
requires:
  - { binary: "ffmpeg" }
  - { env: "ANTHROPIC_API_KEY" }
produces:
  - "markdown"
consumes:
  - "screenshot"
phases:
  - "discover"
  - "draft"
refinement: "enabled"
---
# API surface demo

Body content for the W4 HTTP test.
`

func newSkillRegistryTestServer(t *testing.T) (*httptest.Server, *skillregistry.Registry) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := skillregistry.New(db)
	r := NewRouter(RouterDeps{
		Store:         db,
		SkillRegistry: reg,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, reg
}

// fetchJSON GETs the URL and decodes the body into v (which may be
// any type), failing the test on transport / decode errors.
func fetchJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test code
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d body=%s", url, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func TestSkillRegistryHandler_GetIncludesManifestExtra(t *testing.T) {
	srv, reg := newSkillRegistryTestServer(t)

	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "w4-api-demo", Body: w4SkillBody,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var got map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/w4-api-demo", &got)

	extras, ok := got["manifest_extra"].(map[string]any)
	if !ok {
		t.Fatalf("missing manifest_extra top-level key: %v", got)
	}
	if r, _ := extras["refinement"].(string); r != "enabled" {
		t.Errorf("expected refinement=enabled, got %v", extras["refinement"])
	}
	phases, ok := extras["phases"].([]any)
	if !ok || len(phases) != 2 {
		t.Errorf("expected 2 phases, got %v", extras["phases"])
	}
	reqs, ok := extras["requires"].([]any)
	if !ok || len(reqs) != 2 {
		t.Errorf("expected 2 requires entries, got %v", extras["requires"])
	}
}

func TestSkillRegistryHandler_ListIncludesManifestExtra(t *testing.T) {
	srv, reg := newSkillRegistryTestServer(t)

	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "w4-api-demo", Body: w4SkillBody,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var got []map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry", &got)
	if len(got) == 0 {
		t.Fatal("expected at least one skill in list")
	}
	found := false
	for _, e := range got {
		if e["name"] != "w4-api-demo" {
			continue
		}
		found = true
		extras, ok := e["manifest_extra"].(map[string]any)
		if !ok {
			t.Fatalf("missing manifest_extra in list element: %v", e)
		}
		if _, ok := extras["requires"]; !ok {
			t.Errorf("list element extras missing requires: %v", extras)
		}
	}
	if !found {
		t.Fatal("w4-api-demo not found in list response")
	}
}

func TestSkillRegistryHandler_AbsentExtraRendersAsEmptyObject(t *testing.T) {
	srv, reg := newSkillRegistryTestServer(t)

	body := "---\nname: noextra\ndescription: skill with no W4 fields\n---\n# body\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "noextra", Body: body,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var got map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/noextra", &got)
	extras, ok := got["manifest_extra"].(map[string]any)
	if !ok {
		t.Fatalf("manifest_extra absent or wrong type: %v", got)
	}
	if len(extras) != 0 {
		t.Errorf("expected empty extras object for skill without W4 fields, got %v", extras)
	}
}

func TestSkillRegistryHandler_VersionsIncludesAuthor(t *testing.T) {
	srv, reg := newSkillRegistryTestServer(t)

	body1 := strings.Replace(w4SkillBody, "w4-api-demo", "hist-author", 1)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "hist-author", Body: body1, Author: "alice",
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	body2 := strings.Replace(body1, "API surface demo", "Revised demo", 1)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "hist-author", Body: body2, Author: "bob",
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	var got []map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/hist-author/versions", &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(got))
	}
	authors := map[string]bool{}
	for _, row := range got {
		a, _ := row["author"].(string)
		authors[a] = true
		if _, ok := row["published_at"]; !ok {
			t.Errorf("missing published_at in %v", row)
		}
		if _, ok := row["version"]; !ok {
			t.Errorf("missing version in %v", row)
		}
	}
	if !authors["alice"] || !authors["bob"] {
		t.Errorf("expected alice and bob authors, got %v", authors)
	}
}

func TestSkillRegistryHandler_PublishViaAPIRoundTrip(t *testing.T) {
	srv, _ := newSkillRegistryTestServer(t)

	pub := map[string]any{
		"name": "w4-api-demo",
		"body": w4SkillBody,
	}
	payload, _ := json.Marshal(pub)
	resp, err := http.Post(srv.URL+"/api/v1/skill-registry", "application/json", bytes.NewReader(payload)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("publish status=%d body=%s", resp.StatusCode, body)
	}

	var got map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/w4-api-demo", &got)
	extras, ok := got["manifest_extra"].(map[string]any)
	if !ok {
		t.Fatalf("manifest_extra missing after publish: %v", got)
	}
	produces, _ := extras["produces"].([]any)
	if len(produces) != 1 || produces[0] != "markdown" {
		t.Errorf("expected produces=[markdown], got %v", produces)
	}
}
