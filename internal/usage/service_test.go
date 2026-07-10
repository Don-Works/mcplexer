package usage

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// mockUsageStore implements store.UsageStore for testing.
type mockUsageStore struct {
	providers map[string]*store.CachedProviderSnapshot
	orCache   *store.CachedOpenRouter
}

func newMockUsageStore() *mockUsageStore {
	return &mockUsageStore{providers: make(map[string]*store.CachedProviderSnapshot)}
}

func (m *mockUsageStore) UpsertCachedProviderSnapshot(_ context.Context, s *store.CachedProviderSnapshot) error {
	m.providers[s.Provider] = s
	return nil
}

func (m *mockUsageStore) GetCachedProviderSnapshot(_ context.Context, provider string) (*store.CachedProviderSnapshot, error) {
	s, ok := m.providers[provider]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s, nil
}

func (m *mockUsageStore) ListCachedProviderSnapshots(_ context.Context) ([]store.CachedProviderSnapshot, error) {
	var out []store.CachedProviderSnapshot
	for _, s := range m.providers {
		out = append(out, *s)
	}
	return out, nil
}

func (m *mockUsageStore) UpsertCachedOpenRouter(_ context.Context, s *store.CachedOpenRouter) error {
	m.orCache = s
	return nil
}

func (m *mockUsageStore) GetCachedOpenRouter(_ context.Context) (*store.CachedOpenRouter, error) {
	if m.orCache == nil {
		return nil, store.ErrNotFound
	}
	return m.orCache, nil
}

// mockWorkerRunQuerier implements WorkerRunQuerier.
type mockWorkerRunQuerier struct {
	workers []*store.Worker
	runs    map[string][]*store.WorkerRun
}

func (m *mockWorkerRunQuerier) ListWorkers(_ context.Context, _ string, _ bool) ([]*store.Worker, error) {
	return m.workers, nil
}

func (m *mockWorkerRunQuerier) ListWorkerRuns(_ context.Context, workerID string, _ int) ([]*store.WorkerRun, error) {
	return m.runs[workerID], nil
}

// mockCollector implements ProviderCollector.
type mockCollector struct {
	snap store.ProviderSnapshot
	err  error
}

func (m *mockCollector) Fetch(_ context.Context, _ store.SourceConfig) (store.CollectorResult, error) {
	return store.CollectorResult{Snapshot: m.snap, Duration: time.Millisecond}, m.err
}

// mockORCollector implements ORCollector.
type mockORCollector struct {
	snap store.OpenRouterSnapshot
	err  error
}

func (m *mockORCollector) Fetch(_ context.Context, _ store.SourceConfig) (store.ORCollectorResult, error) {
	return store.ORCollectorResult{Snapshot: m.snap, Duration: time.Millisecond}, m.err
}

