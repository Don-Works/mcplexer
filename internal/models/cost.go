package models

import "strings"

// PricePerMillion is USD per 1,000,000 tokens.
type PricePerMillion struct {
	Input  float64
	Output float64
}

// pricingKey is (provider, modelID); pricing is keyed precisely so the
// same modelID under different providers doesn't collide.
type pricingKey struct {
	provider string
	modelID  string
}

// pricingTable is the seed table of well-known model prices.
//
// Prices are USD per million tokens, sourced from public docs and the
// OpenRouter live catalogue as of 2026-06. When a model isn't here,
// LookupPricing returns ok=false and EstimateCostUSD returns 0; the
// caller should treat that as "unknown".
//
// Anthropic models accept either the stable alias ("claude-opus-4-8")
// or a date-suffixed snapshot ID; we key on the alias.
//
// `:free` OpenRouter tiers are listed with explicit zero prices so cost
// accounting can distinguish "known to be free" (ok=true, $0) from
// "price unknown" (ok=false).
var pricingTable = map[pricingKey]PricePerMillion{
	// Anthropic - https://www.anthropic.com/pricing
	{ProviderAnthropic, "claude-fable-5"}:    {Input: 10.00, Output: 50.00},
	{ProviderAnthropic, "claude-opus-4-8"}:   {Input: 5.00, Output: 25.00},
	{ProviderAnthropic, "claude-opus-4-7"}:   {Input: 5.00, Output: 25.00},
	{ProviderAnthropic, "claude-sonnet-4-6"}: {Input: 3.00, Output: 15.00},
	{ProviderAnthropic, "claude-haiku-4-5"}:  {Input: 1.00, Output: 5.00},

	// OpenAI - https://openai.com/api/pricing
	{ProviderOpenAI, "gpt-5.5"}:      {Input: 5.00, Output: 30.00},
	{ProviderOpenAI, "gpt-5.4"}:      {Input: 2.50, Output: 15.00},
	{ProviderOpenAI, "gpt-5.4-mini"}: {Input: 0.75, Output: 4.50},
	{ProviderOpenAI, "gpt-5.4-nano"}: {Input: 0.20, Output: 1.25},
	{ProviderOpenAI, "gpt-4o"}:       {Input: 2.50, Output: 10.00},
	{ProviderOpenAI, "gpt-4o-mini"}:  {Input: 0.15, Output: 0.60},
	{ProviderOpenAI, "o1"}:           {Input: 15.00, Output: 60.00},
	{ProviderOpenAI, "o1-mini"}:      {Input: 3.00, Output: 12.00},

	// OpenRouter-style namespaced IDs (openai_compat). Frontier.
	{ProviderOpenAICompat, "anthropic/claude-fable-5"}:  {Input: 10.00, Output: 50.00},
	{ProviderOpenAICompat, "anthropic/claude-opus-4.8"}: {Input: 5.00, Output: 25.00},
	{ProviderOpenAICompat, "openai/gpt-5.5"}:            {Input: 5.00, Output: 30.00},

	// OpenRouter discounted heavy hitters - the delegation sweet spot.
	{ProviderOpenAICompat, "minimax/minimax-m3"}:         {Input: 0.30, Output: 1.20},
	{ProviderOpenAICompat, "deepseek/deepseek-v4-pro"}:   {Input: 0.44, Output: 0.87},
	{ProviderOpenAICompat, "deepseek/deepseek-v4-flash"}: {Input: 0.10, Output: 0.20},
	{ProviderOpenAICompat, "z-ai/glm-5.1"}:               {Input: 0.98, Output: 3.08},
	{ProviderOpenAICompat, "z-ai/glm-5"}:                 {Input: 0.60, Output: 1.92},
	{ProviderOpenAICompat, "qwen/qwen3.7-plus"}:          {Input: 0.40, Output: 1.60},
	{ProviderOpenAICompat, "qwen/qwen3.7-max"}:           {Input: 1.25, Output: 3.75},
	{ProviderOpenAICompat, "moonshotai/kimi-k2.6"}:       {Input: 0.68, Output: 3.41},
	{ProviderOpenAICompat, "moonshotai/kimi-k2.5"}:       {Input: 0.40, Output: 1.90},
	{ProviderOpenAICompat, "x-ai/grok-4.3"}:              {Input: 1.25, Output: 2.50},

	// OpenRouter free tiers - known-free, not unknown.
	{ProviderOpenAICompat, "nvidia/nemotron-3-ultra-550b-a55b:free"}: {},
	{ProviderOpenAICompat, "nvidia/nemotron-3-super-120b-a12b:free"}: {},
	{ProviderOpenAICompat, "nex-agi/nex-n2-pro:free"}:                {},
	{ProviderOpenAICompat, "qwen/qwen3-coder:free"}:                  {},
	{ProviderOpenAICompat, "openai/gpt-oss-120b:free"}:               {},

	// Google Gemini - billable via OpenAI-compatible gateways. Listed under
	// openai_compat so callers using AI Gateway / Vertex compat endpoints
	// get a price; native Gemini provider would need its own entry.
	{ProviderOpenAICompat, "google/gemini-3.5-flash"}: {Input: 1.50, Output: 9.00},
	{ProviderOpenAICompat, "gemini-1.5-pro"}:          {Input: 1.25, Output: 5.00},
	{ProviderOpenAICompat, "gemini-1.5-flash"}:        {Input: 0.075, Output: 0.30},

	// DeepSeek - https://api-docs.deepseek.com/quick_start/pricing
	{ProviderOpenAICompat, "deepseek-chat"}: {Input: 0.27, Output: 1.10},

	// Zhipu GLM - https://openrouter.ai/models/z-ai/glm-4.7 ($0.40/$1.75 per M)
	{ProviderOpenAICompat, "z-ai/glm-4.7"}: {Input: 0.40, Output: 1.75},

	// MiniMax M2.5 - https://platform.minimax.io/docs/guides/pricing-paygo ($0.30/$1.20 per M)
	{ProviderOpenAICompat, "minimax/minimax-m2.5"}: {Input: 0.30, Output: 1.20},
}

