package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestCompressionHandlerStats exercises the full stats endpoint against a real
// sqlite store: seed observations, GET, and assert the mode + transform list +
// aggregate come back intact.
func TestCompressionHandlerStats(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	obs := []store.CompressionObservation{{
		Transform: "json_minify", Lossless: true, Changed: true,
		OrigBytes: 1000, WouldSaveBytes: 300, WouldSaveTokens: 85,
	}}
	if err := db.RecordCompression(ctx, "", time.Now(), obs); err != nil {
		t.Fatalf("record: %v", err)
	}

	h := &compressionHandler{store: db, settings: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/compression/stats?days=7", nil)
	h.stats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Mode       string                     `json:"mode"`
		Transforms []map[string]any           `json:"transforms"`
		Disabled   []string                   `json:"disabled"`
		Aggregate  store.CompressionAggregate `json:"aggregate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Mode != "shadow" {
		t.Errorf("mode=%q, want shadow (default when settings unavailable)", body.Mode)
	}
	if len(body.Transforms) == 0 {
		t.Error("expected a non-empty transform list so the UI can render toggles")
	}
	if len(body.Aggregate.ByTransform) != 1 || body.Aggregate.ByTransform[0].WouldSaveTokens != 85 {
		t.Errorf("unexpected aggregate: %+v", body.Aggregate.ByTransform)
	}
}
