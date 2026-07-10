package usage

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestAggregateLedger(t *testing.T) {
	runs := []LedgerRun{
		{SubscriptionBucket: "claude", InputTokens: 1000, OutputTokens: 500, RealCostUSD: 0.05, Status: "success"},
		{SubscriptionBucket: "claude", InputTokens: 2000, OutputTokens: 1000, RealCostUSD: 0.10, Status: "success"},
		{SubscriptionBucket: "claude", InputTokens: 0, OutputTokens: 0, RealCostUSD: 0, Status: "success"},
		{SubscriptionBucket: "claude", InputTokens: 0, OutputTokens: 0, RealCostUSD: 0, Status: "failure"},
		{SubscriptionBucket: "codex", InputTokens: 500, OutputTokens: 200, RealCostUSD: 0.02, Status: "success"},
	}

	agg := AggregateLedger(runs, "claude")
	if agg.Requests != 4 {
		t.Errorf("requests = %d, want 4", agg.Requests)
	}
	if agg.InputTokens != 3000 {
		t.Errorf("input tokens = %d, want 3000", agg.InputTokens)
	}
	if agg.OutputTokens != 1500 {
		t.Errorf("output tokens = %d, want 1500", agg.OutputTokens)
	}
	if diff := agg.CostUSD - 0.15; diff > 0.001 || diff < -0.001 {
		t.Errorf("cost = %f, want 0.15", agg.CostUSD)
	}
	if agg.AccountingMissingRuns != 1 {
		t.Errorf("missing = %d, want 1 (only the success with 0/0/0)", agg.AccountingMissingRuns)
	}

	codex := AggregateLedger(runs, "codex")
	if codex.Requests != 1 {
		t.Errorf("codex requests = %d, want 1", codex.Requests)
	}
	if codex.AccountingMissingRuns != 0 {
		t.Errorf("codex missing = %d, want 0", codex.AccountingMissingRuns)
	}
}

func TestAggregateLedgerEmpty(t *testing.T) {
	agg := AggregateLedger(nil, "claude")
	if agg.Requests != 0 {
		t.Errorf("requests = %d, want 0", agg.Requests)
	}
}

func TestAggregateOpenRouterByHarness(t *testing.T) {
	runs := []LedgerRun{
		{ModelProvider: "claude_code", ModelID: "openrouter/anthropic/claude-sonnet-4", InputTokens: 1000, OutputTokens: 500, RealCostUSD: 0.05, Status: "success"},
		{ModelProvider: "claude_code", ModelID: "openrouter/anthropic/claude-sonnet-4", InputTokens: 2000, OutputTokens: 1000, RealCostUSD: 0.10, Status: "success"},
		{ModelProvider: "codex", ModelID: "openrouter/openai/gpt-4o", InputTokens: 500, OutputTokens: 200, RealCostUSD: 0.02, Status: "success"},
		{ModelProvider: "claude_code", ModelID: "anthropic/claude-sonnet-4", InputTokens: 100, OutputTokens: 50, RealCostUSD: 0.01, Status: "success"},
	}

	result := AggregateOpenRouterByHarness(runs)

	if len(result) != 2 {
		t.Fatalf("harnesses = %d, want 2", len(result))
	}

	var cc, cx store.ORHarnessUsage
	for _, h := range result {
		switch h.Harness {
		case "claude_code":
			cc = h
		case "codex":
			cx = h
		}
	}

	if cc.Requests != 2 {
		t.Errorf("cc requests = %d, want 2", cc.Requests)
	}
	if cc.InputTokens != 3000 {
		t.Errorf("cc input = %d, want 3000", cc.InputTokens)
	}
	if len(cc.Models) != 1 {
		t.Errorf("cc models = %d, want 1", len(cc.Models))
	}

	if cx.Requests != 1 {
		t.Errorf("cx requests = %d, want 1", cx.Requests)
	}
	if len(cx.Models) != 1 {
		t.Errorf("cx models = %d, want 1", len(cx.Models))
	}
}

func TestIsOpenRouterModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"openrouter/anthropic/claude-sonnet-4", true},
		{"openrouter/openai/gpt-4o", true},
		{"anthropic/claude-sonnet-4", false},
		{"gpt-4o", false},
		{"", false},
		{"openrouter/", false}, // too short after prefix
	}
	for _, tt := range tests {
		if got := isOpenRouterModel(tt.model); got != tt.want {
			t.Errorf("isOpenRouterModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}
