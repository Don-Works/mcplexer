package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

type fakeUsageStore struct {
	runs  []store.UsageLedgerRun
	err   error
	since time.Time
}

func (f *fakeUsageStore) ListUsageLedgerRuns(
	_ context.Context,
	since time.Time,
) ([]store.UsageLedgerRun, error) {
	f.since = since
	return f.runs, f.err
}

type fakeProviderCollector struct {
	results []store.ProviderSnapshot
	calls   int
}

func (f *fakeProviderCollector) Fetch(
	_ context.Context,
	_ store.SourceConfig,
) (store.CollectorResult, error) {
	index := f.calls
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	f.calls++
	return store.CollectorResult{Snapshot: f.results[index]}, nil
}

type fakeLocalStats struct {
	stats []clistats.ModelStats
	err   error
	calls int
}

func (f *fakeLocalStats) Stats(
	_ context.Context,
	_ int,
) ([]clistats.ModelStats, error) {
	f.calls++
	return f.stats, f.err
}

func TestSnapshotIncludesAllProvidersAndHonorsDays(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ledger := &fakeUsageStore{runs: []store.UsageLedgerRun{{
		ModelProvider: "claude_cli", SubscriptionBucket: "claude",
		InputTokens: 10, OutputTokens: 2, Status: "success",
	}}}
	service := &Service{Store: ledger, now: func() time.Time { return now }}
	snapshot, err := service.Snapshot(context.Background(), nil, 14, false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WindowDays != 14 || len(snapshot.Providers) != 6 {
		t.Fatalf("snapshot = days:%d providers:%d", snapshot.WindowDays, len(snapshot.Providers))
	}
	wantSince := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	if !ledger.since.Equal(wantSince) {
		t.Fatalf("since = %s, want %s", ledger.since, wantSince)
	}
	if snapshot.Providers[0].Label != "Claude" || snapshot.Providers[0].Observed.InputTokens != 10 {
		t.Fatalf("claude = %+v", snapshot.Providers[0])
	}
}

func TestSnapshotUsesLocalHarnessStatsAndShowsOpenRouterWithoutKey(t *testing.T) {
	opencode := &fakeLocalStats{stats: []clistats.ModelStats{
		{Model: "minimax/MiniMax-M3", Requests: 8, InputTokens: 100, CacheReadTokens: 500},
		{Model: "openrouter/x/model", Requests: 3, InputTokens: 20, CostUSD: 0.25},
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		LocalStats: map[string]LocalStatsCollector{"opencode": opencode},
	}
	snapshot, err := service.Snapshot(context.Background(), nil, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mini := providerByName(t, snapshot, store.ProviderMiniMax)
	if mini.Observed.Requests != 8 || mini.Observed.CacheReadTokens != 500 {
		t.Fatalf("minimax observed = %+v", mini.Observed)
	}
	if snapshot.OpenRouter.Status != store.StatusPartial || len(snapshot.OpenRouter.ByHarness) != 1 {
		t.Fatalf("openrouter = %+v", snapshot.OpenRouter)
	}
	if snapshot.OpenRouter.ByHarness[0].Harness != "opencode" {
		t.Fatalf("harness = %+v", snapshot.OpenRouter.ByHarness[0])
	}
}

func TestProviderCacheIsConfigKeyedAndForceRefreshes(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	collector := &fakeProviderCollector{results: []store.ProviderSnapshot{{
		Status:  store.StatusOK,
		Windows: []store.UsageWindow{{ID: "w", Label: "5 hour", Unit: store.UnitPercent}},
	}}}
	service := &Service{
		Store:      &fakeUsageStore{},
		Collectors: map[string]ProviderCollector{store.ProviderMiniMax: collector},
		now:        func() time.Time { return now },
	}
	config := apiConfig(store.ProviderMiniMax, "scope-a")
	_, _ = service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	_, _ = service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	if collector.calls != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.calls)
	}
	config.AuthScopeID = "scope-b"
	_, _ = service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	_, _ = service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, true)
	if collector.calls != 3 {
		t.Fatalf("collector calls = %d, want 3", collector.calls)
	}
}

func TestFailedRefreshFallsBackToStaleGoodAllowance(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	collector := &fakeProviderCollector{results: []store.ProviderSnapshot{
		{Status: store.StatusOK, Windows: []store.UsageWindow{{ID: "w", Unit: store.UnitPercent}}},
		{Status: store.StatusError, Error: "HTTP 503"},
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		Collectors: map[string]ProviderCollector{store.ProviderZAI: collector},
		now:        func() time.Time { return now },
	}
	config := apiConfig(store.ProviderZAI, "scope")
	_, _ = service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, false)
	snapshot, _ := service.Snapshot(context.Background(), []store.SourceConfig{config}, 30, true)
	zai := providerByName(t, snapshot, store.ProviderZAI)
	if !zai.Stale || zai.Status != store.StatusPartial || len(zai.Windows) != 1 {
		t.Fatalf("zai stale fallback = %+v", zai)
	}
}

func TestLedgerFailureIsolatedPerProvider(t *testing.T) {
	service := &Service{
		Store: &fakeUsageStore{err: errors.New("db down")},
		Collectors: map[string]ProviderCollector{
			store.ProviderClaude: &fakeProviderCollector{results: []store.ProviderSnapshot{{
				Status:  store.StatusOK,
				Windows: []store.UsageWindow{{ID: "weekly", Unit: store.UnitPercent}},
			}}},
		},
	}
	snapshot, err := service.Snapshot(context.Background(), nil, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers[0].Status != store.StatusOK {
		t.Fatalf("claude status = %s", snapshot.Providers[0].Status)
	}
}

func apiConfig(provider, scope string) store.SourceConfig {
	return store.SourceConfig{
		Provider:    provider,
		Kind:        store.SourceKindAPI,
		Label:       store.ProviderLabels[provider],
		AuthScopeID: scope,
		SecretKey:   "api_key",
		Enabled:     true,
	}
}

func providerByName(
	t *testing.T,
	snapshot store.UsageSnapshot,
	provider string,
) store.ProviderSnapshot {
	t.Helper()
	for _, row := range snapshot.Providers {
		if row.Provider == provider {
			return row
		}
	}
	t.Fatalf("provider %q missing", provider)
	return store.ProviderSnapshot{}
}
