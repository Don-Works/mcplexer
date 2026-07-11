package usage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

type fakeUsageCacheStore struct {
	fakeUsageStore
	mu        sync.Mutex
	snapshots map[string]store.UsageSnapshot
}

func (f *fakeUsageCacheStore) GetUsageSnapshot(
	_ context.Context, key string,
) (store.UsageSnapshot, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot, ok := f.snapshots[key]
	return snapshot, ok, nil
}

func (f *fakeUsageCacheStore) PutUsageSnapshot(
	_ context.Context, key string, snapshot store.UsageSnapshot,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshots == nil {
		f.snapshots = make(map[string]store.UsageSnapshot)
	}
	f.snapshots[key] = snapshot
	return nil
}

type countingProviderCollector struct {
	calls     atomic.Int32
	refreshed chan struct{}
}

func (c *countingProviderCollector) Fetch(
	_ context.Context, _ store.SourceConfig,
) (store.CollectorResult, error) {
	if c.calls.Add(1) == 2 {
		close(c.refreshed)
	}
	return store.CollectorResult{Snapshot: store.ProviderSnapshot{
		Status: store.StatusOK,
		Windows: []store.UsageWindow{{
			ID: "live", Label: "Live", Unit: store.UnitPercent,
		}},
	}}, nil
}

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

