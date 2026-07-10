package usage

import (
	"context"
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
	LocalStats  map[string]LocalStatsCollector // opencode, mimo

	mu              sync.Mutex
	providerCache   map[string]providerCacheEntry
	orCache         map[string]openRouterCacheEntry
	localCache      map[string]localCacheEntry
	providerFlights map[string]*providerFlight
	orFlights       map[string]*openRouterFlight
	localFlights    map[string]*localStatsFlight
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
	return store.UsageSnapshot{
		GeneratedAt: now,
		WindowDays:  days,
		Providers:   providers,
		OpenRouter:  openrouter,
	}, nil
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
