package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestHookRoutesRejectWrongMethodThroughRouter prevents the SPA fallback
// from swallowing method errors on the two non-/api hook endpoints.
func TestHookRoutesRejectWrongMethodThroughRouter(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := NewRouter(RouterDeps{
		Store:           db,
		ApprovalManager: approval.NewManager(db, approval.NewBus()),
	})
	for _, path := range []string{"/v1/hooks/pretool", "/v1/hooks/session"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status: want 405, got %d; body=%q", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("Allow header: want POST, got %q", got)
			}
		})
	}
}
