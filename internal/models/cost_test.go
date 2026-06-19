package models

import (
	"math"
	"testing"
)

func TestLookupPricingKnownModels(t *testing.T) {
	cases := []struct {
		provider, model string
		wantIn, wantOut float64
	}{
		{ProviderAnthropic, "claude-fable-5", 10.00, 50.00},
		{ProviderAnthropic, "claude-opus-4-8", 5.00, 25.00},
		{ProviderAnthropic, "claude-opus-4-7", 5.00, 25.00},
		{ProviderAnthropic, "claude-sonnet-4-6", 3.00, 15.00},
		{ProviderAnthropic, "claude-haiku-4-5", 1.00, 5.00},
		{ProviderOpenAI, "gpt-5.5", 5.00, 30.00},
		{ProviderOpenAI, "gpt-4o", 2.50, 10.00},
		{ProviderOpenAI, "gpt-4o-mini", 0.15, 0.60},
		{ProviderOpenAI, "o1", 15.00, 60.00},
		{ProviderOpenAI, "o1-mini", 3.00, 12.00},
		{ProviderOpenAICompat, "minimax/minimax-m3", 0.30, 1.20},
		{ProviderOpenAICompat, "deepseek/deepseek-v4-flash", 0.10, 0.20},
		{ProviderOpenAICompat, "gemini-1.5-pro", 1.25, 5.00},
		{ProviderOpenAICompat, "gemini-1.5-flash", 0.075, 0.30},
		{ProviderOpenAICompat, "deepseek-chat", 0.27, 1.10},
	}
	for _, c := range cases {
		p, ok := LookupPricing(c.provider, c.model)
		if !ok {
			t.Errorf("LookupPricing(%q, %q) ok=false", c.provider, c.model)
			continue
		}
		if p.Input != c.wantIn || p.Output != c.wantOut {
			t.Errorf("LookupPricing(%q, %q) = %+v, want {%v,%v}",
				c.provider, c.model, p, c.wantIn, c.wantOut)
		}
	}
}

func TestLookupPricingUnknown(t *testing.T) {
	_, ok := LookupPricing(ProviderOpenAI, "no-such-model")
	if ok {
		t.Error("expected ok=false for unknown model")
	}
	_, ok = LookupPricing("bogus-provider", "gpt-4o")
	if ok {
		t.Error("expected ok=false for unknown provider")
	}
}

func TestEstimateCostUSDKnown(t *testing.T) {
	// 1M input + 1M output for gpt-4o = 2.50 + 10.00 = 12.50
	got := EstimateCostUSD(ProviderOpenAI, "gpt-4o", 1_000_000, 1_000_000)
	if math.Abs(got-12.50) > 1e-9 {
		t.Errorf("cost = %v, want 12.50", got)
	}
	// Partial million: 500k input + 250k output for gpt-4o-mini
	// = 0.5 * 0.15 + 0.25 * 0.60 = 0.075 + 0.150 = 0.225
	got = EstimateCostUSD(ProviderOpenAI, "gpt-4o-mini", 500_000, 250_000)
	if math.Abs(got-0.225) > 1e-9 {
		t.Errorf("cost = %v, want 0.225", got)
	}
}

