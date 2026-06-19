package models

import "testing"

func TestIsCLIProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{ProviderClaudeCLI, true},
		{ProviderOpenCodeCLI, true},
		{ProviderGrokCLI, true},
		{ProviderMiMoCLI, true},
		{ProviderGeminiCLI, true},
		{ProviderCodexCLI, true},
		{ProviderPiCLI, true},
		{ProviderAnthropic, false},
		{ProviderOpenAI, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsCLIProvider(tc.provider); got != tc.want {
			t.Errorf("IsCLIProvider(%q) = %v, want %v", tc.provider, got, tc.want)
		}
	}
}