func TestSnapshotAllProvidersPresent(t *testing.T) {
	svc := &Service{
		Store:      newMockUsageStore(),
		WorkerRuns: &mockWorkerRunQuerier{},
		WindowDays: 30,
	}
	configs := []store.SourceConfig{
		{Provider: store.ProviderClaude, Kind: store.SourceKindAuto, Label: "Claude Pro", Enabled: true},
	}

	snap, err := svc.Snapshot(context.Background(), configs, 30, true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Must have all 6 providers.
	if len(snap.Providers) != len(store.AllProviders) {
		t.Fatalf("providers = %d, want %d", len(snap.Providers), len(store.AllProviders))
	}

	found := make(map[string]bool)
	for _, p := range snap.Providers {
		found[p.Provider] = true
	}
	for _, name := range store.AllProviders {
		if !found[name] {
			t.Errorf("missing provider: %s", name)
		}
	}
}

func TestSnapshotAutoFromLedger(t *testing.T) {
	workers := []*store.Worker{
		{ID: "w1", Name: "test-worker"},
	}
	runs := map[string][]*store.WorkerRun{
		"w1": {
			{Status: "success", InputTokens: 1000, OutputTokens: 500, RealCostUSD: 0.05, SubscriptionBucket: "claude"},
			{Status: "success", InputTokens: 0, OutputTokens: 0, RealCostUSD: 0, SubscriptionBucket: "claude"},
		},
	}
	svc := &Service{
		Store:      newMockUsageStore(),
		WorkerRuns: &mockWorkerRunQuerier{workers: workers, runs: runs},
		WindowDays: 30,
	}
	configs := []store.SourceConfig{
		{Provider: store.ProviderClaude, Kind: store.SourceKindAuto, Label: "Claude", Enabled: true},
	}

	snap, err := svc.Snapshot(context.Background(), configs, 30, true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	var claude store.ProviderSnapshot
	for _, p := range snap.Providers {
		if p.Provider == store.ProviderClaude {
			claude = p
		}
	}

	if claude.Observed.Requests != 2 {
		t.Errorf("requests = %d, want 2", claude.Observed.Requests)
	}
	if claude.Observed.AccountingMissingRuns != 1 {
		t.Errorf("missing = %d, want 1", claude.Observed.AccountingMissingRuns)
	}
	if claude.Observed.InputTokens != 1000 {
		t.Errorf("input = %d, want 1000", claude.Observed.InputTokens)
	}
}

func TestSnapshotAPICollector(t *testing.T) {
	svc := &Service{
		Store:      newMockUsageStore(),
		WorkerRuns: &mockWorkerRunQuerier{},
		Collectors: map[string]ProviderCollector{
			store.ProviderMiniMax: &mockCollector{
				snap: store.ProviderSnapshot{
					Provider: store.ProviderMiniMax,
					Status:   store.StatusOK,
					Windows: []store.UsageWindow{
						{ID: "minimax_tokens", Label: "Token Plan", Used: 1000, Limit: 10000, Remaining: 9000, Unit: store.UnitTokens},
					},
				},
			},
		},
		WindowDays: 30,
	}
	configs := []store.SourceConfig{
		{Provider: store.ProviderMiniMax, Kind: store.SourceKindAPI, Label: "MiniMax", Enabled: true},
	}

	snap, err := svc.Snapshot(context.Background(), configs, 30, true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	var minimax store.ProviderSnapshot
	for _, p := range snap.Providers {
		if p.Provider == store.ProviderMiniMax {
			minimax = p
		}
	}
	if minimax.Status != store.StatusOK {
		t.Errorf("status = %q, want ok", minimax.Status)
	}
	if len(minimax.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(minimax.Windows))
	}
	if minimax.Windows[0].Remaining != 9000 {
		t.Errorf("remaining = %f, want 9000", minimax.Windows[0].Remaining)
	}
}

func TestSnapshotCacheHit(t *testing.T) {
	mockStore := newMockUsageStore()
	svc := &Service{
		Store:      mockStore,
		WorkerRuns: &mockWorkerRunQuerier{},
		WindowDays: 30,
	}
	configs := []store.SourceConfig{
		{Provider: store.ProviderClaude, Kind: store.SourceKindAuto, Label: "Claude", Enabled: true},
	}

	// First call populates cache.
	snap1, err := svc.Snapshot(context.Background(), configs, 30, true)
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	// Second call (force=false) should hit cache.
	snap2, err := svc.Snapshot(context.Background(), configs, 30, false)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	if snap2.GeneratedAt.Before(snap1.GeneratedAt) {
		t.Error("cached snapshot should not be older")
	}
}

func TestEnsureAllProviders(t *testing.T) {
	providers := []store.ProviderSnapshot{
		{Provider: store.ProviderClaude, Status: store.StatusOK},
	}
	configs := []store.SourceConfig{
		{Provider: store.ProviderClaude, Label: "Claude"},
	}
	result := ensureAllProviders(providers, configs)
	if len(result) != len(store.AllProviders) {
		t.Fatalf("expected %d, got %d", len(store.AllProviders), len(result))
	}
	// Claude should be unchanged.
	for _, p := range result {
		if p.Provider == store.ProviderClaude && p.Status != store.StatusOK {
			t.Errorf("claude status changed to %q", p.Status)
		}
		if p.Provider == store.ProviderGrok && p.Status != store.StatusUnconfigured {
			t.Errorf("grok should be unconfigured, got %q", p.Status)
		}
	}
}
