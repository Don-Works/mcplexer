package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// readFile / writeFile are thin test helpers so a brain .md can be edited
// out-of-band to drive the if_hash conflict path.
func readFile(t *testing.T, path string) (string, error) {
	t.Helper()
	b, err := os.ReadFile(path)
	return string(b), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newBrowserHandler wires a brainBrowserHandler over a real sqlite store +
// the live Editor (sharing the indexer's self-write set, exactly as the
// daemon does) so writes exercise the full serializer + hash-CAS path.
func newBrowserHandler(t *testing.T) (*brainBrowserHandler, *http.ServeMux, store.Store, context.Context) {
	t.Helper()
	db := newBrainTestStore(t)
	ctx := context.Background()
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "ws", Name: "Workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ix := brain.NewIndexer(cfg, db, nil)
	ser := brain.NewSerializer(cfg, db, nil)
	ser.ShareSelfWrites(ix)
	ed := brain.NewEditor(db, ser)

	h := &brainBrowserHandler{editor: ed, indexer: ix, store: db, enabled: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/brain/tree", h.tree)
	mux.HandleFunc("GET /api/v1/brain/clients", h.clients)
	mux.HandleFunc("GET /api/v1/brain/workspaces", h.workspaces)
	mux.HandleFunc("GET /api/v1/brain/scope", h.scope)
	mux.HandleFunc("GET /api/v1/brain/records", h.records)
	mux.HandleFunc("GET /api/v1/brain/search", h.search)
	mux.HandleFunc("GET /api/v1/brain/workspaces/{ws}/tasks", h.listTasks)
	mux.HandleFunc("GET /api/v1/brain/workspaces/{ws}/memory", h.listMemories)
	mux.HandleFunc("GET /api/v1/brain/record/{kind}/{id}", h.getRecord)
	mux.HandleFunc("POST /api/v1/brain/record/{kind}", h.saveRecord)
	mux.HandleFunc("PUT /api/v1/brain/record/{kind}/{id}", h.saveRecord)
	mux.HandleFunc("POST /api/v1/brain/record/{id}/suppress-candidate", h.suppressCandidate)
	mux.HandleFunc("POST /api/v1/brain/reindex", h.reindex)
	return h, mux, db, ctx
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestBrowser_PostTask_WritesViaSerializer(t *testing.T) {
	_, mux, db, ctx := newBrowserHandler(t)

	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{
		Workspace: "ws", Title: "Wired through serializer", Status: "open",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("POST task = %d: %s", w.Code, w.Body.String())
	}
	var saved brain.TaskRecord
	if err := json.NewDecoder(w.Body).Decode(&saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.ID == "" || saved.Path == "" {
		t.Fatalf("expected id + path, got %+v", saved)
	}
	// The DB row exists (FTS triggers fired) and an index_files binding was
	// recorded by the serializer (proving the write went through it).
	if _, err := db.GetTask(ctx, saved.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, err := db.GetIndexFile(ctx, saved.Path); err != nil {
		t.Fatalf("expected index_files row at %s: %v", saved.Path, err)
	}
}

func TestBrowser_PutTask_ValidationReturns422(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	// Create one to update.
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{
		Workspace: "ws", Title: "ok", Status: "open",
	})
	var saved brain.TaskRecord
	_ = json.NewDecoder(w.Body).Decode(&saved)

	// Empty title on update -> 422.
	w2 := doJSON(t, mux, http.MethodPut, "/api/v1/brain/record/task/"+saved.ID, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "", Status: "open",
	})
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&resp)
	if resp["field"] != "title" {
		t.Fatalf("expected field=title, got %v", resp["field"])
	}
}

func TestBrowser_GetTree_CountsRecords(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "a", Status: "open"})
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/memory", brain.MemoryRecord{Kind: "note", Name: "m", Workspace: "ws", Content: "x"})

	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/tree", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET tree = %d: %s", w.Code, w.Body.String())
	}
	var nodes []brain.TreeNode
	if err := json.NewDecoder(w.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, n := range nodes {
		if n.Workspace == "ws" {
			found = true
			if n.TaskCount != 1 || n.MemoryCount != 1 {
				t.Fatalf("counts = %d/%d, want 1/1", n.TaskCount, n.MemoryCount)
			}
		}
	}
	if !found {
		t.Fatal("ws not in tree")
	}
}

func TestBrowser_GetRecord_RoundTrip(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/memory", brain.MemoryRecord{
		Kind: "note", Name: "deploy", Workspace: "ws", Content: "body", Tags: []string{"ops"},
	})
	var saved brain.MemoryRecord
	_ = json.NewDecoder(w.Body).Decode(&saved)

	g := doJSON(t, mux, http.MethodGet, "/api/v1/brain/record/memory/"+saved.ID, nil)
	if g.Code != http.StatusOK {
		t.Fatalf("GET record = %d: %s", g.Code, g.Body.String())
	}
	var got brain.MemoryRecord
	if err := json.NewDecoder(g.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "deploy" || got.Content != "body" || len(got.Tags) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestBrowser_Disabled_Returns503(t *testing.T) {
	h := &brainBrowserHandler{enabled: false}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/brain/tree", h.tree)
	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/tree", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when disabled, got %d", w.Code)
	}
}

