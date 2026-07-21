package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// LedgerRun remains an alias so focused aggregation tests can construct the
// same projection returned by store.UsageStore.
type LedgerRun = store.UsageLedgerRun

type LedgerAggregate struct {
	Requests              int
	InputTokens           int
	OutputTokens          int
	CacheReadTokens       int
	CacheWriteTokens      int
	CostUSD               float64
	AccountingMissingRuns int
}

func AggregateLedger(runs []LedgerRun, provider string) LedgerAggregate {
	var out LedgerAggregate
	for _, run := range runs {
		if !runBelongsToProvider(run, provider) {
			continue
		}
		out.Requests++
		out.InputTokens += run.InputTokens
		out.OutputTokens += run.OutputTokens
		out.CostUSD += observedCost(run)
		if accountingMissing(run) {
			out.AccountingMissingRuns++
		}
	}
	return out
}

func runBelongsToProvider(run LedgerRun, provider string) bool {
	if strings.EqualFold(run.SubscriptionBucket, provider) {
		return true
	}
	p := strings.ToLower(run.ModelProvider)
	m := strings.ToLower(run.ModelID)
	switch provider {
	case store.ProviderClaude:
		return p == "claude_cli"
	case store.ProviderCodex:
		return p == "codex_cli"
	case store.ProviderGrok:
		return p == "grok_cli"
	case store.ProviderMiMo:
		return p == "mimo_cli" && !strings.HasPrefix(m, "openrouter/")
	case store.ProviderMiniMax:
		return p == "opencode_cli" && strings.HasPrefix(m, "minimax/")
	case store.ProviderZAI:
		return p == "opencode_cli" && strings.HasPrefix(m, "zai-coding-plan/")
	default:
		return false
	}
}

func observedCost(run LedgerRun) float64 {
	if run.RealCostUSD != 0 {
		return run.RealCostUSD
	}
	if run.BillingModel == "subscription" || run.BillingModel == "free" {
		return 0
	}
	return run.CostUSD
}

func accountingMissing(run LedgerRun) bool {
	return run.Status == "success" && run.InputTokens == 0 &&
		run.OutputTokens == 0 && run.RealCostUSD == 0 && run.CostUSD == 0
}

func AggregateOpenRouterByHarness(runs []LedgerRun) []store.ORHarnessUsage {
	harnesses := make(map[string]*store.ORHarnessUsage)
	models := make(map[string]map[string]*store.ORModelUsage)
	for _, run := range runs {
		if !isOpenRouterModel(run.ModelID) {
			continue
		}
		harness := harnessLabel(run.ModelProvider)
		if harnesses[harness] == nil {
			harnesses[harness] = &store.ORHarnessUsage{
				Harness: harness, CostKind: store.ObservedCostMetered,
			}
			models[harness] = make(map[string]*store.ORModelUsage)
		}
		addOpenRouterRun(harnesses[harness], models[harness], run)
	}
	return sortedHarnesses(harnesses, models)
}

func addOpenRouterRun(
	harness *store.ORHarnessUsage,
	models map[string]*store.ORModelUsage,
	run LedgerRun,
) {
	cost := observedCost(run)
	harness.Requests++
	harness.InputTokens += run.InputTokens
	harness.OutputTokens += run.OutputTokens
	harness.CostUSD += cost
	if accountingMissing(run) {
		harness.AccountingMissingRuns++
	}
	model := models[run.ModelID]
	if model == nil {
		model = &store.ORModelUsage{Model: run.ModelID}
		models[run.ModelID] = model
	}
	model.Requests++
	model.InputTokens += run.InputTokens
	model.OutputTokens += run.OutputTokens
	model.CostUSD += cost
}

func sortedHarnesses(
	harnesses map[string]*store.ORHarnessUsage,
	models map[string]map[string]*store.ORModelUsage,
) []store.ORHarnessUsage {
	names := make([]string, 0, len(harnesses))
	for name := range harnesses {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]store.ORHarnessUsage, 0, len(names))
	for _, name := range names {
		modelNames := make([]string, 0, len(models[name]))
		for model := range models[name] {
			modelNames = append(modelNames, model)
		}
		sort.Strings(modelNames)
		for _, model := range modelNames {
			harnesses[name].Models = append(harnesses[name].Models, *models[name][model])
		}
		out = append(out, *harnesses[name])
	}
	return out
}

func harnessLabel(provider string) string {
	label := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(provider)), "_cli")
	if label == "" {
		return "unknown"
	}
	return label
}

func isOpenRouterModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return len(modelID) > len("openrouter/") && strings.HasPrefix(modelID, "openrouter/")
}

func WindowSince(days int) time.Time {
	return windowSince(time.Now(), days)
}

func windowSince(now time.Time, days int) time.Time {
	if days <= 0 {
		days = 30
	}
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(days - 1))
}
