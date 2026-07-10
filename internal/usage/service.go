// Package usage — service.go implements the Snapshot method that
// aggregates usage data from all sources and returns the JSON contract.
// Five-minute slow-probe cache; force bypasses. Never includes secrets
// in errors/logs/cache keys.
package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const slowProbeCacheDuration = 5 * time.Minute

// WorkerRunQuerier abstracts the store methods the service needs for
// querying worker_runs. This interface lets tests inject a mock.
type WorkerRunQuerier interface {
	ListWorkerRuns(ctx context.Context, workerID string, limit int) ([]*store.WorkerRun, error)
	ListWorkers(ctx context.Context, workspaceID string, enabledOnly bool) ([]*store.Worker, error)
}

// ProviderCollector is the interface for HTTP-based provider collectors.
type ProviderCollector interface {
	Fetch(ctx context.Context, cfg store.SourceConfig) (store.CollectorResult, error)
}

// ORCollector is the interface for the OpenRouter collector.
type ORCollector interface {
	Fetch(ctx context.Context, cfg store.SourceConfig) (store.ORCollectorResult, error)
}

// Service orchestrates usage data collection.
type Service struct {
	Store       store.UsageStore
	WorkerRuns  WorkerRunQuerier
	Collectors  map[string]ProviderCollector // keyed by provider
	ORCollector ORCollector
	WindowDays  int
}

// Snapshot returns the full usage snapshot. When force is false and a
// cached snapshot is less than 5 minutes old, the cached version is
// returned. Otherwise the service refreshes from all sources.
func (s *Service) Snapshot(
	ctx context.Context, configs []store.SourceConfig, days int, force bool,
) (store.UsageSnapshot, error) {
	if days <= 0 {
		days = 30
	}
	if !force {
		cached, ok := s.tryCache(ctx, days)
		if ok {
			return cached, nil
		}
	}
	providers, err := s.collectProviders(ctx, configs, days)
	if err != nil {
		return store.UsageSnapshot{}, fmt.Errorf("collect providers: %w", err)
	}
	or := s.collectOpenRouter(ctx, configs)
	providers = ensureAllProviders(providers, configs)
	snapshot := store.UsageSnapshot{
		GeneratedAt: time.Now().UTC(),
		WindowDays:  days,
		Providers:   providers,
		OpenRouter:  or,
	}
	s.cacheSnapshot(ctx, snapshot)
	return snapshot, nil
}

func (s *Service) collectProviders(
	ctx context.Context, configs []store.SourceConfig, days int,
) ([]store.ProviderSnapshot, error) {
	var result []store.ProviderSnapshot
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		result = append(result, s.collectOneProvider(ctx, cfg, days))
	}
	return result, nil
}

func (s *Service) collectOneProvider(
	ctx context.Context, cfg store.SourceConfig, days int,
) store.ProviderSnapshot {
	switch cfg.Kind {
	case store.SourceKindAuto:
		return s.collectFromLedger(ctx, cfg, days)
	case store.SourceKindAPI:
		return s.collectFromAPI(ctx, cfg)
	case store.SourceKindManual:
		return store.ProviderSnapshot{
			Provider: cfg.Provider, Label: cfg.Label,
			Status: store.StatusPartial, Source: "manual",
			Detail: "manual source — awaiting CLI stats or config",
		}
	default:
		return store.ProviderSnapshot{
			Provider: cfg.Provider, Label: cfg.Label,
			Status: store.StatusUnconfigured,
		}
	}
}

