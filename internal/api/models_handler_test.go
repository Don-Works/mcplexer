package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
)

type stubCatalogReader struct{ cat models.Catalog }

func (s stubCatalogReader) Catalog() models.Catalog { return s.cat }

// TestModelsHandlerExposesSourceAndFreshness pins the API contract: GET
// /api/v1/models returns, per provider, the model list, its source (live vs
// static), the auth state, and a last-refreshed timestamp — everything an
// operator needs to trust or distrust the catalog.
func TestModelsHandlerExposesSourceAndFreshness(t *testing.T) {
	when := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	h := &modelsHandler{catalog: stubCatalogReader{cat: models.Catalog{
		RefreshedAt: when,
		Providers: []models.ProviderCatalog{
			{
				Provider:      "grok_cli",
				Models:        []string{"grok-4.5"},
				Source:        models.ModelSourceLive,
				AuthState:     models.ModelAuthOK,
				LastRefreshed: when,
			},
			{
				Provider:      "claude_cli",
				Models:        []string{"claude-sonnet-4-5"},
				Source:        models.ModelSourceStatic,
				AuthState:     models.ModelAuthNotApplicable,
				LastRefreshed: when,
				Note:          "no live source; showing declared known models",
			},
		},
	}}}

	rec := httptest.NewRecorder()
	h.list(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got models.Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if !got.RefreshedAt.Equal(when) {
		t.Fatalf("refreshed_at = %v, want %v", got.RefreshedAt, when)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(got.Providers))
	}

	grok, ok := got.Provider("grok_cli")
	if !ok {
		t.Fatal("missing grok_cli entry")
	}
	if grok.Source != models.ModelSourceLive {
		t.Errorf("grok source = %q, want live", grok.Source)
	}
	if grok.AuthState != models.ModelAuthOK {
		t.Errorf("grok auth = %q, want ok", grok.AuthState)
	}
	if grok.LastRefreshed.IsZero() {
		t.Error("grok last_refreshed is zero — freshness must be observable")
	}
	if !grok.HasModel("grok-4.5") {
		t.Errorf("grok models = %v", grok.Models)
	}

	claude, _ := got.Provider("claude_cli")
	if claude.Source != models.ModelSourceStatic {
		t.Errorf("claude source = %q, want static", claude.Source)
	}
	if claude.Note == "" {
		t.Error("static fallback must carry an explanatory note")
	}
}

// TestModelsHandlerRawJSONFieldNames pins the wire field names the UI depends
// on (snake_case), independent of the Go struct.
func TestModelsHandlerRawJSONFieldNames(t *testing.T) {
	h := &modelsHandler{catalog: stubCatalogReader{cat: models.Catalog{
		RefreshedAt: time.Unix(0, 0).UTC(),
		Providers: []models.ProviderCatalog{{
			Provider: "grok_cli", Models: []string{"grok-4.5"},
			Source: models.ModelSourceLive, AuthState: models.ModelAuthOK,
			LastRefreshed: time.Unix(0, 0).UTC(),
		}},
	}}}
	rec := httptest.NewRecorder()
	h.list(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["refreshed_at"]; !ok {
		t.Errorf("missing refreshed_at; keys=%v", rawKeys(raw))
	}
	var provs []map[string]json.RawMessage
	if err := json.Unmarshal(raw["providers"], &provs); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	for _, field := range []string{"provider", "models", "source", "auth_state", "last_refreshed"} {
		if _, ok := provs[0][field]; !ok {
			t.Errorf("provider entry missing %q; keys=%v", field, rawKeys(provs[0]))
		}
	}
}

func rawKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
