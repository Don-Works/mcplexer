// memory_handler_test.go — HTTP-level tests for the memory REST surface.
// Spins up a real sqlite-backed memory.Service so each test exercises the
// same code path the PWA hits in production.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newMemoryTestServer wires a fresh sqlite-backed memory.Service into an
// httptest.Server. Returns the server + the underlying store so tests can
// seed rows or assert side-effects directly.
func newMemoryTestServer(t *testing.T) (*httptest.Server, *sqlite.DB, *memory.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := memory.NewService(db, nil, nil)
	r := NewRouter(RouterDeps{
		APIToken:  "",
		Store:     db,
		MemorySvc: svc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

// seedMemory inserts one row through the service (so the test agrees with
// the prod write path) and returns the assigned ID.
func seedMemory(t *testing.T, svc *memory.Service, name, content, kind string, tags []string) string {
	t.Helper()
	id, err := svc.Write(context.Background(), memory.WriteOptions{
		Name:       name,
		Kind:       kind,
		Content:    content,
		Tags:       tags,
		SourceKind: store.MemorySourceHuman,
	})
	if err != nil {
		t.Fatalf("seed memory %q: %v", name, err)
	}
	return id
}

func TestMemoryHandlerListAndFilters(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)

	// Empty list returns [].
	rows := mustListMemory(t, srv.URL+"/api/v1/memory")
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows, got %d", len(rows))
	}

	// Seed three rows: 2 notes + 1 fact.
	seedMemory(t, svc, "fact-a", "ssh keys rotate weekly", store.MemoryKindFact, []string{"ops"})
	seedMemory(t, svc, "note-a", "first note", store.MemoryKindNote, []string{"learning"})
	seedMemory(t, svc, "note-b", "second note", store.MemoryKindNote, []string{"learning", "ops"})

	rows = mustListMemory(t, srv.URL+"/api/v1/memory")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	// Filter by kind=fact.
	rows = mustListMemory(t, srv.URL+"/api/v1/memory?kind=fact")
	if len(rows) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(rows))
	}
	if rows[0]["name"] != "fact-a" {
		t.Errorf("expected name=fact-a, got %v", rows[0]["name"])
	}

	// Filter by tag=learning.
	rows = mustListMemory(t, srv.URL+"/api/v1/memory?tags=learning")
	if len(rows) != 2 {
		t.Fatalf("expected 2 learning-tagged rows, got %d", len(rows))
	}
}

func TestMemoryHandlerCount(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)
	seedMemory(t, svc, "fact-a", "fact body alpha", store.MemoryKindFact, nil)
	seedMemory(t, svc, "fact-b", "fact body beta", store.MemoryKindFact, nil)
	seedMemory(t, svc, "note-a", "note body alpha", store.MemoryKindNote, nil)

	resp, err := http.Get(srv.URL + "/api/v1/memory/count")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var out map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["facts"] != 2 {
		t.Errorf("facts=%d want 2", out["facts"])
	}
	if out["notes"] != 1 {
		t.Errorf("notes=%d want 1", out["notes"])
	}
}

func TestMemoryHandlerGet(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)
	id := seedMemory(t, svc, "thing", "hello memory body", store.MemoryKindNote, nil)

	// Hit (200).
	resp, err := http.Get(srv.URL + "/api/v1/memory/" + id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var entry map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry["name"] != "thing" {
		t.Errorf("name mismatch: %v", entry["name"])
	}

	// Miss (404).
	resp2, err := http.Get(srv.URL + "/api/v1/memory/nope-not-found")
	if err != nil {
		t.Fatalf("get nope: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp2.StatusCode)
	}
}

func TestMemoryHandlerCreate(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)

	// 201 on happy path.
	body := map[string]any{
		"name":    "preferences",
		"content": "user prefers dark mode",
		"kind":    "fact",
		"tags":    []string{"ui"},
	}
	created := postJSON(t, srv.URL+"/api/v1/memory", body, http.StatusCreated)
	if created["name"] != "preferences" {
		t.Errorf("name mismatch: %v", created["name"])
	}
	if created["kind"] != "fact" {
		t.Errorf("kind mismatch: %v", created["kind"])
	}
	if _, ok := created["id"].(string); !ok || created["id"] == "" {
		t.Errorf("missing id: %v", created["id"])
	}

	// 400 when name is missing.
	bad := map[string]any{"content": "no name here"}
	postJSON(t, srv.URL+"/api/v1/memory", bad, http.StatusBadRequest)

	// 400 when content is missing.
	bad2 := map[string]any{"name": "no-content"}
	postJSON(t, srv.URL+"/api/v1/memory", bad2, http.StatusBadRequest)
}

