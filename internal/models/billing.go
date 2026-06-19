package models

import "strings"

type BillingModel string

const (
	BillingMetered      BillingModel = "metered"
	BillingSubscription BillingModel = "subscription"
	BillingFree         BillingModel = "free"
	BillingUnknown      BillingModel = "unknown"
)

type SubscriptionBucket string

const (
	BucketClaude  SubscriptionBucket = "claude"
	BucketGrok    SubscriptionBucket = "grok"
	BucketCodex   SubscriptionBucket = "codex"
	BucketMiniMax SubscriptionBucket = "minimax"
	BucketZAI     SubscriptionBucket = "zai"
)

type Classification struct {
	Model  BillingModel
	Bucket SubscriptionBucket
}

func ClassifyBilling(provider, modelID string) Classification {
	mid := strings.TrimSpace(strings.ToLower(modelID))
	p := strings.TrimSpace(provider)

	if strings.HasSuffix(mid, ":free") {
		return Classification{Model: BillingFree, Bucket: ""}
	}

	switch p {
	case ProviderClaudeCLI:
		return Classification{Model: BillingSubscription, Bucket: BucketClaude}
	case ProviderGrokCLI:
		return Classification{Model: BillingSubscription, Bucket: BucketGrok}
	case ProviderOpenCodeCLI:
		return classifyOpenCodeCLI(mid)
	case ProviderMiMoCLI:
		return Classification{Model: BillingMetered, Bucket: ""}
	case ProviderGeminiCLI:
		return Classification{Model: BillingMetered, Bucket: ""}
	case ProviderCodexCLI:
		return Classification{Model: BillingMetered, Bucket: ""}
	case ProviderPiCLI:
		// Pi routes to local/unknown-priced models; the adapter reports
		// CostUSD=0 and there's no pricing table entry, so cost stays 0.
		return Classification{Model: BillingMetered, Bucket: ""}
	case ProviderAnthropic, ProviderOpenAI:
		return Classification{Model: BillingMetered, Bucket: ""}
	case ProviderOpenAICompat:
		_, ok := LookupPricing(p, modelID)
		if ok {
			return Classification{Model: BillingMetered, Bucket: ""}
		}
		return Classification{Model: BillingUnknown, Bucket: ""}
	default:
		return Classification{Model: BillingUnknown, Bucket: ""}
	}
}

func classifyOpenCodeCLI(mid string) Classification {
	if _, ok := strings.CutPrefix(mid, "openrouter/"); ok {
		return Classification{Model: BillingMetered, Bucket: ""}
	}
	if _, ok := strings.CutPrefix(mid, "zai-coding-plan/"); ok {
		return Classification{Model: BillingSubscription, Bucket: BucketZAI}
	}
	if _, ok := strings.CutPrefix(mid, "minimax/"); ok {
		return Classification{Model: BillingSubscription, Bucket: BucketMiniMax}
	}
	if _, ok := strings.CutPrefix(mid, "opencode/"); ok {
		return Classification{Model: BillingFree, Bucket: ""}
	}
	_, ok := LookupPricing(ProviderOpenCodeCLI, mid)
	if ok {
		return Classification{Model: BillingMetered, Bucket: ""}
	}
	return Classification{Model: BillingUnknown, Bucket: ""}
}

func RealCostUSD(provider, modelID string, inputTokens, outputTokens int, reportedUSD float64) float64 {
	cl := ClassifyBilling(provider, modelID)
	if cl.Model == BillingMetered {
		if reportedUSD > 0 {
			return reportedUSD
		}
		return EstimateCostUSD(provider, modelID, inputTokens, outputTokens)
	}
	return 0
}