func TestEstimateCostUSDUnknownReturnsZero(t *testing.T) {
	got := EstimateCostUSD(ProviderOpenAI, "nope", 1_000_000, 1_000_000)
	if got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

func TestEstimateCostUSDZeroTokens(t *testing.T) {
	got := EstimateCostUSD(ProviderAnthropic, "claude-opus-4-7", 0, 0)
	if got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

func TestLookupPricingOpenCodeOpenRouterFallback(t *testing.T) {
	p, ok := LookupPricing(ProviderOpenCodeCLI, "openrouter/minimax/minimax-m3")
	if !ok {
		t.Fatal("expected openrouter/-prefixed opencode model to resolve")
	}
	if p.Input != 0.30 || p.Output != 1.20 {
		t.Errorf("price = %+v, want {0.30, 1.20}", p)
	}
	// Subscription-plan IDs now resolve to estimated metered-tier equivalents.
	p, ok = LookupPricing(ProviderOpenCodeCLI, "zai-coding-plan/glm-5.1")
	if !ok {
		t.Fatal("expected zai-coding-plan/glm-5.1 to resolve via z-ai/glm-5.1")
	}
	if p.Input != 0.98 || p.Output != 3.08 {
		t.Errorf("price = %+v, want {0.98, 3.08}", p)
	}
}

func TestLookupPricingOpenCodeSubscriptionPlans(t *testing.T) {
	cases := []struct {
		modelID         string
		wantIn, wantOut float64
	}{
		{"zai-coding-plan/glm-5.1", 0.98, 3.08},
		{"zai-coding-plan/glm-4.7", 0.40, 1.75},
		{"minimax/MiniMax-M3", 0.30, 1.20},
		{"minimax/MiniMax-M2.5", 0.30, 1.20},
		{"openrouter/minimax/MiniMax-M3", 0.30, 1.20},
	}
	for _, c := range cases {
		p, ok := LookupPricing(ProviderOpenCodeCLI, c.modelID)
		if !ok {
			t.Errorf("LookupPricing(opencode_cli, %q) ok=false, want ok=true", c.modelID)
			continue
		}
		if p.Input != c.wantIn || p.Output != c.wantOut {
			t.Errorf("LookupPricing(opencode_cli, %q) = {%v,%v}, want {%v,%v}",
				c.modelID, p.Input, p.Output, c.wantIn, c.wantOut)
		}
		// Non-zero tokens must produce non-zero cost.
		cost := EstimateCostUSD(ProviderOpenCodeCLI, c.modelID, 100_000, 50_000)
		if cost <= 0 {
			t.Errorf("EstimateCostUSD(opencode_cli, %q, 100k, 50k) = %v, want >0", c.modelID, cost)
		}
	}
}

func TestLookupPricingFreeTierIsKnownFree(t *testing.T) {
	p, ok := LookupPricing(ProviderOpenAICompat, "qwen/qwen3-coder:free")
	if !ok {
		t.Fatal("expected :free tier to be a known model")
	}
	if p.Input != 0 || p.Output != 0 {
		t.Errorf("free tier price = %+v, want zero", p)
	}
}

func TestIsFrontierClass(t *testing.T) {
	cases := []struct {
		provider string
		modelID  string
		want     bool
	}{
		// Frontier by name — catches CLI/subscription providers absent
		// from the pricing table (the audit's worst spend offender).
		{ProviderClaudeCLI, "claude-opus-4-8", true},
		{ProviderClaudeCLI, "claude-fable-5", true},
		{ProviderAnthropic, "claude-opus-4-7", true},
		{ProviderAnthropic, "claude-fable-5", true},
		{ProviderOpenAI, "gpt-5.5", true},
		{ProviderOpenCodeCLI, "anthropic/claude-opus-4.8", true},
		// Frontier by price threshold.
		{ProviderOpenAI, "o1", true},
		// Workhorse tiers — must NOT be flagged.
		{ProviderAnthropic, "claude-sonnet-4-6", false},
		{ProviderAnthropic, "claude-haiku-4-5", false},
		{ProviderOpenAI, "gpt-5.4", false},
		{ProviderOpenAI, "gpt-5.4-mini", false},
		{ProviderOpenAI, "o1-mini", false},
		{ProviderOpenCodeCLI, "zai-coding-plan/glm-5.1", false},
		{ProviderOpenCodeCLI, "minimax/MiniMax-M3", false},
		{ProviderOpenCodeCLI, "openrouter/deepseek/deepseek-v4-pro", false},
		{ProviderGrokCLI, "grok-build", false},
		{ProviderOpenCodeCLI, "", false},
	}
	for _, c := range cases {
		if got := IsFrontierClass(c.provider, c.modelID); got != c.want {
			t.Errorf("IsFrontierClass(%q, %q) = %v, want %v", c.provider, c.modelID, got, c.want)
		}
	}
}