func TestMemoryHandlerInvalidate(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)
	id := seedMemory(t, svc, "rotating-secret", "version one secret body", store.MemoryKindFact, nil)

	// 204 on success.
	postJSONMethod(t, http.MethodPost, srv.URL+"/api/v1/memory/"+id+"/invalidate",
		map[string]any{}, http.StatusNoContent)

	// Verify state changed — list with include_invalid=1 still shows it,
	// default list hides it. The default ListMemories excludes
	// invalidated rows so a follow-up list MUST omit the row.
	rows := mustListMemory(t, srv.URL+"/api/v1/memory")
	for _, r := range rows {
		if r["id"] == id {
			t.Errorf("expected invalidated row to be hidden, but found %v", r)
		}
	}

	// 404 on unknown id.
	postJSONMethod(t, http.MethodPost, srv.URL+"/api/v1/memory/nope/invalidate",
		map[string]any{}, http.StatusNotFound)
}

func TestMemoryHandlerDelete(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)
	id := seedMemory(t, svc, "thing", "hello memory body", store.MemoryKindNote, nil)

	// 204 on success.
	deleteReq(t, srv.URL+"/api/v1/memory/"+id, http.StatusNoContent)

	// 404 on the same id (soft-deleted rows return ErrNotFound).
	deleteReq(t, srv.URL+"/api/v1/memory/"+id, http.StatusNotFound)

	// 404 on never-existed id.
	deleteReq(t, srv.URL+"/api/v1/memory/nope", http.StatusNotFound)
}

func TestMemoryHandlerSearch(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)
	seedMemory(t, svc, "ssh-rotation", "rotate ssh keys weekly", store.MemoryKindFact, nil)
	seedMemory(t, svc, "deploy-notes", "blue/green deploys take 90s", store.MemoryKindNote, nil)

	// Empty query → returns recent rows (List path).
	hits := mustSearchMemory(t, srv.URL+"/api/v1/memory/search",
		map[string]any{"query": "", "limit": 10})
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits on empty query, got %d", len(hits))
	}

	// Non-empty query runs the FTS path. Confirm 200 + at least one hit.
	hits = mustSearchMemory(t, srv.URL+"/api/v1/memory/search",
		map[string]any{"query": "ssh", "limit": 10})
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit for 'ssh', got 0")
	}
}

func TestMemoryHandlerForgetBySource(t *testing.T) {
	srv, db, _ := newMemoryTestServer(t)
	// Seed two rows tagged with a known source_session_id, plus one untagged.
	for _, name := range []string{"poisoned-1", "poisoned-2"} {
		if err := db.WriteMemory(context.Background(), &store.MemoryEntry{
			Name: name, Kind: store.MemoryKindNote, Content: "leak",
			SourceKind: store.MemorySourceAgent, SourceSessionID: "evil-session-42",
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := db.WriteMemory(context.Background(), &store.MemoryEntry{
		Name: "clean", Kind: store.MemoryKindNote, Content: "ok",
		SourceKind: store.MemorySourceHuman,
	}); err != nil {
		t.Fatalf("seed clean: %v", err)
	}

	// Forget-by-source returns {count: 2}.
	out := postJSON(t, srv.URL+"/api/v1/memory/forget-by-source",
		map[string]any{"source_session_id": "evil-session-42"}, http.StatusOK)
	if int(out["count"].(float64)) != 2 {
		t.Fatalf("expected count=2, got %v", out["count"])
	}

	// 400 when source_session_id is empty.
	postJSON(t, srv.URL+"/api/v1/memory/forget-by-source",
		map[string]any{"source_session_id": ""}, http.StatusBadRequest)

	// Confirm the clean row survived.
	rows := mustListMemory(t, srv.URL+"/api/v1/memory")
	if len(rows) != 1 || rows[0]["name"] != "clean" {
		t.Fatalf("expected only clean row to remain, got %+v", rows)
	}
}

// --- helpers ---------------------------------------------------------

func mustListMemory(t *testing.T, url string) []map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d want 200", url, resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}

func mustSearchMemory(t *testing.T, url string, body map[string]any) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status=%d want 200", url, resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}
