package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

type fakeUsageSnapshotter struct {
	days    int
	force   bool
	configs []store.SourceConfig
}

func (f *fakeUsageSnapshotter) Snapshot(
	_ context.Context,
	configs []store.SourceConfig,
	days int,
	force bool,
) (store.UsageSnapshot, error) {
	f.days, f.force, f.configs = days, force, configs
	return store.UsageSnapshot{
		WindowDays: days,
		Providers:  []store.ProviderSnapshot{},
		OpenRouter: store.OpenRouterSnapshot{ByHarness: []store.ORHarnessUsage{}},
	}, nil
}

func TestUsageRoutesGetAndRefresh(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageSnapshotter{}
	router := NewRouter(RouterDeps{UsageSvc: fake})

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/usage?days=14", nil))
	if get.Code != http.StatusOK || fake.days != 14 || fake.force {
		t.Fatalf("GET status=%d days=%d force=%v", get.Code, fake.days, fake.force)
	}
	var payload store.UsageSnapshot
	if err := json.Unmarshal(get.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WindowDays != 14 {
		t.Fatalf("window_days = %d", payload.WindowDays)
	}

	refresh := httptest.NewRecorder()
	router.ServeHTTP(refresh, httptest.NewRequest(http.MethodPost, "/api/v1/usage/refresh", nil))
	if refresh.Code != http.StatusOK || fake.days != 30 || !fake.force {
		t.Fatalf("refresh status=%d days=%d force=%v", refresh.Code, fake.days, fake.force)
	}
}

func TestUsageRouteRejectsInvalidDays(t *testing.T) {
	t.Parallel()
	router := NewRouter(RouterDeps{UsageSvc: &fakeUsageSnapshotter{}})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/usage?days=0", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}
