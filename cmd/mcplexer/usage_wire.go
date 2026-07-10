package main

import (
	"time"

	"github.com/don-works/mcplexer/internal/netguard"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage"
	"github.com/don-works/mcplexer/internal/usage/clistats"
	"github.com/don-works/mcplexer/internal/usage/collectors"
)

func buildUsageService(
	ledger store.UsageStore,
	secretReader *secrets.Manager,
) *usage.Service {
	client := netguard.NewPublicHTTPClient(8 * time.Second)
	statsRunner := clistats.NewRunner(nil)
	statsRunner.Timeout = 8 * time.Second
	authReader := newLocalUsageAuthReader(secretReader)
	return &usage.Service{
		Store: ledger,
		Collectors: map[string]usage.ProviderCollector{
			store.ProviderClaude:  &collectors.ClaudeCollector{},
			store.ProviderCodex:   &collectors.CodexCollector{},
			store.ProviderGrok:    &collectors.GrokCollector{},
			store.ProviderMiniMax: &collectors.MiniMaxCollector{Client: client, Secret: authReader},
			store.ProviderZAI:     &collectors.ZAICollector{Client: client, Secret: authReader},
			store.ProviderMiMo:    &collectors.MiMoCollector{},
		},
		ORCollector: &collectors.OpenRouterCollector{
			Client: client,
			Secret: authReader,
		},
		LocalStats: map[string]usage.LocalStatsCollector{
			"opencode": usage.HarnessStatsCollector{Runner: statsRunner, Binary: "opencode"},
			"mimo":     usage.HarnessStatsCollector{Runner: statsRunner, Binary: "mimo"},
		},
	}
}
