package models

import "testing"

func TestCharsPerToken(t *testing.T) {
	tests := []struct {
		provider string
		want     float64
	}{
		{ProviderAnthropic, charsPerTokenAnthropic},
		{ProviderClaudeCLI, charsPerTokenAnthropic},
		{ProviderOpenAI, charsPerTokenOpenAI},
		{ProviderOpenAICompat, charsPerTokenOpenAI},
		{ProviderCodexCLI, charsPerTokenOpenAI},
		{"something-unknown", charsPerTokenDefault},
		{"", charsPerTokenDefault},
	}
	for _, tt := range tests {
		if got := CharsPerToken(tt.provider, ""); got != tt.want {
			t.Errorf("CharsPerToken(%q) = %v, want %v", tt.provider, got, tt.want)
		}
	}
}

func TestEstimateTokensFromBytes(t *testing.T) {
	tests := []struct {
		name     string
		nBytes   int
		provider string
		want     int
	}{
		{"zero", 0, ProviderAnthropic, 0},
		{"negative", -10, ProviderAnthropic, 0},
		{"anthropic-7-bytes", 7, ProviderAnthropic, 2},       // ceil(7/3.5)=2
		{"anthropic-350-bytes", 350, ProviderAnthropic, 100}, // ceil(350/3.5)=100
		{"openai-8-bytes", 8, ProviderOpenAI, 2},             // ceil(8/4.0)=2
		{"openai-400-bytes", 400, ProviderOpenAI, 100},       // ceil(400/4.0)=100
		{"unknown-defaults", 380, "weird", 100},              // ceil(380/3.8)=100
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EstimateTokensFromBytes(tt.nBytes, tt.provider, ""); got != tt.want {
				t.Errorf("EstimateTokensFromBytes(%d, %q) = %d, want %d", tt.nBytes, tt.provider, got, tt.want)
			}
		})
	}
}

func TestEstimateContextTokensMonotonic(t *testing.T) {
	// More bytes must never estimate to fewer tokens.
	prev := 0
	for _, n := range []int{0, 1, 100, 1000, 100000} {
		got := EstimateContextTokens(n)
		if got < prev {
			t.Fatalf("EstimateContextTokens not monotonic: n=%d gave %d < prev %d", n, got, prev)
		}
		prev = got
	}
}