func (s *Service) collectFromLedger(
	ctx context.Context, cfg store.SourceConfig, days int,
) store.ProviderSnapshot {
	snap := store.ProviderSnapshot{
		Provider: cfg.Provider, Label: cfg.Label, Plan: cfg.Plan,
		Source: "auto", SourceLabel: cfg.Label, Windows: []store.UsageWindow{},
	}
	if s.WorkerRuns == nil {
		snap.Status = store.StatusUnavailable
		snap.Error = "worker run store not available"
		return snap
	}
	workers, err := s.WorkerRuns.ListWorkers(ctx, "", false)
	if err != nil {
		snap.Status = store.StatusError
		snap.Error = fmt.Sprintf("list workers: %v", err)
		return snap
	}
	var allRuns []LedgerRun
	for _, w := range workers {
		runs, err := s.WorkerRuns.ListWorkerRuns(ctx, w.ID, 0)
		if err != nil {
			continue
		}
		for _, r := range runs {
			allRuns = append(allRuns, LedgerRun{
				ModelProvider: r.ModelProvider, ModelID: r.ModelID,
				BillingModel: r.BillingModel, SubscriptionBucket: r.SubscriptionBucket,
				RealCostUSD: r.RealCostUSD, InputTokens: r.InputTokens,
				OutputTokens: r.OutputTokens, CostUSD: int(r.CostUSD), Status: r.Status,
			})
		}
	}
	agg := AggregateLedger(allRuns, cfg.Provider)
	snap.Observed = store.ObservedUsage{
		Requests: agg.Requests, InputTokens: agg.InputTokens,
		OutputTokens: agg.OutputTokens, CostUSD: agg.CostUSD,
		AccountingMissingRuns: agg.AccountingMissingRuns,
	}
	snap.Status = store.StatusOK
	now := time.Now().UTC()
	snap.UpdatedAt = &now
	return snap
}

func (s *Service) collectFromAPI(
	ctx context.Context, cfg store.SourceConfig,
) store.ProviderSnapshot {
	collector, ok := s.Collectors[cfg.Provider]
	if !ok {
		return store.ProviderSnapshot{
			Provider: cfg.Provider, Label: cfg.Label,
			Status: store.StatusUnavailable, Error: "no collector registered",
		}
	}
	result, err := collector.Fetch(ctx, cfg)
	if err != nil {
		return store.ProviderSnapshot{
			Provider: cfg.Provider, Label: cfg.Label,
			Status: store.StatusError, Error: fmt.Sprintf("collector: %v", err),
		}
	}
	return result.Snapshot
}

func (s *Service) collectOpenRouter(
	ctx context.Context, configs []store.SourceConfig,
) store.OpenRouterSnapshot {
	if s.ORCollector == nil {
		return store.OpenRouterSnapshot{Status: store.StatusUnconfigured}
	}
	var cfg store.SourceConfig
	found := false
	for _, c := range configs {
		if c.Provider == store.ProviderOpenRouter {
			cfg, found = c, true
			break
		}
	}
	if !found || !cfg.Enabled {
		return store.OpenRouterSnapshot{Status: store.StatusUnconfigured}
	}
	result, err := s.ORCollector.Fetch(ctx, cfg)
	if err != nil {
		return store.OpenRouterSnapshot{Status: store.StatusError, Error: fmt.Sprintf("openrouter: %v", err)}
	}
	snapshot := result.Snapshot
	s.mergeORFromLedger(ctx, &snapshot)
	now := time.Now().UTC()
	snapshot.UpdatedAt = &now
	return snapshot
}

func (s *Service) mergeORFromLedger(ctx context.Context, snapshot *store.OpenRouterSnapshot) {
	if s.WorkerRuns == nil {
		return
	}
	workers, err := s.WorkerRuns.ListWorkers(ctx, "", false)
	if err != nil {
		return
	}
	var allRuns []LedgerRun
	for _, w := range workers {
		runs, _ := s.WorkerRuns.ListWorkerRuns(ctx, w.ID, 0)
		for _, r := range runs {
			allRuns = append(allRuns, LedgerRun{
				ModelProvider: r.ModelProvider, ModelID: r.ModelID,
				InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
				RealCostUSD: r.RealCostUSD, Status: r.Status,
			})
		}
	}
	snapshot.ByHarness = AggregateOpenRouterByHarness(allRuns)
}

func ensureAllProviders(
	providers []store.ProviderSnapshot, configs []store.SourceConfig,
) []store.ProviderSnapshot {
	existing := make(map[string]bool, len(providers))
	for _, p := range providers {
		existing[p.Provider] = true
	}
	configMap := make(map[string]store.SourceConfig, len(configs))
	for _, c := range configs {
		configMap[c.Provider] = c
	}
	for _, name := range store.AllProviders {
		if existing[name] {
			continue
		}
		cfg := configMap[name]
		providers = append(providers, store.ProviderSnapshot{
			Provider: name, Label: cfg.Label,
			Status: store.StatusUnconfigured, Source: "none",
		})
	}
	return providers
}
