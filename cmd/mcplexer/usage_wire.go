package main

import (
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/netguard"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage"
	"github.com/don-works/mcplexer/internal/usage/clistats"
	"github.com/don-works/mcplexer/internal/usage/collectors"
	"github.com/don-works/mcplexer/internal/usage/grokstats"
)

var (
	sharedUsageOnce sync.Once
	sharedUsageSvc  *usage.Service
)

// sharedUsageService returns a process-wide usage service, building it once.
// Per-connection gateway sites (the socket handler) use it so mcpx__usage_summary
// is available without rebuilding collectors on every accept. The read tool only
// touches the DB-backed snapshot cache, so this instance sees whatever the
// dashboard/admin path assembled through any other usage.Service instance.
func sharedUsageService(ledger store.UsageStore, secretReader *secrets.Manager) *usage.Service {
	sharedUsageOnce.Do(func() {
		sharedUsageSvc = buildUsageService(ledger, secretReader)
	})
	return sharedUsageSvc
}

func buildUsageService(
	ledger store.UsageStore,
	secretReader *secrets.Manager,
) *usage.Service {
	client := netguard.NewPublicHTTPClient(8 * time.Second)
	statsRunner := clistats.NewRunner(nil)
	statsRunner.Timeout = 8 * time.Second
	authReader := newLocalUsageAuthReader(secretReader)
	claudeBinary := collectors.ResolveBinary("claude")
	codexBinary := collectors.ResolveBinary("codex")
	grokBinary := collectors.ResolveBinary("grok")
	mimoBinary := collectors.ResolveBinary("mimo")
	opencodeBinary := collectors.ResolveBinary("opencode")
	return &usage.Service{
		Store: ledger,
		Collectors: map[string]usage.ProviderCollector{
			store.ProviderClaude:  &collectors.ClaudeCollector{ClaudeBinary: claudeBinary},
			store.ProviderCodex:   &collectors.CodexCollector{CodexBinary: codexBinary},
			store.ProviderGrok:    &collectors.GrokCollector{GrokBinary: grokBinary},
			store.ProviderMiniMax: &collectors.MiniMaxCollector{Client: client, Secret: authReader},
			store.ProviderZAI:     &collectors.ZAICollector{Client: client, Secret: authReader},
			store.ProviderMiMo:    &collectors.MiMoCollector{MiMoBinary: mimoBinary, Secret: authReader},
		},
		ORCollector: &collectors.OpenRouterCollector{
			Client: client,
			Secret: authReader,
		},
		LocalStats: map[string]usage.LocalStatsCollector{
			"opencode": usage.HarnessStatsCollector{Runner: statsRunner, Binary: opencodeBinary},
			"mimo":     usage.HarnessStatsCollector{Runner: statsRunner, Binary: mimoBinary},
			"grok":     grokstats.Collector{},
		},
	}
}
