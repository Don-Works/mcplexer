package usage

import (
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

type providerCacheEntry struct {
	Snapshot store.ProviderSnapshot
	At       time.Time
	Good     *store.ProviderSnapshot
}

type openRouterCacheEntry struct {
	Snapshot store.OpenRouterSnapshot
	At       time.Time
	Good     *store.OpenRouterSnapshot
}

type localCacheEntry struct {
	Stats []clistats.ModelStats
	Err   error
	At    time.Time
	Days  int
}

func sourceCacheKey(cfg store.SourceConfig) string {
	return strings.Join([]string{
		cfg.Provider, cfg.Kind, cfg.Label, cfg.AuthScopeID, cfg.SecretKey,
		cfg.BaseURL, cfg.Plan, cfg.Harness,
		fmt.Sprintf("%g:%s:%s:%d", cfg.Limit, cfg.Unit, cfg.WindowLabel, cfg.WindowMinutes),
	}, "\x00")
}

func (s *Service) cachedProvider(
	key string,
	now time.Time,
) (store.ProviderSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.providerCache[key]
	if !ok || now.Sub(entry.At) >= slowProbeCacheDuration {
		return store.ProviderSnapshot{}, false
	}
	return providerCacheValue(entry), true
}

func providerCacheValue(entry providerCacheEntry) store.ProviderSnapshot {
	if entry.Snapshot.Status != store.StatusError || entry.Good == nil {
		return entry.Snapshot
	}
	stale := *entry.Good
	stale.Stale = true
	stale.AllowanceStale = true
	stale.Status = store.StatusPartial
	stale.AllowanceStatus = store.StatusPartial
	stale.Error = entry.Snapshot.Error
	stale.AllowanceError = entry.Snapshot.Error
	return stale
}

func (s *Service) putProviderCache(
	key string,
	snapshot store.ProviderSnapshot,
	now time.Time,
) store.ProviderSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerCache == nil {
		s.providerCache = make(map[string]providerCacheEntry)
	}
	entry := s.providerCache[key]
	entry.Snapshot, entry.At = snapshot, now
	if snapshot.Status == store.StatusOK || snapshot.AllowanceStatus == store.StatusOK {
		good := snapshot
		entry.Good = &good
	}
	s.providerCache[key] = entry
	return providerCacheValue(entry)
}

func (s *Service) cachedOpenRouter(
	key string,
	now time.Time,
) (store.OpenRouterSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.orCache[key]
	if !ok || now.Sub(entry.At) >= slowProbeCacheDuration {
		return store.OpenRouterSnapshot{}, false
	}
	return openRouterCacheValue(entry), true
}

func openRouterCacheValue(entry openRouterCacheEntry) store.OpenRouterSnapshot {
	if entry.Snapshot.Status != store.StatusError || entry.Good == nil {
		return entry.Snapshot
	}
	stale := *entry.Good
	stale.Stale = true
	stale.Status = store.StatusPartial
	stale.Error = entry.Snapshot.Error
	return stale
}

func (s *Service) putOpenRouterCache(
	key string,
	snapshot store.OpenRouterSnapshot,
	now time.Time,
) store.OpenRouterSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orCache == nil {
		s.orCache = make(map[string]openRouterCacheEntry)
	}
	entry := s.orCache[key]
	entry.Snapshot, entry.At = snapshot, now
	if snapshot.Status == store.StatusOK {
		good := snapshot
		entry.Good = &good
	}
	s.orCache[key] = entry
	return openRouterCacheValue(entry)
}
