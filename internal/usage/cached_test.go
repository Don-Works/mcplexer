package usage

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestCachedSnapshotReadsPersistedWithoutProbing proves the read path a
// non-admin agent uses returns the persisted snapshot and never runs a
// provider collector — the "cache-only, side-effect-free" contract.
func TestCachedSnapshotReadsPersistedWithoutProbing(t *testing.T) {
	cache := &fakeUsageCacheStore{}
	configs := []store.SourceConfig{apiConfig(store.ProviderMiniMax, "scope")}
	key := snapshotCacheKey(configs, 30)
	want := store.UsageSnapshot{
		GeneratedAt: time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC),
		WindowDays:  30,
		Providers: []store.ProviderSnapshot{{
			Provider: store.ProviderMiniMax, Status: store.StatusOK,
		}},
	}
	if err := cache.PutUsageSnapshot(context.Background(), key, want); err != nil {
		t.Fatal(err)
	}
	collector := &countingProviderCollector{refreshed: make(chan struct{})}
	service := &Service{
		Store:      cache,
		Collectors: map[string]ProviderCollector{store.ProviderMiniMax: collector},
	}

	got, found, err := service.CachedSnapshot(context.Background(), configs, 30)
	if err != nil || !found {
		t.Fatalf("CachedSnapshot found=%v err=%v", found, err)
	}
	if !got.GeneratedAt.Equal(want.GeneratedAt) || got.WindowDays != 30 {
		t.Fatalf("cached snapshot = %+v", got)
	}
	if calls := collector.calls.Load(); calls != 0 {
		t.Fatalf("collector called %d times on a cached read — read must never probe", calls)
	}
}

// TestCachedSnapshotMissReportsNotFoundWithoutProbing proves a cold cache is
// reported as "not found" (which callers must treat as UNKNOWN, not zero) and
// still triggers no probe.
func TestCachedSnapshotMissReportsNotFoundWithoutProbing(t *testing.T) {
	collector := &countingProviderCollector{refreshed: make(chan struct{})}
	service := &Service{
		Store:      &fakeUsageCacheStore{},
		Collectors: map[string]ProviderCollector{store.ProviderMiniMax: collector},
	}
	_, found, err := service.CachedSnapshot(
		context.Background(),
		[]store.SourceConfig{apiConfig(store.ProviderMiniMax, "scope")}, 30,
	)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected found=false on an empty cache")
	}
	if calls := collector.calls.Load(); calls != 0 {
		t.Fatalf("collector called %d times on a cache miss — read must never probe", calls)
	}
}

// TestCachedSnapshotDefaultsDaysToThirty locks the default-window contract: a
// days=0 read must resolve to the same key the admin/API path warmed at the
// default 30-day window, so the common case is a cache hit.
func TestCachedSnapshotDefaultsDaysToThirty(t *testing.T) {
	cache := &fakeUsageCacheStore{}
	configs := []store.SourceConfig{apiConfig(store.ProviderZAI, "scope")}
	if err := cache.PutUsageSnapshot(
		context.Background(), snapshotCacheKey(configs, 30),
		store.UsageSnapshot{WindowDays: 30},
	); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: cache}
	_, found, err := service.CachedSnapshot(context.Background(), configs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("days=0 read did not resolve to the default 30-day key")
	}
}
