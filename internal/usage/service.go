package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

const (
	slowProbeCacheDuration = 5 * time.Minute
	maxWindowDays          = 365
)

type ProviderCollector interface {
	Fetch(context.Context, store.SourceConfig) (store.CollectorResult, error)
}

type ORCollector interface {
	Fetch(context.Context, store.SourceConfig) (store.ORCollectorResult, error)
}

// LocalStatsCollector reads an installed harness's local usage database.
// Implementations must use direct argv execution, never a shell.
type LocalStatsCollector interface {
	Stats(context.Context, int) ([]clistats.ModelStats, error)
}

type Service struct {
	Store       store.UsageStore
	Collectors  map[string]ProviderCollector
	ORCollector ORCollector
	LocalStats  map[string]LocalStatsCollector // opencode, mimo, grok

	mu              sync.Mutex
	providerCache   map[string]providerCacheEntry
	orCache         map[string]openRouterCacheEntry
	localCache      map[string]localCacheEntry
	providerFlights map[string]*providerFlight
	orFlights       map[string]*openRouterFlight
	localFlights    map[string]*localStatsFlight
	snapshotRefresh map[string]bool
	now             func() time.Time
}

func (s *Service) Snapshot(
	ctx context.Context,
	configs []store.SourceConfig,
	days int,
	force bool,
) (store.UsageSnapshot, error) {
	days = normalizeDays(days)
	now := s.clock()().UTC()
	cacheKey := snapshotCacheKey(configs, days)
	persisted, found, cacheReadErr := s.loadPersistedSnapshot(ctx, cacheKey)
	if !force && found {
		if now.Sub(persisted.GeneratedAt) >= slowProbeCacheDuration {
			s.refreshPersistedSnapshot(cacheKey, configs, days, persisted)
		}
		return persisted, nil
	}
	var fallback *store.UsageSnapshot
	if found {
		fallback = &persisted
	}
	snapshot, err := s.snapshotFresh(ctx, configs, days, force, now, cacheKey, fallback)
	if cacheReadErr != nil && snapshot.CacheError == "" {
		snapshot.CacheError = fmt.Sprintf("usage cache read failed: %v", cacheReadErr)
	}
	return snapshot, err
}

func (s *Service) snapshotFresh(
	ctx context.Context,
	configs []store.SourceConfig,
	days int,
	force bool,
	now time.Time,
	cacheKey string,
	fallback *store.UsageSnapshot,
) (store.UsageSnapshot, error) {
	runs, ledgerErr := s.loadLedger(ctx, days, now)
	local := s.loadLocalStats(ctx, days, force)
	byProvider := normalizeConfigs(configs)

	providers := make([]store.ProviderSnapshot, len(store.AllProviders))
	var openrouter store.OpenRouterSnapshot
	var wg sync.WaitGroup
	for index, provider := range store.AllProviders {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			providers[i] = s.providerSnapshot(
				ctx, name, byProvider[name], runs, local, ledgerErr, force, now,
				days*24*60,
			)
		}(index, provider)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		openrouter = s.openRouterSnapshot(
			ctx, byProvider[store.ProviderOpenRouter], runs, local, force, now,
		)
	}()
	wg.Wait()
	snapshot := store.UsageSnapshot{
		GeneratedAt: now,
		WindowDays:  days,
		Providers:   providers,
		OpenRouter:  openrouter,
	}
	if fallback != nil {
		preserveLastGoodSnapshot(&snapshot, *fallback)
	}
	if err := s.persistSnapshot(ctx, cacheKey, snapshot); err != nil {
		snapshot.CacheError = fmt.Sprintf("usage cache write failed: %v", err)
	}
	return snapshot, nil
}

func (s *Service) loadPersistedSnapshot(
	ctx context.Context, key string,
) (store.UsageSnapshot, bool, error) {
	cache, ok := s.Store.(store.UsageSnapshotCache)
	if !ok {
		return store.UsageSnapshot{}, false, nil
	}
	snapshot, found, err := cache.GetUsageSnapshot(ctx, key)
	return snapshot, found && err == nil, err
}

func (s *Service) persistSnapshot(
	ctx context.Context, key string, snapshot store.UsageSnapshot,
) error {
	if cache, ok := s.Store.(store.UsageSnapshotCache); ok {
		return cache.PutUsageSnapshot(ctx, key, snapshot)
	}
	return nil
}

func (s *Service) refreshPersistedSnapshot(
	key string, configs []store.SourceConfig, days int, fallback store.UsageSnapshot,
) {
	s.mu.Lock()
	if s.snapshotRefresh == nil {
		s.snapshotRefresh = make(map[string]bool)
	}
	if s.snapshotRefresh[key] {
		s.mu.Unlock()
		return
	}
	s.snapshotRefresh[key] = true
	s.mu.Unlock()

	configCopy := append([]store.SourceConfig(nil), configs...)
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.snapshotRefresh, key)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		snapshot, _ := s.snapshotFresh(ctx, configCopy, days, true, s.clock()().UTC(), key, &fallback)
		if snapshot.CacheError != "" {
			log.Printf("usage snapshot background refresh: %s", snapshot.CacheError)
		}
	}()
}

