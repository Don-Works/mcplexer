package runner

import (
	"math"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
)

func TestBillingStampClassification(t *testing.T) {
	cases := []struct {
		name              string
		provider, modelID string
		reportedUSD       float64
		wantBillingModel  string
		wantBucket        string
		wantRealCostUSD   float64
	}{
		{
			name:     "claude_cli subscription",
			provider: models.ProviderClaudeCLI, modelID: "claude-opus-4-8",
			reportedUSD:      64.95,
			wantBillingModel: "subscription", wantBucket: "claude",
			wantRealCostUSD: 0,
		},
		{
			name:     "opencode_cli openrouter metered with reported cost",
			provider: models.ProviderOpenCodeCLI, modelID: "openrouter/deepseek/deepseek-v4-pro",
			reportedUSD:      3.75,
			wantBillingModel: "metered", wantBucket: "",
			wantRealCostUSD: 3.75,
		},
		{
			name:     "opencode_cli zai subscription",
			provider: models.ProviderOpenCodeCLI, modelID: "zai-coding-plan/glm-5.1",
			reportedUSD:      0,
			wantBillingModel: "subscription", wantBucket: "zai",
			wantRealCostUSD: 0,
		},
		{
			name:     "opencode_cli minimax subscription",
			provider: models.ProviderOpenCodeCLI, modelID: "minimax/MiniMax-M2.5",
			reportedUSD:      2.0,
			wantBillingModel: "subscription", wantBucket: "minimax",
			wantRealCostUSD: 0,
		},
		{
			name:     "opencode_cli free tier",
			provider: models.ProviderOpenCodeCLI, modelID: "opencode/deepseek-v4-flash-free",
			reportedUSD:      0,
			wantBillingModel: "free", wantBucket: "",
			wantRealCostUSD: 0,
		},
		{
			name:     "grok_cli subscription",
			provider: models.ProviderGrokCLI, modelID: "grok-build",
			reportedUSD:      10.0,
			wantBillingModel: "subscription", wantBucket: "grok",
			wantRealCostUSD: 0,
		},
		{
			name:     "anthropic direct metered with reported cost",
			provider: models.ProviderAnthropic, modelID: "claude-opus-4-8",
			reportedUSD:      5.25,
			wantBillingModel: "metered", wantBucket: "",
			wantRealCostUSD: 5.25,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := models.ClassifyBilling(c.provider, c.modelID)
			if got := string(cl.Model); got != c.wantBillingModel {
				t.Errorf("BillingModel = %q, want %q", got, c.wantBillingModel)
			}
			if got := string(cl.Bucket); got != c.wantBucket {
				t.Errorf("SubscriptionBucket = %q, want %q", got, c.wantBucket)
			}
			realCost := models.RealCostUSD(c.provider, c.modelID, 1000, 500, c.reportedUSD)
			if math.Abs(realCost-c.wantRealCostUSD) > 1e-9 {
				t.Errorf("RealCostUSD = %v, want %v", realCost, c.wantRealCostUSD)
			}
		})
	}
}