func TestBrowser_Search_RanksExactPrefixFirst(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "Re-arm worker cron", Status: "open"})
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "Disarm alarm handler", Status: "open"})

	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/search?q=re-arm&kind=task&workspace=ws", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", w.Code, w.Body.String())
	}
	var res brain.SearchResult
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected search hits")
	}
	if res.Hits[0].Tier != 0 {
		t.Fatalf("top tier = %d, want 0 (exact-prefix)", res.Hits[0].Tier)
	}
	if res.CreateLabel != "re-arm" {
		t.Fatalf("create label = %q, want re-arm", res.CreateLabel)
	}
}

func TestBrowser_Records_FiltersByStatus(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "open one", Status: "open"})
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "doing one", Status: "doing"})

	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/records?workspace=ws&kind=task&status=doing", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("records = %d: %s", w.Code, w.Body.String())
	}
	var recs []brain.TaskRecord
	if err := json.NewDecoder(w.Body).Decode(&recs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != "doing" {
		t.Fatalf("status filter failed: %+v", recs)
	}
	if recs[0].OnDiskHash == "" {
		t.Fatal("expected on_disk_hash on the record row")
	}
}

func TestBrowser_Records_FiltersMemoriesByKind(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/memory", brain.MemoryRecord{
		Kind: "note", Name: "page-one", Workspace: "ws", Content: "page",
	})
	_ = doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/memory", brain.MemoryRecord{
		Kind: "fact", Name: "fact-one", Workspace: "ws", Content: "fact",
	})

	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/records?workspace=ws&kind=memory&memory_kind=fact", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("records = %d: %s", w.Code, w.Body.String())
	}
	var recs []brain.MemoryRecord
	if err := json.NewDecoder(w.Body).Decode(&recs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(recs) != 1 || recs[0].Kind != "fact" {
		t.Fatalf("memory_kind filter failed: %+v", recs)
	}
}

func TestBrowser_Scope_FusionString(t *testing.T) {
	_, mux, db, ctx := newBrowserHandler(t)
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "child", Name: "Child", ParentID: "acme"}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	w := doJSON(t, mux, http.MethodGet, "/api/v1/brain/scope?workspace=child", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("scope = %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["scope"] != "child ∪ acme ∪ global" {
		t.Fatalf("scope = %q, want 'child ∪ acme ∪ global'", resp["scope"])
	}
}

func TestBrowser_PutTask_StatusOffVocabReturns422WithAllowed(t *testing.T) {
	_, mux, db, ctx := newBrowserHandler(t)
	// Seed a status vocab so an off-vocab status is rejected with the allowed set.
	for _, s := range []string{"open", "doing", "done"} {
		if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{WorkspaceID: "ws", StatusText: s, Kind: "open"}); err != nil {
			t.Fatalf("seed vocab: %v", err)
		}
	}
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "ok", Status: "open"})
	var saved brain.TaskRecord
	_ = json.NewDecoder(w.Body).Decode(&saved)

	w2 := doJSON(t, mux, http.MethodPut, "/api/v1/brain/record/task/"+saved.ID, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "ok", Status: "in-progres", IfHash: saved.OnDiskHash,
	})
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&resp)
	if resp["field"] != "status" {
		t.Fatalf("expected field=status, got %v", resp["field"])
	}
	allowed, ok := resp["allowed"].([]any)
	if !ok || len(allowed) != 3 {
		t.Fatalf("expected allowed vocab of 3, got %v", resp["allowed"])
	}
}

func TestBrowser_PutTask_StaleIfHashReturns409Detail(t *testing.T) {
	_, mux, db, ctx := newBrowserHandler(t)
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/task", brain.TaskRecord{Workspace: "ws", Title: "Original", Status: "open"})
	var saved brain.TaskRecord
	_ = json.NewDecoder(w.Body).Decode(&saved)

	// Concurrent out-of-band edit so the on-disk hash diverges.
	raw, err := readFile(t, saved.Path)
	if err != nil {
		t.Fatalf("read .md: %v", err)
	}
	writeFile(t, saved.Path, raw+"\nout of band\n")
	// Drop the index sha-match too so casConflict would also fire — but the
	// if_hash check fires first with the stale token.
	_ = ctx
	_ = db

	w2 := doJSON(t, mux, http.MethodPut, "/api/v1/brain/record/task/"+saved.ID, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "My edit", Status: "doing", IfHash: saved.OnDiskHash,
	})
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	var resp struct {
		Conflict bool                 `json:"conflict"`
		Detail   brain.ConflictDetail `json:"detail"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 409: %v", err)
	}
	if !resp.Conflict || resp.Detail.OnDiskTask == nil {
		t.Fatalf("expected a conflict detail with on_disk_task, got %+v", resp)
	}
	if resp.Detail.OnDiskTask.Title != "Original" {
		t.Fatalf("on-disk task title = %q, want Original", resp.Detail.OnDiskTask.Title)
	}
	if resp.Detail.Writer == "" {
		t.Fatal("conflict detail must name a writer")
	}
}

func TestBrowser_SuppressCandidate(t *testing.T) {
	_, mux, db, ctx := newBrowserHandler(t)
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/record/rec1/suppress-candidate", map[string]string{"content_hash": "h1"})
	if w.Code != http.StatusOK {
		t.Fatalf("suppress = %d: %s", w.Code, w.Body.String())
	}
	yes, err := db.IsCandidateSuppressed(ctx, "rec1", "h1")
	if err != nil || !yes {
		t.Fatalf("expected rec1/h1 suppressed, got %v/%v", yes, err)
	}
}

func TestBrowser_Reindex(t *testing.T) {
	_, mux, _, _ := newBrowserHandler(t)
	w := doJSON(t, mux, http.MethodPost, "/api/v1/brain/reindex", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reindex = %d: %s", w.Code, w.Body.String())
	}
}