func preserveLastGoodSnapshot(current *store.UsageSnapshot, previous store.UsageSnapshot) {
	previousProviders := make(map[string]store.ProviderSnapshot, len(previous.Providers))
	for _, provider := range previous.Providers {
		previousProviders[provider.Provider] = provider
	}
	for index := range current.Providers {
		provider := &current.Providers[index]
		old, ok := previousProviders[provider.Provider]
		if !ok {
			continue
		}
		if len(provider.Windows) == 0 && len(old.Windows) > 0 &&
			provider.AllowanceStatus != store.StatusOK {
			provider.Windows = old.Windows
			provider.AllowanceUpdatedAt = old.AllowanceUpdatedAt
			provider.AllowanceStale = true
			provider.Stale = true
			provider.AllowanceStatus = store.StatusPartial
			provider.Status = store.StatusPartial
			if provider.Plan == "" {
				provider.Plan = old.Plan
			}
		}
		if provider.ObservedUpdatedAt == nil && !hasObserved(provider.Observed) && hasObserved(old.Observed) {
			provider.Observed = old.Observed
			provider.ObservedSource = old.ObservedSource
			provider.ObservedSourceLabel = old.ObservedSourceLabel
			provider.ObservedUpdatedAt = old.ObservedUpdatedAt
			provider.ObservedCostKind = old.ObservedCostKind
			provider.Detail = appendDetail(provider.Detail, "showing last-known local observation")
			if provider.Status == store.StatusUnavailable || provider.Status == store.StatusError {
				provider.Status = store.StatusPartial
			}
		}
	}
	if current.OpenRouter.Status != store.StatusOK && previous.OpenRouter.Status == store.StatusOK {
		current.OpenRouter.Credits = previous.OpenRouter.Credits
		current.OpenRouter.Status = store.StatusPartial
		current.OpenRouter.Stale = true
	}
}

func snapshotCacheKey(configs []store.SourceConfig, days int) string {
	byProvider := normalizeConfigs(configs)
	providers := make([]string, 0, len(byProvider))
	for provider := range byProvider {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	parts := []string{"usage-snapshot-v1", strconv.Itoa(days)}
	for _, provider := range providers {
		parts = append(parts, sourceCacheKey(byProvider[provider]))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

func (s *Service) loadLedger(
	ctx context.Context,
	days int,
	now time.Time,
) ([]store.UsageLedgerRun, error) {
	if s.Store == nil {
		return nil, nil
	}
	return s.Store.ListUsageLedgerRuns(ctx, windowSince(now, days))
}

func normalizeDays(days int) int {
	if days <= 0 {
		return 30
	}
	if days > maxWindowDays {
		return maxWindowDays
	}
	return days
}

func normalizeConfigs(configs []store.SourceConfig) map[string]store.SourceConfig {
	out := make(map[string]store.SourceConfig, len(configs)+1)
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if cfg.Label == "" {
			cfg.Label = store.ProviderLabels[cfg.Provider]
		}
		cfg.Kind = inferSourceKind(cfg)
		out[cfg.Provider] = cfg
	}
	applyDefaultSourceConfigs(out)
	return out
}

func applyDefaultSourceConfigs(out map[string]store.SourceConfig) {
	defaults := []store.SourceConfig{
		{
			Provider: store.ProviderClaude,
			Kind:     store.SourceKindCLI,
			Label:    store.ProviderLabels[store.ProviderClaude],
			Enabled:  true,
		},
		{
			Provider: store.ProviderCodex,
			Kind:     store.SourceKindCLI,
			Label:    store.ProviderLabels[store.ProviderCodex],
			Enabled:  true,
		},
		{
			Provider: store.ProviderGrok,
			Kind:     store.SourceKindCLI,
			Label:    store.ProviderLabels[store.ProviderGrok],
			Enabled:  true,
		},
		localAPIConfig(store.ProviderMiniMax, store.LocalAuthKeyMiniMax),
		localAPIConfig(store.ProviderZAI, store.LocalAuthKeyZAI),
		{
			Provider:    store.ProviderMiMo,
			Kind:        store.SourceKindCLI,
			Label:       store.ProviderLabels[store.ProviderMiMo],
			AuthScopeID: store.LocalAuthScopeMiMo,
			SecretKey:   store.LocalAuthKeyMiMoXiaomi,
			Enabled:     true,
		},
		{
			Provider:    store.ProviderOpenRouter,
			Kind:        store.SourceKindAPI,
			Label:       "OpenRouter",
			AuthScopeID: store.LocalAuthScopeOpenCode,
			SecretKey:   store.LocalAuthKeyOpenRouter,
			Enabled:     true,
		},
	}
	for _, cfg := range defaults {
		if _, ok := out[cfg.Provider]; ok {
			continue
		}
		out[cfg.Provider] = cfg
	}
}

func localAPIConfig(provider, secretKey string) store.SourceConfig {
	return store.SourceConfig{
		Provider:    provider,
		Kind:        store.SourceKindAPI,
		Label:       store.ProviderLabels[provider],
		AuthScopeID: store.LocalAuthScopeOpenCode,
		SecretKey:   secretKey,
		Enabled:     true,
	}
}

func inferSourceKind(cfg store.SourceConfig) string {
	if cfg.Kind != "" {
		return cfg.Kind
	}
	if cfg.Limit > 0 && cfg.AuthScopeID == "" {
		return store.SourceKindManual
	}
	if cfg.AuthScopeID != "" {
		return store.SourceKindAPI
	}
	switch cfg.Provider {
	case store.ProviderClaude, store.ProviderCodex, store.ProviderGrok, store.ProviderMiMo:
		return store.SourceKindCLI
	}
	return store.SourceKindAuto
}

func (s *Service) clock() func() time.Time {
	if s.now != nil {
		return s.now
	}
	return time.Now
}
