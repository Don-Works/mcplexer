package usage

import (
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func observedForUnit(observed store.ObservedUsage, unit string) (float64, bool) {
	switch unit {
	case store.UnitRequests:
		return float64(observed.Requests), true
	case store.UnitTokens:
		if observed.TotalTokens > 0 {
			return float64(observed.TotalTokens), true
		}
		return float64(observed.InputTokens + observed.OutputTokens), true
	case store.UnitUSD:
		return observed.CostUSD, true
	default:
		return 0, false
	}
}

func hasObserved(observed store.ObservedUsage) bool {
	return observed.Requests > 0 || observed.TotalTokens > 0 || observed.InputTokens > 0 ||
		observed.OutputTokens > 0 || observed.CacheReadTokens > 0 ||
		observed.CacheWriteTokens > 0 || observed.CostUSD != 0 ||
		observed.AccountingMissingRuns > 0
}

func appendDetail(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func coalesceTime(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func numberPtr(value float64) *float64 { return &value }