func TestPersistedSnapshotReturnsImmediatelyAndRefreshesStaleInBackground(t *testing.T) {
	first := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var clock atomic.Value
	clock.Store(first)
	collector := &countingProviderCollector{refreshed: make(chan struct{})}
	cache := &fakeUsageCacheStore{}
	service := &Service{
		Store: cache,
		Collectors: map[string]ProviderCollector{
			store.ProviderMiniMax: collector,
		},
		now: func() time.Time { return clock.Load().(time.Time) },
	}
	config := []store.SourceConfig{apiConfig(store.ProviderMiniMax, "scope")}

	initial, err := service.Snapshot(context.Background(), config, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	clock.Store(first.Add(slowProbeCacheDuration + time.Minute))
	cached, err := service.Snapshot(context.Background(), config, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.GeneratedAt.Equal(initial.GeneratedAt) {
		t.Fatalf("cached generated_at = %s, want %s", cached.GeneratedAt, initial.GeneratedAt)
	}

	select {
	case <-collector.refreshed:
	case <-time.After(time.Second):
		t.Fatal("stale snapshot did not trigger a background refresh")
	}
	if calls := collector.calls.Load(); calls != 2 {
		t.Fatalf("collector calls = %d, want 2", calls)
	}
}

func TestPreserveLastGoodSnapshotAcrossFailedRefresh(t *testing.T) {
	updated := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	previous := store.UsageSnapshot{
		Providers: []store.ProviderSnapshot{{
			Provider: store.ProviderGrok, Plan: "SuperGrok",
			Status: store.StatusOK, AllowanceStatus: store.StatusOK,
			AllowanceUpdatedAt: &updated,
			Windows:            []store.UsageWindow{{ID: "weekly", UsedPercent: numberPtr(12)}},
			Observed:           store.ObservedUsage{Requests: 4, InputTokens: 80},
			ObservedSource:     "cli", ObservedSourceLabel: "Grok CLI logs",
		}},
		OpenRouter: store.OpenRouterSnapshot{Status: store.StatusOK},
	}
	current := store.UsageSnapshot{
		Providers: []store.ProviderSnapshot{{
			Provider: store.ProviderGrok, Status: store.StatusError,
			AllowanceStatus: store.StatusError, AllowanceError: "probe failed",
			Windows: []store.UsageWindow{},
		}},
		OpenRouter: store.OpenRouterSnapshot{Status: store.StatusError, Error: "offline"},
	}
	preserveLastGoodSnapshot(&current, previous)
	grok := current.Providers[0]
	if grok.Status != store.StatusPartial || !grok.AllowanceStale || len(grok.Windows) != 1 ||
		grok.Observed.Requests != 4 || grok.ObservedSourceLabel != "Grok CLI logs" {
		t.Fatalf("grok = %+v", grok)
	}
	if current.OpenRouter.Status != store.StatusPartial || !current.OpenRouter.Stale ||
		current.OpenRouter.Error != "offline" {
		t.Fatalf("openrouter = %+v", current.OpenRouter)
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

func TestMiMoEstimatedCreditsWindowInjectedFromLocalStats(t *testing.T) {
	mimo := &fakeLocalStats{stats: []clistats.ModelStats{
		{Model: "xiaomi/mimo-v2.5-pro", Requests: 5, InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000},
		{Model: "mimo/mimo-v2.5", Requests: 3, InputTokens: 200, OutputTokens: 100},
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		LocalStats: map[string]LocalStatsCollector{"mimo": mimo},
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{{
		Provider: store.ProviderMiMo, Kind: store.SourceKindCLI,
		Plan: "Token Plan", Enabled: true,
	}}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mimoSnap := providerByName(t, snapshot, store.ProviderMiMo)
	var creditsWindow *store.UsageWindow
	for i := range mimoSnap.Windows {
		if mimoSnap.Windows[i].ID == "mimo_token_plan_credits" {
			creditsWindow = &mimoSnap.Windows[i]
			break
		}
	}
	if creditsWindow == nil {
		t.Fatal("expected mimo_token_plan_credits window")
	}
	if creditsWindow.Unit != store.UnitCredits {
		t.Fatalf("unit = %q", creditsWindow.Unit)
	}
	if creditsWindow.Label != "Token Plan credits (estimate)" {
		t.Fatalf("label = %q", creditsWindow.Label)
	}
	// v2.5-pro: 1000*300 + 500*600 + 2000*2.5 = 605000
	// v2.5: 200*100 + 100*200 = 40000
	// total: 645000
	if creditsWindow.Used == nil || *creditsWindow.Used != 645000 {
		t.Fatalf("used = %v", creditsWindow.Used)
	}
	if creditsWindow.Limit != nil {
		t.Fatalf("limit should be nil without configured limit")
	}
	if !strings.Contains(mimoSnap.Detail, "off-peak 0.8x discount not reconstructible") {
		t.Fatalf("detail missing estimate caveat: %q", mimoSnap.Detail)
	}
}

func TestMiMoCreditsWindowRespectsConfiguredLimit(t *testing.T) {
	mimo := &fakeLocalStats{stats: []clistats.ModelStats{
		{Model: "xiaomi/mimo-v2.5-pro", InputTokens: 100},
	}}
	config := []store.SourceConfig{{
		Provider: store.ProviderMiMo, Kind: store.SourceKindCLI,
		Limit: 1000000, Unit: store.UnitCredits, WindowMinutes: 30 * 24 * 60, Enabled: true,
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		LocalStats: map[string]LocalStatsCollector{"mimo": mimo},
	}
	snapshot, err := service.Snapshot(context.Background(), config, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mimoSnap := providerByName(t, snapshot, store.ProviderMiMo)
	var creditsWindow *store.UsageWindow
	for i := range mimoSnap.Windows {
		if mimoSnap.Windows[i].ID == "mimo_token_plan_credits" {
			creditsWindow = &mimoSnap.Windows[i]
			break
		}
	}
	if creditsWindow == nil {
		t.Fatal("expected credits window")
	}
	if creditsWindow.Limit == nil || *creditsWindow.Limit != 1000000 {
		t.Fatalf("limit = %v", creditsWindow.Limit)
	}
	// 100*300 = 30000
	if creditsWindow.Used == nil || *creditsWindow.Used != 30000 {
		t.Fatalf("used = %v", creditsWindow.Used)
	}
	if creditsWindow.Remaining == nil || *creditsWindow.Remaining != 970000 {
		t.Fatalf("remaining = %v", creditsWindow.Remaining)
	}
	if creditsWindow.UsedPercent == nil || *creditsWindow.UsedPercent != 3.0 {
		t.Fatalf("used_percent = %v", creditsWindow.UsedPercent)
	}
}

func TestMiMoCreditsWindowReplacesManualCreditsWindow(t *testing.T) {
	mimo := &fakeLocalStats{stats: []clistats.ModelStats{
		{Model: "xiaomi/mimo-v2.5-pro", InputTokens: 100},
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		LocalStats: map[string]LocalStatsCollector{"mimo": mimo},
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{{
		Provider: store.ProviderMiMo, Kind: store.SourceKindManual,
		Limit: 1_000_000, Unit: store.UnitCredits, WindowMinutes: 30 * 24 * 60, Enabled: true,
	}}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mimoSnap := providerByName(t, snapshot, store.ProviderMiMo)
	if len(mimoSnap.Windows) != 1 || mimoSnap.Windows[0].ID != "mimo_token_plan_credits" {
		t.Fatalf("windows = %+v", mimoSnap.Windows)
	}
}

func TestMiMoNoCreditsWindowForUnknownModels(t *testing.T) {
	mimo := &fakeLocalStats{stats: []clistats.ModelStats{
		{Model: "xiaomi/unknown-model", InputTokens: 1000},
	}}
	service := &Service{
		Store:      &fakeUsageStore{},
		LocalStats: map[string]LocalStatsCollector{"mimo": mimo},
	}
	snapshot, err := service.Snapshot(context.Background(), []store.SourceConfig{{
		Provider: store.ProviderMiMo, Kind: store.SourceKindCLI,
		Plan: "Token Plan", Enabled: true,
	}}, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	mimoSnap := providerByName(t, snapshot, store.ProviderMiMo)
	for _, w := range mimoSnap.Windows {
		if w.ID == "mimo_token_plan_credits" {
			t.Fatal("should not have credits window for unknown model")
		}
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
