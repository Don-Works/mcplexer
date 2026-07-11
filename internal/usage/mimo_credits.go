package usage

import (
	"math"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/clistats"
)

type mimoCreditCoefficients struct {
	input     float64
	output    float64
	cacheRead float64
}

var mimoCreditRates = map[string]mimoCreditCoefficients{
	"mimo-v2.5-pro": {input: 300, output: 600, cacheRead: 2.5},
	"mimo-v2.5":     {input: 100, output: 200, cacheRead: 2},
}

func isMiMoTokenPlan(snapshot *store.ProviderSnapshot, cfg store.SourceConfig) bool {
	return strings.Contains(strings.ToLower(snapshot.Plan), "token plan") ||
		(cfg.Limit > 0 && strings.EqualFold(cfg.Unit, store.UnitCredits))
}

func estimatedMiMoCreditsWindow(
	cfg store.SourceConfig,
	local map[string]localStatsResult,
) (store.UsageWindow, bool) {
	result, ok := local["mimo"]
	if !ok || result.Err != nil {
		return store.UsageWindow{}, false
	}
	total, supported := aggregateMiMoCredits(result.Stats)
	if !supported {
		return store.UsageWindow{}, false
	}
	label := strings.TrimSpace(cfg.WindowLabel)
	if label == "" {
		label = "Token Plan credits"
	}
	if !strings.Contains(strings.ToLower(label), "estimate") {
		label += " (estimate)"
	}
	window := store.UsageWindow{
		ID:              "mimo_token_plan_credits",
		Label:           label,
		Unit:            store.UnitCredits,
		Used:            numberPtr(total),
		DurationMinutes: cfg.WindowMinutes,
	}
	if cfg.Limit > 0 && strings.EqualFold(cfg.Unit, store.UnitCredits) {
		window.Limit = numberPtr(cfg.Limit)
		window.Remaining = numberPtr(math.Max(0, cfg.Limit-total))
		window.UsedPercent = numberPtr(math.Min(100, total/cfg.Limit*100))
	}
	return window, true
}

func aggregateMiMoCredits(stats []clistats.ModelStats) (float64, bool) {
	var total float64
	var supported bool
	for _, stat := range stats {
		credits, ok := estimateMiMoCredits(stat)
		if !ok {
			continue
		}
		supported = true
		total += credits
	}
	return total, supported
}

func estimateMiMoCredits(stat clistats.ModelStats) (float64, bool) {
	model := strings.ToLower(strings.TrimSpace(stat.Model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = model[slash+1:]
	}
	rate, ok := mimoCreditRates[model]
	if !ok {
		return 0, false
	}
	credits := float64(stat.InputTokens)*rate.input +
		float64(stat.OutputTokens)*rate.output +
		float64(stat.CacheReadTokens)*rate.cacheRead
	return credits, true
}
