// audit_search_handler_test.go — HTTP-contract tests for the audit
// search / capabilities / alerts / saved-search REST surface. The
// frontend codes against these exact response shapes, so the assertions
// pin field names + types.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newAuditTestServer(t *testing.T) (*httptest.Server, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := NewRouter(RouterDeps{
		APIToken:  "",
		Store:     db,
		MemorySvc: memory.NewService(db, memory.NoopEmbedder{}, nil),
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db
}

func seedAuditAPI(t *testing.T, db *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	rows := []store.AuditRecord{
		{ToolName: "github__create_issue", Status: "success", WorkspaceName: "acme",
			ParamsRedacted: json.RawMessage(`{"title":"deploy failure"}`)},
		{ToolName: "slack__post", Status: "error", ErrorMessage: "deploy rate limit"},
		{ToolName: "postgres__query", Status: "error", ErrorMessage: "connection refused"},
	}
	for i := range rows {
		rows[i].Timestamp = base.Add(time.Duration(i) * time.Minute)
		rows[i].CreatedAt = rows[i].Timestamp
		if err := db.InsertAuditRecord(ctx, &rows[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

// getAuditJSON GETs url, asserts 200, and decodes the JSON body. (The
// package-level getJSON takes an explicit wantStatus; this wrapper pins
// 200 for the read-path assertions below.)
func getAuditJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	return getJSON(t, url, http.StatusOK)
}

// TestAuditSearchEndpoint pins the {data, total, mode, query} shape and
// the tfidf mode under the noop embedder (no vector tier).
func TestAuditSearchEndpoint(t *testing.T) {
	srv, db := newAuditTestServer(t)
	seedAuditAPI(t, db)

	body := getAuditJSON(t, srv.URL+"/api/v1/audit/search?q=deploy")
	if body["mode"] != "tfidf" {
		t.Fatalf("mode = %v, want tfidf", body["mode"])
	}
	if body["query"] != "deploy" {
		t.Fatalf("query = %v, want deploy", body["query"])
	}
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("data not an array: %T", body["data"])
	}
	if len(data) == 0 {
		t.Fatalf("expected deploy matches")
	}
}

// TestAuditCapabilitiesEndpoint pins the capability flags.
func TestAuditCapabilitiesEndpoint(t *testing.T) {
	srv, _ := newAuditTestServer(t)
	body := getAuditJSON(t, srv.URL+"/api/v1/audit/capabilities")
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search not an object: %T", body["search"])
	}
	if search["fts"] != true || search["tfidf"] != true {
		t.Fatalf("fts/tfidf must be true: %+v", search)
	}
	if search["vector"] != false {
		t.Fatalf("vector should be false under noop embedder: %v", search["vector"])
	}
	if body["alerts"] != true || body["saved_searches"] != true {
		t.Fatalf("alerts/saved_searches must be true: %+v", body)
	}
}

// TestAuditListNextCursor pins next_cursor presence on a full page and
// its absence on a short final page.
func TestAuditListNextCursor(t *testing.T) {
	srv, db := newAuditTestServer(t)
	seedAuditAPI(t, db)

	// limit 2 over 3 rows → full first page → next_cursor non-empty.
	body := getAuditJSON(t, srv.URL+"/api/v1/audit?limit=2")
	cur, _ := body["next_cursor"].(string)
	if cur == "" {
		t.Fatalf("expected non-empty next_cursor on full page")
	}
	// Following the cursor returns the remaining row, short page → empty.
	body2 := getAuditJSON(t, srv.URL+"/api/v1/audit?limit=2&cursor="+cur)
	if c2, _ := body2["next_cursor"].(string); c2 != "" {
		t.Fatalf("expected empty next_cursor on short final page, got %q", c2)
	}
	data2, _ := body2["data"].([]any)
	if len(data2) != 1 {
		t.Fatalf("cursor page 2 len = %d, want 1", len(data2))
	}
}

// TestAuditAlertsEndpoint pins the {alerts, generated_at} shape.
func TestAuditAlertsEndpoint(t *testing.T) {
	srv, db := newAuditTestServer(t)
	seedAuditAPI(t, db)
	body := getAuditJSON(t, srv.URL+"/api/v1/audit/alerts")
	if _, ok := body["alerts"].([]any); !ok {
		t.Fatalf("alerts not an array: %T", body["alerts"])
	}
	if _, ok := body["generated_at"].(string); !ok {
		t.Fatalf("generated_at not a string: %T", body["generated_at"])
	}
}

// TestAuditSavedSearchCRUDEndpoint walks create → list → patch → delete
// over HTTP and pins the {data: SavedSearch} envelope.
func TestAuditSavedSearchCRUDEndpoint(t *testing.T) {
	srv, _ := newAuditTestServer(t)

	// Create.
	createBody, _ := json.Marshal(map[string]any{
		"name": "errs", "q": "refused", "filter": map[string]any{"status": "error"},
		"threshold_count": 3, "window_sec": 300, "enabled": true,
	})
	resp, err := http.Post(srv.URL+"/api/v1/audit/saved-searches",
		"application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		Data store.SavedSearch `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	_ = resp.Body.Close()
	if created.Data.ID == "" || created.Data.Name != "errs" {
		t.Fatalf("create roundtrip: %+v", created.Data)
	}

	// List.
	list := getAuditJSON(t, srv.URL+"/api/v1/audit/saved-searches")
	if arr, _ := list["data"].([]any); len(arr) != 1 {
		t.Fatalf("list len = %d, want 1", len(arr))
	}

	// Patch (disable).
	patchBody, _ := json.Marshal(map[string]any{
		"name": "errs", "enabled": false, "threshold_count": 9, "window_sec": 600,
	})
	req, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/api/v1/audit/saved-searches/"+created.Data.ID, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	var patched struct {
		Data store.SavedSearch `json:"data"`
	}
	_ = json.NewDecoder(presp.Body).Decode(&patched)
	_ = presp.Body.Close()
	if patched.Data.Enabled {
		t.Fatalf("patch should have disabled the search")
	}
	if patched.Data.ThresholdCount != 9 {
		t.Fatalf("patch threshold = %d, want 9", patched.Data.ThresholdCount)
	}

	// Delete → 204.
	dreq, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/api/v1/audit/saved-searches/"+created.Data.ID, nil)
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", dresp.StatusCode)
	}
}
