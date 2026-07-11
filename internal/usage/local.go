package usage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
	"github.com/don-works/mcplexer/internal/usage/collectors"
)

type localStatsResult struct {
	Stats []clistats.ModelStats
	Err   error
}

// HarnessStatsCollector adapts clistats.Runner to the service interface.
type HarnessStatsCollector struct {
	Runner *clistats.Runner
	Binary string
}

func (c HarnessStatsCollector) Stats(
	ctx context.Context,
	days int,
) ([]clistats.ModelStats, error) {
	runner := c.Runner
	if runner == nil {
		runner = clistats.NewRunner(nil)
	}
	result := runner.Run(ctx, c.Binary, days)
	return result.Models, result.Err
}

func (s *Service) loadLocalStats(
	ctx context.Context,
	days int,
	force bool,
) map[string]localStatsResult {
	out := make(map[string]localStatsResult, len(s.LocalStats))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for harness, collector := range s.LocalStats {
		wg.Add(1)
		go func(name string, source LocalStatsCollector) {
			defer wg.Done()
			stats, err := s.oneLocalStats(ctx, name, source, days, force)
			mu.Lock()
			out[name] = localStatsResult{Stats: stats, Err: err}
			mu.Unlock()
		}(harness, collector)
	}
	wg.Wait()
	return out
}

func (s *Service) oneLocalStats(
	ctx context.Context,
	harness string,
	collector LocalStatsCollector,
	days int,
	force bool,
) ([]clistats.ModelStats, error) {
	now := s.clock()().UTC()
	if !force {
		s.mu.Lock()
		entry, ok := s.localCache[harness]
		s.mu.Unlock()
		if ok && entry.Days == days && now.Sub(entry.At) < slowProbeCacheDuration {
			return entry.Stats, entry.Err
		}
	}
	flightKey := fmt.Sprintf("%s\x00%d", harness, days)
	flight, leader := s.beginLocalStatsFlight(flightKey)
	if !leader {
		return waitLocalStatsFlight(ctx, flight)
	}
	stats, err := collector.Stats(ctx, days)
	s.mu.Lock()
	if s.localCache == nil {
		s.localCache = make(map[string]localCacheEntry)
	}
	s.localCache[harness] = localCacheEntry{Stats: stats, Err: err, At: now, Days: days}
	s.mu.Unlock()
	s.finishLocalStatsFlight(flightKey, flight, stats, err)
	return stats, err
}

func observedFromLocal(
	provider string,
	local map[string]localStatsResult,
) (store.ObservedUsage, string, bool) {
	harness, _ := localSelector(provider)
	result, ok := local[harness]
	if !ok || result.Err != nil {
		return store.ObservedUsage{}, "", false
	}
	var observed store.ObservedUsage
	for _, stat := range result.Stats {
		if !matchesLocalProvider(provider, stat.Model) {
			continue
		}
		observed.Requests += stat.Requests
		observed.InputTokens += stat.InputTokens
		observed.OutputTokens += stat.OutputTokens
		observed.CacheReadTokens += stat.CacheReadTokens
		observed.CacheWriteTokens += stat.CacheWriteTokens
		observed.CostUSD += stat.CostUSD
	}
	// A successful local query with no rows is a measured zero, not a failure.
	// The updated-at lineage lets durable fallback distinguish it from a probe
	// that could not run.
	return observed, localStatsSourceLabel(harness), true
}

func localStatsSourceLabel(harness string) string {
	switch harness {
	case "mimo":
		return "MiMoCode CLI stats"
	case "opencode":
		return "OpenCode CLI stats"
	case "grok":
		return "Grok CLI logs"
	default:
		return harness + " CLI stats"
	}
}

func matchesLocalProvider(provider, model string) bool {
	model = strings.ToLower(model)
	switch provider {
	case store.ProviderMiniMax:
		return strings.HasPrefix(model, "minimax/")
	case store.ProviderZAI:
		return strings.HasPrefix(model, "zai-coding-plan/")
	case store.ProviderMiMo:
		return strings.HasPrefix(model, "xiaomi/") || strings.HasPrefix(model, "mimo/")
	case store.ProviderGrok:
		return strings.HasPrefix(model, "grok/")
	default:
		return false
	}
}

func localSelector(provider string) (string, string) {
	switch provider {
	case store.ProviderMiniMax:
		return "opencode", "minimax/"
	case store.ProviderZAI:
		return "opencode", "zai-coding-plan/"
	case store.ProviderMiMo:
		return "mimo", "xiaomi/"
	case store.ProviderGrok:
		return "grok", "grok/"
	default:
		return "", "\x00"
	}
}

func localUpdatedAt(local map[string]localStatsResult, provider string, now time.Time) *time.Time {
	harness, _ := localSelector(provider)
	if result, ok := local[harness]; ok && result.Err == nil {
		return &now
	}
	return nil
}

// mimoPerModelObserved returns per-model observed usage for MiMo models from
// local stats. Each entry preserves the original model name for coefficient
// matching.
func mimoPerModelObserved(local map[string]localStatsResult) []collectors.ObservedModelUsage {
	result, ok := local["mimo"]
	if !ok || result.Err != nil {
		return nil
	}
	var out []collectors.ObservedModelUsage
	for _, stat := range result.Stats {
		model := strings.ToLower(stat.Model)
		if !strings.HasPrefix(model, "xiaomi/") && !strings.HasPrefix(model, "mimo/") {
			continue
		}
		out = append(out, collectors.ObservedModelUsage{
			Model: stat.Model,
			Observed: store.ObservedUsage{
				Requests:         stat.Requests,
				InputTokens:      stat.InputTokens,
				OutputTokens:     stat.OutputTokens,
				CacheReadTokens:  stat.CacheReadTokens,
				CacheWriteTokens: stat.CacheWriteTokens,
				CostUSD:          stat.CostUSD,
			},
		})
	}
	return out
}
