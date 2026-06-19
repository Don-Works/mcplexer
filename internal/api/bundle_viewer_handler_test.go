package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newBundleViewerTestServer(t *testing.T) (*httptest.Server, *skillregistry.Registry) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "bv.db"))
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

const bvBody = "---\nname: bv-demo\ndescription: Use when testing bundle viewer API.\n---\n# BV Demo\nBundle viewer test.\n"

func buildBVBundle(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"SKILL.md":        body,
		"scripts/run.mjs": "console.log('bv');\n",
		"docs/guide.md":   "# Guide\nTest guide.\n",
	}
	for name, content := range files {
		full := "bv-demo/" + name
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func TestBundleFileIndexAPI(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	bundle := buildBVBundle(t, bvBody)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: bvBody, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var entries []map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/bv-demo/bundle/files", &entries)
	if len(entries) != 3 {
		t.Fatalf("expected 3 files, got %d", len(entries))
	}

	paths := map[string]bool{}
	for _, e := range entries {
		if p, ok := e["path"].(string); ok {
			paths[p] = true
		}
		if _, ok := e["sha256"]; !ok {
			t.Errorf("missing sha256 in %v", e)
		}
		if _, ok := e["role"]; !ok {
			t.Errorf("missing role in %v", e)
		}
	}
	if !paths["SKILL.md"] {
		t.Error("SKILL.md not found in index")
	}
}

func TestBundleFileContentAPI(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	bundle := buildBVBundle(t, bvBody)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: bvBody, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var fc map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/bv-demo/bundle/file-content?path=scripts/run.mjs", &fc)

	if isText, _ := fc["is_text"].(bool); !isText {
		t.Error("scripts/run.mjs should be text")
	}
	if content, _ := fc["content"].(string); content == "" {
		t.Error("expected non-empty content")
	}
}

func TestBundleFileContentAPITraversal(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	bundle := buildBVBundle(t, bvBody)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: bvBody, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/skill-registry/bv-demo/bundle/file-content?path=../etc/passwd")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for traversal, got %d: %s", resp.StatusCode, body)
	}
}

func TestBundleFileContentAPIMissingPath(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	bundle := buildBVBundle(t, bvBody)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: bvBody, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/skill-registry/bv-demo/bundle/file-content")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing path, got %d", resp.StatusCode)
	}
}

func TestVersionDiffAPI(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	bundle1 := buildBVBundle(t, bvBody)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: bvBody, Bundle: bundle1,
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	body2 := "---\nname: bv-demo\ndescription: Revised version.\n---\n# BV Demo\nRevised.\n"
	bundle2 := buildBVBundle(t, body2)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name: "bv-demo", Body: body2, Bundle: bundle2,
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	var diff map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/bv-demo/diff?old_version=1&new_version=2", &diff)

	if oldV, _ := diff["old_version"].(float64); oldV != 1 {
		t.Errorf("old_version = %v, want 1", diff["old_version"])
	}
	if newV, _ := diff["new_version"].(float64); newV != 2 {
		t.Errorf("new_version = %v, want 2", diff["new_version"])
	}
	if _, ok := diff["body_diff"]; !ok {
		t.Error("missing body_diff in response")
	}
	if tree, ok := diff["tree"].([]any); !ok || len(tree) == 0 {
		t.Error("expected non-empty tree diff")
	}
}

func TestVersionDiffAPINoBundle(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	body1 := "---\nname: no-bun\ndescription: V1\n---\n# No Bundle V1\n"
	body2 := "---\nname: no-bun\ndescription: V2\n---\n# No Bundle V2\n"

	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "no-bun", Body: body1}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "no-bun", Body: body2}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	var diff map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/no-bun/diff?old_version=1&new_version=2", &diff)

	if diff["old_has_bundle"].(bool) {
		t.Error("old should not have bundle")
	}
	if diff["new_has_bundle"].(bool) {
		t.Error("new should not have bundle")
	}
	if diff["body_diff"] == "" {
		t.Error("expected body_diff for text-only skill")
	}
}

func TestVersionDiffAPIDefaultsToLatest(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	body := "---\nname: miss\ndescription: Test\n---\n# Body\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "miss", Body: body}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var diff map[string]any
	fetchJSON(t, srv.URL+"/api/v1/skill-registry/miss/diff", &diff)
	if oldV, _ := diff["old_version"].(float64); oldV != 1 {
		t.Errorf("old_version = %v, want 1", diff["old_version"])
	}
	if newV, _ := diff["new_version"].(float64); newV != 1 {
		t.Errorf("new_version = %v, want 1", diff["new_version"])
	}
}

func TestBundleFileIndexAPINoBundle(t *testing.T) {
	srv, reg := newBundleViewerTestServer(t)

	body := "---\nname: no-bun-idx\ndescription: No bundle\n---\n# Body\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "no-bun-idx", Body: body}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/skill-registry/no-bun-idx/bundle/files")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404 for no bundle, got %d: %s", resp.StatusCode, body)
	}
}
