package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newBrainTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newBrainMux(h *brainHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/brain/status", h.status)
	mux.HandleFunc("GET /api/v1/brain/errors", h.errors)
	mux.HandleFunc("POST /api/v1/brain/push", h.push)
	mux.HandleFunc("POST /api/v1/brain/sync", h.sync)
	return mux
}

func TestBrainStatus_Disabled(t *testing.T) {
	h := &brainHandler{store: newBrainTestStore(t), enabled: false, cfg: brain.Config{Dir: "/tmp/brain"}}
	mux := newBrainMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/brain/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", w.Code, w.Body.String())
	}
	var resp brainStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Enabled {
		t.Error("expected enabled=false")
	}
	if resp.Dir != "/tmp/brain" {
		t.Errorf("dir = %q, want /tmp/brain", resp.Dir)
	}
}

func TestBrainErrors_SurfacesRows(t *testing.T) {
	db := newBrainTestStore(t)
	ctx := context.Background()
	if err := db.RecordBrainError(ctx, &store.BrainError{
		Path: "/x/tasks/bad.md", EntityKind: "task", Field: "status", Reason: "not in vocab",
	}); err != nil {
		t.Fatalf("RecordBrainError: %v", err)
	}

	h := &brainHandler{store: db, enabled: true}
	mux := newBrainMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/brain/errors", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var errs []store.BrainError
	if err := json.NewDecoder(w.Body).Decode(&errs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(errs) != 1 || errs[0].Reason != "not in vocab" {
		t.Fatalf("errors = %+v, want one 'not in vocab'", errs)
	}
}

func TestBrainPush_NoGit503(t *testing.T) {
	h := &brainHandler{store: newBrainTestStore(t), enabled: true} // git nil
	mux := newBrainMux(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/brain/push", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("push with no git: status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
}

func TestBrainSync_DisabledReturns503(t *testing.T) {
	h := &brainHandler{store: newBrainTestStore(t), enabled: false}
	mux := newBrainMux(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/brain/sync", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("sync when disabled: status = %d, want 503", w.Code)
	}
}

func TestBrainStatus_GitUnavailableReturnsError(t *testing.T) {
	h := &brainHandler{store: newBrainTestStore(t), enabled: true} // no git
	mux := newBrainMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/brain/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", w.Code, w.Body.String())
	}
	var resp brainStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled {
		t.Error("expected enabled=true")
	}
	if resp.Git != nil {
		t.Error("expected no git status when git unavailable")
	}
	if resp.GitErr == "" {
		t.Fatal("expected git_error to be set when brain enabled but git unavailable")
	}
}
