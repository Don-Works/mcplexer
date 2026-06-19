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
	h := spaFallback(fsys, http.FileServerFS(fsys), "")

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
	h := spaFallback(fsys, http.FileServerFS(fsys), "")

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

// TestSPAFallback_SWVersionBumped ensures sw.js contains a version string
// that differs from the old v4, confirming the cache-busting bump landed.
func TestSPAFallback_SWVersionBumped(t *testing.T) {
	sw, err := os.ReadFile("../../web/public/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sw), "mcplexer-shell-v4") {
		t.Error("sw.js CACHE_NAME still uses old v4; expected v5 bump")
	}
}
