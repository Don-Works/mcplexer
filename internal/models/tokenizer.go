package models

import "math"

// mcplexer ships no BPE tokenizer today. These characters-per-token ratios
// are heuristic approximations — good enough to size and compare payloads and
// to state savings, not exact BPE counts. This file is the single conversion
// point so every consumer (compression measurement, context-cost stats,
// savings ledger) agrees on one estimate. A real per-model BPE table can slot
// in behind CharsPerToken without touching callers.
const (
	charsPerTokenAnthropic = 3.5
	charsPerTokenOpenAI    = 4.0
	charsPerTokenDefault   = 3.8
)

// DefaultContextProvider is the provider whose tokenizer ratio is used when
// estimating tokens for payloads consumed by the primary harness session.
// Claude Code / Anthropic is the dominant consumer of mcplexer tool results;
// callers that know the concrete model should pass it to EstimateTokens.
const DefaultContextProvider = ProviderAnthropic

// CharsPerToken returns the estimated characters-per-token ratio for a
// provider/model. modelID is currently unused for ratio selection (models
// within a provider share a ratio) but is part of the signature so a future
// per-model table can be introduced without a breaking change.
func CharsPerToken(provider, modelID string) float64 {
	switch provider {
	case ProviderAnthropic, ProviderClaudeCLI:
		return charsPerTokenAnthropic
	case ProviderOpenAI, ProviderOpenAICompat, ProviderCodexCLI:
		return charsPerTokenOpenAI
	default:
		return charsPerTokenDefault
	}
}

// EstimateTokensFromBytes estimates the tokens a payload of nBytes occupies
// for the given provider/model. Rounds up so a non-empty payload never
// estimates to zero tokens.
func EstimateTokensFromBytes(nBytes int, provider, modelID string) int {
	if nBytes <= 0 {
		return 0
	}
	cpt := CharsPerToken(provider, modelID)
	if cpt <= 0 {
		cpt = charsPerTokenDefault
	}
	return int(math.Ceil(float64(nBytes) / cpt))
}

// EstimateTokens estimates the tokens in text for the given provider/model.
func EstimateTokens(text, provider, modelID string) int {
	return EstimateTokensFromBytes(len(text), provider, modelID)
}

// EstimateContextTokens estimates the tokens a payload of nBytes occupies in
// the primary harness context, using the default context provider ratio. Use
// this for tool-result compression measurement where the concrete consuming
// model is not known at the gateway.
func EstimateContextTokens(nBytes int) int {
	return EstimateTokensFromBytes(nBytes, DefaultContextProvider, "")
}
