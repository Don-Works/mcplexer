package models

import (
	"math"
	"testing"
)

func TestClassifyBilling(t *testing.T) {
	cases := []struct {
		provider, modelID string
		wantModel         BillingModel
		wantBucket        SubscriptionBucket
	}{
		{ProviderClaudeCLI, "claude-opus-4-8", BillingSubscription, BucketClaude},
		{ProviderClaudeCLI, "claude-sonnet-4-6", BillingSubscription, BucketClaude},
		{ProviderGrokCLI, "grok-build", BillingSubscription, BucketGrok},
		{ProviderOpenCodeCLI, "openrouter/deepseek/deepseek-v4-pro", BillingMetered, ""},
		{ProviderOpenCodeCLI, "openrouter/minimax/minimax-m3", BillingMetered, ""},
		{ProviderOpenCodeCLI, "zai-coding-plan/glm-5.1", BillingSubscription, BucketZAI},
		{ProviderOpenCodeCLI, "minimax/MiniMax-M2.5", BillingSubscription, BucketMiniMax},
		{ProviderOpenCodeCLI, "opencode/deepseek-v4-flash-free", BillingFree, ""},
		{ProviderMiMoCLI, "xiaomi/mimo-v2.5", BillingMetered, ""},
		{ProviderGeminiCLI, "gemini-2.5-pro", BillingMetered, ""},
		{ProviderPiCLI, "local-model", BillingMetered, ""},
		{ProviderAnthropic, "claude-opus-4-8", BillingMetered, ""},
		{ProviderAnthropic, "claude-sonnet-4-6", BillingMetered, ""},
		{ProviderOpenAI, "gpt-5.5", BillingMetered, ""},
		{ProviderOpenAI, "gpt-4o", BillingMetered, ""},
		{ProviderOpenAICompat, "qwen/qwen3-coder:free", BillingFree, ""},
		{ProviderOpenAICompat, "minimax/minimax-m3", BillingMetered, ""},
		{ProviderOpenAICompat, "no-such-model", BillingUnknown, ""},
		{"bogus-provider", "some-model", BillingUnknown, ""},
		{ProviderOpenCodeCLI, "unknown-model-xyz", BillingUnknown, ""},
		{"any", "model:free", BillingFree, ""},
		{ProviderOpenCodeCLI, "openrouter/minimax/MiniMax-M3", BillingMetered, ""},
	}
	for _, c := range cases {
		got := ClassifyBilling(c.provider, c.modelID)
		if got.Model != c.wantModel || got.Bucket != c.wantBucket {
			t.Errorf("ClassifyBilling(%q, %q) = {%s, %q}, want {%s, %q}",
				c.provider, c.modelID, got.Model, got.Bucket, c.wantModel, c.wantBucket)
		}
	}
}

func TestClassifyBillingCaseInsensitive(t *testing.T) {
	cases := []struct {
		provider, modelID string
		wantModel         BillingModel
		wantBucket        SubscriptionBucket
	}{
		{ProviderOpenCodeCLI, "  zai-coding-plan/GLM-5.1  ", BillingSubscription, BucketZAI},
		{ProviderOpenCodeCLI, "OpenRouter/deepseek/deepseek-v4-pro", BillingMetered, ""},
		{ProviderOpenCodeCLI, "MINIMAX/MiniMax-M3", BillingSubscription, BucketMiniMax},
	}
	for _, c := range cases {
		got := ClassifyBilling(c.provider, c.modelID)
		if got.Model != c.wantModel || got.Bucket != c.wantBucket {
			t.Errorf("ClassifyBilling(%q, %q) = {%s, %q}, want {%s, %q}",
				c.provider, c.modelID, got.Model, got.Bucket, c.wantModel, c.wantBucket)
		}
	}
}

