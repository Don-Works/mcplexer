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
	plan := strings.ToLower(strings.Join(strings.Fields(snapshot.Plan), " "))
	switch plan {
	case "token plan", "basic token plan", "pro token plan", "max token plan":
		return true
	default:
		return cfg.Limit > 0 && strings.EqualFold(cfg.Unit, store.UnitCredits)
	}
}

func estimatedMiMoCreditsWindow(
	cfg store.SourceConfig,
	local map[string]localStatsResult,
	observationMinutes int,
) (store.UsageWindow, bool) {
	result, ok := local["mimo"]
	if !ok || result.Err != nil {
		return store.UsageWindow{}, false
	}
	total, supported, partial := aggregateMiMoCredits(result.Stats)
	if !supported {
		return store.UsageWindow{}, false
	}
	label := strings.TrimSpace(cfg.WindowLabel)
	if label == "" {
		label = "Token Plan credits"
	}
	if partial {
		label = strings.TrimSuffix(label, " (estimate)")
		if !strings.Contains(strings.ToLower(label), "partial estimate") {
			label += " (partial estimate)"
		}
	} else if !strings.Contains(strings.ToLower(label), "estimate") {
		label += " (estimate)"
	}
	window := store.UsageWindow{
		ID:              "mimo_token_plan_credits",
		Label:           label,
		Unit:            store.UnitCredits,
		Used:            numberPtr(total),
		DurationMinutes: observationMinutes,
	}
	periodsAlign := cfg.WindowMinutes > 0 && observationMinutes == cfg.WindowMinutes
	if cfg.Limit > 0 && strings.EqualFold(cfg.Unit, store.UnitCredits) && periodsAlign {
		window.Limit = numberPtr(cfg.Limit)
		window.Remaining = numberPtr(math.Max(0, cfg.Limit-total))
		window.UsedPercent = numberPtr(math.Min(100, total/cfg.Limit*100))
	}
	return window, true
}

func aggregateMiMoCredits(stats []clistats.ModelStats) (float64, bool, bool) {
	var total float64
	var supported bool
	var partial bool
	for _, stat := range stats {
		credits, ok := estimateMiMoCredits(stat)
		if !ok {
			if isClearlyForeignMiMoStats(stat) {
				continue
			}
			if mimoStatsHaveTokens(stat) {
				partial = true
			}
			continue
		}
		supported = true
		total += credits
	}
	return total, supported, partial
}

func estimateMiMoCredits(stat clistats.ModelStats) (float64, bool) {
	model := strings.ToLower(strings.TrimSpace(stat.Model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		namespace := model[:slash]
		if namespace != "xiaomi" && namespace != "mimo" {
			return 0, false
		}
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

func mimoStatsHaveTokens(stat clistats.ModelStats) bool {
	return stat.InputTokens > 0 || stat.OutputTokens > 0 ||
		stat.CacheReadTokens > 0 || stat.CacheWriteTokens > 0
}

func isClearlyForeignMiMoStats(stat clistats.ModelStats) bool {
	model := strings.ToLower(strings.TrimSpace(stat.Model))
	slash := strings.LastIndex(model, "/")
	if slash < 0 {
		return false
	}
	namespace := model[:slash]
	return namespace != "xiaomi" && namespace != "mimo"
}
