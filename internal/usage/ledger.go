// Package usage — ledger.go aggregates usage from the worker_runs
// ledger table. This is the "auto" source for providers whose billing
// data lives in mcplexer's own database.
package usage

import (
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// LedgerAggregate is the result of aggregating worker_runs for one
// subscription bucket over a time window.
type LedgerAggregate struct {
	Requests              int
	InputTokens           int
	OutputTokens          int
	CacheReadTokens       int
	CacheWriteTokens      int
	CostUSD               float64
	AccountingMissingRuns int
}

// LedgerRun is a slim projection of a WorkerRun used for aggregation.
type LedgerRun struct {
	ModelProvider      string
	ModelID            string
	BillingModel       string
	SubscriptionBucket string
	RealCostUSD        float64
	InputTokens        int
	OutputTokens       int
	CostUSD            int
	Status             string
}

// AggregateLedger computes the LedgerAggregate for a set of runs.
// Runs are filtered by subscriptionBucket; runs with empty/zero
// tokens AND zero cost on success are counted as accounting-missing.
func AggregateLedger(runs []LedgerRun, bucket string) LedgerAggregate {
	var agg LedgerAggregate
	for _, r := range runs {
		if r.SubscriptionBucket != bucket {
			continue
		}
		agg.Requests++
		agg.InputTokens += r.InputTokens
		agg.OutputTokens += r.OutputTokens
		agg.CostUSD += r.RealCostUSD
		if r.Status == "success" && r.InputTokens == 0 && r.OutputTokens == 0 && r.RealCostUSD == 0 {
			agg.AccountingMissingRuns++
		}
	}
	return agg
}

// AggregateOpenRouterByHarness groups OpenRouter-prefixed runs by
// harness (model_provider) and model. Runs whose ModelID starts with
// "openrouter/" are attributed to the harness indicated by
// ModelProvider.
func AggregateOpenRouterByHarness(runs []LedgerRun) []store.ORHarnessUsage {
	type harnessKey struct {
		provider string
		model    string
	}
	harnesses := make(map[string]*store.ORHarnessUsage)
	models := make(map[string]map[string]*store.ORModelUsage)

	for _, r := range runs {
		if !isOpenRouterModel(r.ModelID) {
			continue
		}
		h := r.ModelProvider
		if h == "" {
			h = "unknown"
		}
		modelName := r.ModelID

		if _, ok := harnesses[h]; !ok {
			harnesses[h] = &store.ORHarnessUsage{Harness: h}
			models[h] = make(map[string]*store.ORModelUsage)
		}
		he := harnesses[h]
		he.Requests++
		he.InputTokens += r.InputTokens
		he.OutputTokens += r.OutputTokens
		he.CostUSD += r.RealCostUSD
		if r.Status == "success" && r.InputTokens == 0 && r.OutputTokens == 0 && r.RealCostUSD == 0 {
			he.AccountingMissingRuns++
		}

		if _, ok := models[h][modelName]; !ok {
			models[h][modelName] = &store.ORModelUsage{Model: modelName}
		}
		me := models[h][modelName]
		me.Requests++
		me.InputTokens += r.InputTokens
		me.OutputTokens += r.OutputTokens
		me.CostUSD += r.RealCostUSD
	}

	result := make([]store.ORHarnessUsage, 0, len(harnesses))
	for _, he := range harnesses {
		ml := models[he.Harness]
		he.Models = make([]store.ORModelUsage, 0, len(ml))
		for _, me := range ml {
			he.Models = append(he.Models, *me)
		}
		result = append(result, *he)
	}
	return result
}

// isOpenRouterModel checks if a model_id uses the openrouter/ prefix.
func isOpenRouterModel(modelID string) bool {
	return len(modelID) > 11 && modelID[:11] == "openrouter/"
}

// WindowSince computes the start of a UTC day window.
func WindowSince(days int) time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(days - 1))
}
