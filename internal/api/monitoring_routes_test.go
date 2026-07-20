// monitoring_routes_test.go — the new monitoring routes exercised through the
// REAL router rather than by calling handlers directly.
//
// This exists for one specific failure mode. An /api/ path that no route
// matches does not 404: it falls through to the SPA handler and answers
// index.html with a 200. A gate implemented by conditionally registering the
// route would therefore hand the integration rig an HTML page and a success
// status when the gate is closed, which is a far worse outcome than being told
// no. Registering unconditionally and answering 404 inside the handler is the
// fix, and this test is what holds it in place.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newMonitoringRouteTestRouter(t *testing.T) http.Handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRouter(RouterDeps{Store: db})
}

func doMonitoringRoute(
	t *testing.T, router http.Handler, method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = "127.0.0.1:3333"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// TestTestIngestRoutesAre404WhenGated — closed gate, JSON 404, no SPA HTML.
func TestTestIngestRoutesAre404WhenGated(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_TEST_INGEST", "")
	router := newMonitoringRouteTestRouter(t)
	for _, path := range []string{
		"/api/v1/monitoring/test-ingest", "/api/v1/monitoring/test-tick",
	} {
		rr := doMonitoringRoute(t, router, http.MethodPost, path,
			`{"workspace_id":"ws-A","source_id":"src","lines":[{"message":"x"}]}`)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body %s)", path, rr.Code, rr.Body)
		}
		if strings.Contains(rr.Body.String(), "<!doctype") ||
			strings.Contains(rr.Body.String(), "<html") {
			t.Errorf("%s: answered SPA HTML instead of a JSON 404", path)
		}
	}
}

// TestMonitoringReadRoutesRegistered — the read endpoints must be reachable and
// must enforce workspace_id rather than falling through to the SPA.
func TestMonitoringReadRoutesRegistered(t *testing.T) {
	router := newMonitoringRouteTestRouter(t)
	for _, path := range []string{
		"/api/v1/monitoring/incidents", "/api/v1/monitoring/expected-signals",
	} {
		rr := doMonitoringRoute(t, router, http.MethodGet, path, "")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s without workspace_id: status = %d, want 400 (body %s)",
				path, rr.Code, rr.Body)
		}
	}
	rr := doMonitoringRoute(t, router, http.MethodGet,
		"/api/v1/monitoring/incidents?workspace_id=ws-A", "")
	if rr.Code != http.StatusOK {
		t.Errorf("incidents list: status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	rr = doMonitoringRoute(t, router, http.MethodGet,
		"/api/v1/monitoring/incidents/nope?workspace_id=ws-A", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown incident id: status = %d, want 404 (body %s)", rr.Code, rr.Body)
	}
}
