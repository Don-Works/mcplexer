package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSPAFallback_CacheControl asserts that the cache headers are correct
// for the three categories that matter for PWA freshness:
//
//   - sw.js must get no-cache so the browser always revalidates the version.
//   - index.html (HTML) must get no-store so upgrades aren't masked by stale shells.
//   - assets/* must get immutable long-cache so hashed bundles aren't revalidated.
func TestSPAFallback_CacheControl(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html>")},
		"sw.js":                &fstest.MapFile{Data: []byte("// sw")},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte("{}")},
		"assets/app.js":        &fstest.MapFile{Data: []byte("// app")},
		"icon.svg":             &fstest.MapFile{Data: []byte("<svg/>")},
	}
	h := spaFallback(fsys, http.FileServerFS(fsys), "", "")

	cases := []struct {
		path             string
		wantCacheControl string
	}{
		{"/sw.js", "no-cache"},
		{"/index.html", "no-store, must-revalidate"},
		{"/", "no-store, must-revalidate"},
		{"/assets/app.js", "public, max-age=31536000, immutable"},
		{"/manifest.webmanifest", ""}, // no explicit header, falls through
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("Cache-Control")
			if got != c.wantCacheControl {
				t.Errorf("Cache-Control=%q want %q", got, c.wantCacheControl)
			}
		})
	}
}

// TestSPAFallback_MissingAssetsReturn404 ensures that a request for a
// missing /assets/* or /sw.js returns 404 (not the SPA index.html which
// would cause strict-MIME mismatch).
func TestSPAFallback_MissingAssetsReturn404(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>")},
	}
	h := spaFallback(fsys, http.FileServerFS(fsys), "", "")

	cases := []string{"/assets/missing.js", "/sw.js", "/app.css"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status=%d want 404", rec.Code)
			}
		})
	}
}

// TestSPAFallback_SWVersionBumped ensures sw.js contains the current shell
// version, confirming cache-busting bumps land alongside service-worker edits.
func TestSPAFallback_SWVersionBumped(t *testing.T) {
	sw, err := os.ReadFile("../../web/public/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sw), "mcplexer-shell-v7") {
		t.Error("sw.js CACHE_NAME still uses old v7")
	}
	if strings.Contains(string(sw), "mcplexer-shell-v8'") || strings.Contains(string(sw), `mcplexer-shell-v8"`) {
		t.Error("sw.js CACHE_NAME still uses old v8")
	}
	if !strings.Contains(string(sw), "mcplexer-shell-v12") {
		t.Error("sw.js CACHE_NAME should use v12")
	}
}

func TestSPAFallback_RedirectsNonLoopbackHTTPToPublicURL(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>")},
	}
	h := spaFallback(fsys, http.FileServerFS(fsys), "", "https://dev-laptop-a.example.ts.net")

	req := httptest.NewRequest(http.MethodGet, "http://dev-laptop-a:13333/app?source=pwa", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusTemporaryRedirect)
	}
	if got, want := rec.Header().Get("Location"), "https://dev-laptop-a.example.ts.net/app?source=pwa"; got != want {
		t.Fatalf("Location=%q want %q", got, want)
	}
}

func TestSPAFallback_DoesNotRedirectLoopbackHTTP(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>")},
	}
	h := spaFallback(fsys, http.FileServerFS(fsys), "", "https://dev-laptop-a.example.ts.net")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:13333/app", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}