// LookupPricing returns the per-million-token price for (provider, modelID).
// The second return is false if the model isn't in the seeded table.
//
// opencode_cli model IDs are namespaced by opencode's own provider
// ("openrouter/z-ai/glm-5", "zai-coding-plan/glm-5.1"). The
// openrouter/-prefixed ones bill per token at OpenRouter list prices, so
// they fall back to the openai_compat entry for the trimmed ID.
//
// Subscription-plan IDs (zai-coding-plan/*, minimax/*, etc.) are billed
// against a flat subscription, not per-token. The adapter itself reports
// cost:0 for these. We resolve them to an ESTIMATED metered-tier-equivalent
// from the pricing table so the Delegations ROI panel shows a sensible
// list-price figure — this is NOT the actual marginal cost.
func LookupPricing(provider, modelID string) (PricePerMillion, bool) {
	if p, ok := pricingTable[pricingKey{provider: provider, modelID: modelID}]; ok {
		return p, true
	}
	if provider == ProviderOpenCodeCLI {
		key := modelID
		if rest, ok := strings.CutPrefix(key, "openrouter/"); ok {
			key = rest
		} else if rest, ok := strings.CutPrefix(key, "zai-coding-plan/"); ok {
			key = "z-ai/" + rest
		}
		// Lowercase for table lookup — ledger emits mixed case (MiniMax-M3).
		p, found := pricingTable[pricingKey{ProviderOpenAICompat, strings.ToLower(key)}]
		return p, found
	}
	return PricePerMillion{}, false
}

// EstimateCostUSD returns the dollar cost for a single call. Returns 0
// if the (provider, modelID) pair isn't in the pricing table.
func EstimateCostUSD(provider, modelID string, inputTokens, outputTokens int) float64 {
	p, ok := LookupPricing(provider, modelID)
	if !ok {
		return 0
	}
	const perMillion = 1_000_000.0
	in := float64(inputTokens) / perMillion * p.Input
	out := float64(outputTokens) / perMillion * p.Output
	return in + out
}

// Frontier-tier price thresholds (USD per million tokens). A model at or
// above either threshold is treated as top-tier for delegation purposes.
// Tuned to catch opus/fable/gpt-5.5/o1-class while leaving the workhorse
// tiers (sonnet, gpt-5.4, haiku, glm, minimax, deepseek) unflagged.
const (
	frontierInputPriceThreshold  = 5.0
	frontierOutputPriceThreshold = 25.0
)

// frontierNameMarkers are case-insensitive substrings that identify a
// top-tier model by id alone. Needed because subscription/CLI providers
// (claude_cli, grok_cli, mimo_cli) aren't in the pricing table, so a price-only
// check would miss claude_cli/claude-opus-* — the single biggest spend
// line in the first-12h delegation audit.
var frontierNameMarkers = []string{"opus", "fable", "mythos", "gpt-5.5", "gpt5.5"}

// IsFrontierClass reports whether (provider, modelID) is a top-tier model
// that should only be used as a delegated worker in exceptional cases —
// a frontier worker's quality edge over a workhorse model is usually
// closed for free by parent review, while its cost is multiples higher.
// The check is name-first (catches CLI/subscription providers absent from
// the pricing table) then price-threshold (catches metered providers).
func IsFrontierClass(provider, modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	for _, marker := range frontierNameMarkers {
		if strings.Contains(id, marker) {
			return true
		}
	}
	if p, ok := LookupPricing(provider, modelID); ok {
		if p.Input >= frontierInputPriceThreshold || p.Output >= frontierOutputPriceThreshold {
			return true
		}
	}
	return false
}
