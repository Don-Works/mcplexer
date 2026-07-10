package usage

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

func (s *Service) openRouterSnapshot(
	ctx context.Context,
	cfg store.SourceConfig,
	runs []store.UsageLedgerRun,
	local map[string]localStatsResult,
	force bool,
	now time.Time,
) store.OpenRouterSnapshot {
	harnesses := AggregateOpenRouterByHarness(runs)
	harnesses = mergeLocalOpenRouter(harnesses, local)
	snapshot := store.OpenRouterSnapshot{
		Status:    store.StatusUnconfigured,
		Credits:   store.ORCreditInfo{},
		ByHarness: harnesses,
	}
	if cfg.Provider == store.ProviderOpenRouter && cfg.Enabled && s.ORCollector != nil {
		snapshot = s.openRouterCredits(ctx, cfg, force, now)
		snapshot.ByHarness = harnesses
	}
	if len(harnesses) > 0 {
		if snapshot.Status == store.StatusUnconfigured || snapshot.Status == store.StatusError {
			snapshot.Status = store.StatusPartial
		}
		if snapshot.UpdatedAt == nil {
			snapshot.UpdatedAt = &now
		}
	}
	if snapshot.ByHarness == nil {
		snapshot.ByHarness = []store.ORHarnessUsage{}
	}
	return snapshot
}

func (s *Service) openRouterCredits(
	ctx context.Context,
	cfg store.SourceConfig,
	force bool,
	now time.Time,
) store.OpenRouterSnapshot {
	key := sourceCacheKey(cfg)
	if !force {
		if cached, ok := s.cachedOpenRouter(key, now); ok {
			return cached
		}
	}
	flight, leader := s.beginOpenRouterFlight(key)
	if !leader {
		return waitOpenRouterFlight(ctx, flight)
	}
	snapshot := s.collectOpenRouterCredits(ctx, cfg, now)
	snapshot = s.putOpenRouterCache(key, snapshot, now)
	s.finishOpenRouterFlight(key, flight, snapshot)
	return snapshot
}

func (s *Service) collectOpenRouterCredits(
	ctx context.Context,
	cfg store.SourceConfig,
	now time.Time,
) store.OpenRouterSnapshot {
	result, err := s.ORCollector.Fetch(ctx, cfg)
	snapshot := result.Snapshot
	if err != nil {
		snapshot = store.OpenRouterSnapshot{
			Status: store.StatusError,
			Error:  err.Error(),
		}
	}
	if snapshot.Status == "" {
		snapshot.Status = store.StatusOK
	}
	if snapshot.ByHarness == nil {
		snapshot.ByHarness = []store.ORHarnessUsage{}
	}
	if snapshot.UpdatedAt == nil {
		snapshot.UpdatedAt = &now
	}
	return snapshot
}

func mergeLocalOpenRouter(
	ledger []store.ORHarnessUsage,
	local map[string]localStatsResult,
) []store.ORHarnessUsage {
	merged := make(map[string]store.ORHarnessUsage, len(ledger)+len(local))
	for _, harness := range ledger {
		merged[harness.Harness] = harness
	}
	for name, result := range local {
		if result.Err != nil {
			continue
		}
		if harness, ok := openRouterFromModels(name, result.Stats); ok {
			merged[name] = harness
		}
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]store.ORHarnessUsage, 0, len(names))
	for _, name := range names {
		out = append(out, merged[name])
	}
	return out
}

func openRouterFromModels(
	harness string,
	stats []clistats.ModelStats,
) (store.ORHarnessUsage, bool) {
	out := store.ORHarnessUsage{
		Harness: harness, Models: []store.ORModelUsage{},
		CostKind: store.ObservedCostEstimate,
	}
	for _, stat := range stats {
		if !strings.HasPrefix(strings.ToLower(stat.Model), "openrouter/") {
			continue
		}
		out.Requests += stat.Requests
		out.InputTokens += stat.InputTokens
		out.OutputTokens += stat.OutputTokens
		out.CacheReadTokens += stat.CacheReadTokens
		out.CacheWriteTokens += stat.CacheWriteTokens
		out.CostUSD += stat.CostUSD
		out.Models = append(out.Models, store.ORModelUsage{
			Model:        stat.Model,
			Requests:     stat.Requests,
			InputTokens:  stat.InputTokens,
			OutputTokens: stat.OutputTokens,
			CostUSD:      stat.CostUSD,
		})
	}
	sort.Slice(out.Models, func(i, j int) bool {
		return out.Models[i].Model < out.Models[j].Model
	})
	return out, len(out.Models) > 0
}