func TestRealCostUSD(t *testing.T) {
	cases := []struct {
		name              string
		provider, modelID string
		inputTokens       int
		outputTokens      int
		reportedUSD       float64
		wantCost          float64
		wantApprox        bool
	}{
		{
			name:     "subscription claude_cli ignores reported cost",
			provider: ProviderClaudeCLI, modelID: "claude-opus-4-8",
			reportedUSD: 64.95, wantCost: 0,
		},
		{
			name:     "subscription grok_cli zero",
			provider: ProviderGrokCLI, modelID: "grok-build",
			reportedUSD: 10.0, wantCost: 0,
		},
		{
			name:     "subscription zai zero",
			provider: ProviderOpenCodeCLI, modelID: "zai-coding-plan/glm-5.1",
			reportedUSD: 5.0, wantCost: 0,
		},
		{
			name:     "subscription minimax zero",
			provider: ProviderOpenCodeCLI, modelID: "minimax/MiniMax-M2.5",
			reportedUSD: 2.0, wantCost: 0,
		},
		{
			name:     "free tier zero",
			provider: ProviderOpenCodeCLI, modelID: "opencode/deepseek-v4-flash-free",
			reportedUSD: 0, wantCost: 0,
		},
		{
			name:     "free suffix zero",
			provider: ProviderOpenAICompat, modelID: "qwen/qwen3-coder:free",
			reportedUSD: 0, wantCost: 0,
		},
		{
			name:     "metered openrouter uses reported cost",
			provider: ProviderOpenCodeCLI, modelID: "openrouter/deepseek/deepseek-v4-pro",
			reportedUSD: 3.75, wantCost: 3.75,
		},
		{
			name:     "metered openrouter minimax uses reported cost",
			provider: ProviderOpenCodeCLI, modelID: "openrouter/minimax/minimax-m3",
			reportedUSD: 1.50, wantCost: 1.50,
		},
		{
			name:     "metered anthropic falls back to EstimateCostUSD when reported=0",
			provider: ProviderAnthropic, modelID: "claude-opus-4-8",
			inputTokens: 1_000_000, outputTokens: 1_000_000,
			reportedUSD: 0, wantApprox: true,
		},
		{
			name:     "metered openai falls back to EstimateCostUSD",
			provider: ProviderOpenAI, modelID: "gpt-5.5",
			inputTokens: 1_000_000, outputTokens: 1_000_000,
			reportedUSD: 0, wantApprox: true,
		},
		{
			name:     "metered openai_compat uses reported cost",
			provider: ProviderOpenAICompat, modelID: "minimax/minimax-m3",
			reportedUSD: 0.88, wantCost: 0.88,
		},
		{
			name:     "metered mimo_cli uses reported cost",
			provider: ProviderMiMoCLI, modelID: "xiaomi/mimo-v2.5",
			reportedUSD: 0.42, wantCost: 0.42,
		},
		{
			name:     "metered gemini_cli uses reported cost",
			provider: ProviderGeminiCLI, modelID: "gemini-2.5-pro",
			reportedUSD: 0.35, wantCost: 0.35,
		},
		{
			// pi_cli is local/unknown-priced: adapter reports 0 and there is
			// no pricing table entry, so RealCostUSD stays 0 even with tokens.
			name:     "pi_cli local model zero cost",
			provider: ProviderPiCLI, modelID: "local-model",
			inputTokens: 1000, outputTokens: 500, reportedUSD: 0, wantCost: 0,
		},
		{
			name:     "unknown provider zero",
			provider: "bogus", modelID: "model",
			reportedUSD: 5.0, wantCost: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RealCostUSD(c.provider, c.modelID, c.inputTokens, c.outputTokens, c.reportedUSD)
			if c.wantApprox {
				estimated := EstimateCostUSD(c.provider, c.modelID, c.inputTokens, c.outputTokens)
				if estimated <= 0 {
					t.Fatalf("RealCostUSD estimate fallback = %v, want >0", estimated)
				}
				if math.Abs(got-estimated) > 1e-9 {
					t.Errorf("RealCostUSD = %v, want ~%v", got, estimated)
				}
				return
			}
			if math.Abs(got-c.wantCost) > 1e-9 {
				t.Errorf("RealCostUSD = %v, want %v", got, c.wantCost)
			}
		})
	}
}
