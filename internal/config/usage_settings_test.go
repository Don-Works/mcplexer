package config

import "testing"

func TestValidateUsageSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sources []UsageSourceSettings
		wantErr bool
	}{
		{name: "local collector", sources: []UsageSourceSettings{{Provider: "codex", Plan: "Pro"}}},
		{name: "api collector", sources: []UsageSourceSettings{{Provider: "zai", AuthScopeID: "scope", SecretKey: "api_key", BaseURL: "https://api.z.ai"}}},
		{name: "duplicate openrouter accounts", sources: []UsageSourceSettings{{Provider: "openrouter", Harness: "opencode"}, {Provider: "openrouter", Harness: "mimo"}}, wantErr: true},
		{name: "unknown provider", sources: []UsageSourceSettings{{Provider: "other"}}, wantErr: true},
		{name: "half secret ref", sources: []UsageSourceSettings{{Provider: "minimax", SecretKey: "api_key"}}, wantErr: true},
		{name: "ssrf base url", sources: []UsageSourceSettings{{Provider: "openrouter", BaseURL: "https://127.0.0.1/internal"}}, wantErr: true},
		{name: "duplicate", sources: []UsageSourceSettings{{Provider: "codex"}, {Provider: "CODEX"}}, wantErr: true},
		{name: "limit without unit", sources: []UsageSourceSettings{{Provider: "mimo", Limit: 82_000_000_000}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUsageSources(tt.sources)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUsageSources() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
