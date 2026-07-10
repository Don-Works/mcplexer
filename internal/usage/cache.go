// Package usage — cache.go implements the slow-probe cache for usage
// snapshots. Five-minute TTL; force bypasses.
package usage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (s *Service) tryCache(ctx context.Context, days int) (store.UsageSnapshot, bool) {
	providers, err := s.Store.ListCachedProviderSnapshots(ctx)
	if err != nil || len(providers) == 0 {
		return store.UsageSnapshot{}, false
	}
	now := time.Now().UTC()
	for _, p := range providers {
		if now.Sub(p.UpdatedAt) > slowProbeCacheDuration {
			return store.UsageSnapshot{}, false
		}
	}
	result := make([]store.ProviderSnapshot, 0, len(providers))
	for _, p := range providers {
		var snap store.ProviderSnapshot
		if err := json.Unmarshal([]byte(p.Snapshot), &snap); err != nil {
			return store.UsageSnapshot{}, false
		}
		snap.Stale = false
		result = append(result, snap)
	}
	or := store.OpenRouterSnapshot{Status: store.StatusUnconfigured}
	if cached, err := s.Store.GetCachedOpenRouter(ctx); err == nil {
		if now.Sub(cached.UpdatedAt) <= slowProbeCacheDuration {
			_ = json.Unmarshal([]byte(cached.Snapshot), &or)
			or.Stale = false
		}
	}
	return store.UsageSnapshot{
		GeneratedAt: now,
		WindowDays:  days,
		Providers:   result,
		OpenRouter:  or,
	}, true
}

func (s *Service) cacheSnapshot(ctx context.Context, snap store.UsageSnapshot) {
	for _, p := range snap.Providers {
		data, err := json.Marshal(p)
		if err != nil {
			continue
		}
		_ = s.Store.UpsertCachedProviderSnapshot(ctx, &store.CachedProviderSnapshot{
			Provider:  p.Provider,
			Snapshot:  string(data),
			UpdatedAt: time.Now().UTC(),
		})
	}
	orData, err := json.Marshal(snap.OpenRouter)
	if err == nil {
		_ = s.Store.UpsertCachedOpenRouter(ctx, &store.CachedOpenRouter{
			Snapshot:  string(orData),
			UpdatedAt: time.Now().UTC(),
		})
	}
}
